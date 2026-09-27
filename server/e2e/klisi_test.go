package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"
	lkauth "github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/encoding/protojson"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/config"
	"klisi/internal/httpapi"
	"klisi/internal/store"
)

// A klisi is klisi as main runs it, in this process: httpapi.New with the
// stub bundle, served by an http.Server with main's timeouts on a loopback
// port, its Background work beside it, and SQLite on disk. Restart stops
// it as main does, and starts a new one on the same database and address.
type klisi struct {
	t      *testing.T
	cfg    config.Config
	bundle *moil.Bundle
	url    string
	// client is a browser's, except that it doesn't follow redirects.
	client *http.Client

	// db and stop belong to the klisi that runs now.
	db   *store.Store
	stop func()
}

func startKlisi(t *testing.T) *klisi {
	t.Helper()
	needSuite(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	k := &klisi{
		t: t,
		cfg: config.Config{
			Addr:          address,
			BaseURL:       "http://" + address,
			SessionSecret: rand.Text() + rand.Text(),
			DBPath:        filepath.Join(t.TempDir(), "klisi.db"),
			// No LiveKit runs. Where these tests take klisi, it only tries
			// to clear a room's recording flag when egress ends, and
			// carries on when that fails.
			LiveKitURL:       "ws://" + closedAddress(t),
			LiveKitAPIKey:    "klisi-e2e",
			LiveKitAPISecret: rand.Text() + rand.Text(),
			S3Endpoint:       objects.endpoint,
			S3PublicEndpoint: objects.endpoint,
			S3EgressEndpoint: objects.endpoint,
			S3Bucket:         objects.bucket,
			S3AccessKey:      objects.accessKey,
			S3SecretKey:      objects.secretKey,
			S3Region:         objects.region,
		},
		bundle: stubBundle(t),
		url:    "http://" + address,
		client: &http.Client{
			Timeout:       patience,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	k.cfg.LiveKitPublicURL = k.cfg.LiveKitURL
	k.start(listener)
	t.Cleanup(func() { k.stop() })
	return k
}

// stubBundle is the bundle the tests publish in place of the real
// transcriber (testdata/transcribe-stub).
func stubBundle(t *testing.T) *moil.Bundle {
	t.Helper()
	bundle, err := moil.LoadBundle(os.DirFS("testdata/transcribe-stub"))
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

// closedAddress is a loopback address nothing listens on.
func closedAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

// start does what main does, on listener.
func (k *klisi) start(listener net.Listener) {
	k.t.Helper()
	db, err := store.Open(k.cfg.DBPath)
	if err != nil {
		k.t.Fatal(err)
	}
	handler, background, err := httpapi.New(k.cfg, nil, db, k.bundle)
	if err != nil {
		k.t.Fatal(err)
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		background.Run(ctx)
	}()
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	k.db = db
	// As main does on SIGTERM: stop the background work and the HTTP
	// server, then close moil, which disconnects the machines.
	k.stop = sync.OnceFunc(func() {
		cancel()
		shutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		if err := server.Shutdown(shutdown); err != nil {
			k.t.Errorf("shutting klisi's HTTP server down: %v", err)
		}
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			k.t.Errorf("klisi served until %v", err)
		}
		if err := background.Close(); err != nil {
			k.t.Errorf("closing moil: %v", err)
		}
		<-ran
		if err := db.Close(); err != nil {
			k.t.Errorf("closing klisi's database: %v", err)
		}
	})
}

// Restart stops klisi as main does, then starts a new klisi on the same
// database and address, as a deploy would.
func (k *klisi) Restart() {
	k.t.Helper()
	k.stop()
	listener, err := net.Listen("tcp", k.cfg.Addr)
	if err != nil {
		k.t.Fatalf("listening at klisi's address again: %v", err)
	}
	k.start(listener)
}

func (k *klisi) moilURL() string { return k.url + api.MoilBasePath }

// A host is someone signed in to klisi in a browser, using its API as the
// SPA does: with their session cookie, and the CSRF header.
type host struct {
	k      *klisi
	sub    string
	cookie *http.Cookie
}

func (k *klisi) signIn(sub string) *host {
	k.t.Helper()
	sessions := auth.NewSessions(k.cfg.SessionSecret, k.cfg.BaseURL, nil)
	recorder := httptest.NewRecorder()
	if err := sessions.Set(recorder, auth.Session{Sub: sub, Email: sub + "@klisi.dev", Name: sub}); err != nil {
		k.t.Fatal(err)
	}
	return &host{k: k, sub: sub, cookie: recorder.Result().Cookies()[0]}
}

// request sends a request as the SPA does and returns klisi's answer.
func (h *host) request(method, target string, body any) (*http.Response, []byte) {
	h.k.t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			h.k.t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, h.k.url+target, reader)
	if err != nil {
		h.k.t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.AddCookie(h.cookie)
	request.Header.Set("X-Klisi-Csrf", "1")
	response, err := h.k.client.Do(request)
	if err != nil {
		h.k.t.Fatalf("%s %s: %v", method, target, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		h.k.t.Fatalf("%s %s: %v", method, target, err)
	}
	return response, data
}

// call makes a request that must be answered with want, and decodes the
// answer into out, if set.
func (h *host) call(method, target string, body any, want int, out any) *http.Response {
	h.k.t.Helper()
	response, data := h.request(method, target, body)
	if response.StatusCode != want {
		h.k.t.Fatalf("%s %s as %s = %d %s, want %d", method, target, h.sub, response.StatusCode, data, want)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			h.k.t.Fatalf("%s %s = %s: %v", method, target, data, err)
		}
	}
	return response
}

func (h *host) createRoom(name string) api.RoomInfo {
	h.k.t.Helper()
	var room api.RoomInfo
	h.call(http.MethodPost, api.RoomsPath, api.CreateRoomRequest{Name: name}, http.StatusCreated, &room)
	return room
}

func (h *host) machines() api.MachinesResponse {
	h.k.t.Helper()
	var page api.MachinesResponse
	h.call(http.MethodGet, api.MachinesPath, nil, http.StatusOK, &page)
	return page
}

// machine returns one of the host's machines, as /machines lists it.
func (h *host) machine(id string) api.MachineInfo {
	h.k.t.Helper()
	for _, machine := range h.machines().Machines {
		if machine.ID == id {
			return machine
		}
	}
	h.k.t.Fatalf("%s's machines don't include %s", h.sub, id)
	return api.MachineInfo{}
}

// waitForMachine waits until /machines shows one of the host's machines as
// ready says.
func (h *host) waitForMachine(id, what string, ready func(api.MachineInfo) bool) api.MachineInfo {
	h.k.t.Helper()
	return await(h.k.t, "machine "+id+" to be "+what, func() api.MachineInfo { return h.machine(id) }, ready)
}

// recordings returns the room's recordings as the dashboard lists them,
// by ID.
func (h *host) recordings(room api.RoomInfo) map[string]api.RecordingInfo {
	h.k.t.Helper()
	var list []api.RecordingInfo
	h.call(http.MethodGet, fill(api.RoomRecordingsPath, room.Slug), nil, http.StatusOK, &list)
	byID := make(map[string]api.RecordingInfo, len(list))
	for _, recording := range list {
		byID[recording.ID] = recording
	}
	return byID
}

// transcript returns a recording's transcript, as the dashboard shows it.
func (h *host) transcript(r *recording) *api.TranscriptInfo {
	h.k.t.Helper()
	info, ok := h.recordings(r.room)[r.id]
	if !ok {
		h.k.t.Fatalf("recording %s isn't listed", r.id)
	}
	return info.Transcript
}

// waitTranscript waits until the dashboard shows the recording's
// transcript as ok says, and returns it.
func (h *host) waitTranscript(r *recording, what string, ok func(*api.TranscriptInfo) bool) *api.TranscriptInfo {
	h.k.t.Helper()
	return await(h.k.t, "the transcript of "+r.id+" to be "+what, func() *api.TranscriptInfo { return h.transcript(r) }, ok)
}

func (h *host) waitStatus(r *recording, status string) *api.TranscriptInfo {
	h.k.t.Helper()
	return h.waitTranscript(r, status, func(info *api.TranscriptInfo) bool {
		return info != nil && info.Status == status
	})
}

// downloadTranscript downloads a transcript as a browser does: klisi
// redirects to S3, which serves the file.
func (h *host) downloadTranscript(r *recording, format string) ([]byte, http.Header) {
	h.k.t.Helper()
	response := h.call(http.MethodGet, fill(api.RecordingTranscriptDownloadPath, r.id)+"?format="+format, nil, http.StatusFound, nil)
	location := response.Header.Get("Location")
	if !strings.HasPrefix(location, objects.endpoint+"/") {
		h.k.t.Fatalf("the %s download redirects to %q, not S3 at %s", format, location, objects.endpoint)
	}
	// A fresh browser: S3 must not need klisi's cookie.
	download, err := http.Get(location)
	if err != nil {
		h.k.t.Fatal(err)
	}
	defer download.Body.Close()
	body, err := io.ReadAll(download.Body)
	if err != nil || download.StatusCode != http.StatusOK {
		h.k.t.Fatalf("GET %s: %d %s %v", location, download.StatusCode, body, err)
	}
	return body, download.Header
}

// A recording is a meeting recording as egress makes it: a row, then a
// file in S3, then LiveKit's word that it's done.
type recording struct {
	id, egressID, key string
	room              api.RoomInfo
	started           time.Time
	// data is the recording's file, and sum its SHA-256.
	data []byte
	sum  string
}

// startRecording starts recording a meeting in room, as recording.Start
// does, bar asking LiveKit's egress: the row as Start inserts it, then the
// signed egress_updated webhook that says egress is recording. The file
// lands in S3 at the key Start gives egress.
func (k *klisi) startRecording(room api.RoomInfo, by string) *recording {
	k.t.Helper()
	started := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	r := &recording{
		id: randomHex(16), egressID: "EG_" + randomHex(6), room: room, started: started,
		data: make([]byte, 64<<10),
	}
	// An Ogg page's capture pattern, then noise: bytes nobody else has.
	rand.Read(r.data)
	copy(r.data, "OggS\x00\x02")
	sum := sha256.Sum256(r.data)
	r.sum = hex.EncodeToString(sum[:])
	r.key = path.Join("recordings", room.Slug, r.id, started.UTC().Format("2006-01-02 15-04")+" - "+room.Name+".ogg")
	if err := k.db.InsertRecording(context.Background(), store.Recording{
		ID: r.id, RoomID: room.ID, RoomSlug: room.Slug, EgressID: r.egressID,
		Status: "starting", StartedBy: by, StartedAt: started.Unix(), AudioOnly: true,
	}); err != nil {
		k.t.Fatal(err)
	}
	if status := k.webhook(&livekit.WebhookEvent{
		Event: "egress_updated", Id: "EV_" + randomHex(6), CreatedAt: time.Now().Unix(),
		EgressInfo: &livekit.EgressInfo{
			EgressId: r.egressID, RoomName: room.Slug, Status: livekit.EgressStatus_EGRESS_ACTIVE,
			StartedAt: started.UnixNano(),
		},
	}, k.cfg.LiveKitAPISecret); status != http.StatusOK {
		k.t.Fatalf("egress_updated webhook: %d", status)
	}
	if status := k.recordingStatus(r); status != "recording" {
		k.t.Fatalf("the recording is %q once egress is active", status)
	}
	objects.put(k.t, r.key, r.data, "audio/ogg")
	return r
}

// ended is the egress_ended webhook LiveKit sends when egress has
// uploaded the recording.
func (r *recording) ended() *livekit.WebhookEvent {
	ended := time.Now()
	// Where egress uploaded it, at KLISI_S3_EGRESS_ENDPOINT.
	location, err := url.JoinPath(objects.endpoint, objects.bucket, r.key)
	if err != nil {
		panic(err)
	}
	return &livekit.WebhookEvent{
		Event: "egress_ended", Id: "EV_" + randomHex(6), CreatedAt: ended.Unix(),
		EgressInfo: &livekit.EgressInfo{
			EgressId: r.egressID, RoomName: r.room.Slug, Status: livekit.EgressStatus_EGRESS_COMPLETE,
			StartedAt: r.started.UnixNano(), EndedAt: ended.UnixNano(),
			FileResults: []*livekit.FileInfo{{
				Filename:  r.key,
				StartedAt: r.started.UnixNano(),
				EndedAt:   ended.UnixNano(),
				Duration:  int64(ended.Sub(r.started)),
				Size:      int64(len(r.data)),
				Location:  location,
			}},
		},
	}
}

// endRecording ends a recording the way egress does: LiveKit's signed
// egress_ended webhook completes it.
func (k *klisi) endRecording(r *recording) {
	k.t.Helper()
	if status := k.webhook(r.ended(), k.cfg.LiveKitAPISecret); status != http.StatusOK {
		k.t.Fatalf("egress_ended webhook: %d", status)
	}
	if status := k.recordingStatus(r); status != "completed" {
		k.t.Fatalf("the recording is %q after egress_ended", status)
	}
}

// record records a meeting in room from start to end.
func (k *klisi) record(room api.RoomInfo, by string) *recording {
	k.t.Helper()
	r := k.startRecording(room, by)
	k.endRecording(r)
	return r
}

// webhook sends event to klisi as LiveKit sends webhooks
// (protocol/webhook.URLNotifier): protobuf JSON, signed by a token of the
// API key that carries the body's SHA-256, signed with secret.
func (k *klisi) webhook(event *livekit.WebhookEvent, secret string) int {
	k.t.Helper()
	body, err := protojson.Marshal(event)
	if err != nil {
		k.t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	token, err := lkauth.NewAccessToken(k.cfg.LiveKitAPIKey, secret).
		SetValidFor(5 * time.Minute).
		SetSha256(base64.StdEncoding.EncodeToString(sum[:])).
		ToJWT()
	if err != nil {
		k.t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, k.url+api.LiveKitWebhookPath, bytes.NewReader(body))
	if err != nil {
		k.t.Fatal(err)
	}
	request.Header.Set("Authorization", token)
	request.Header.Set("Content-Type", "application/webhook+json")
	response, err := k.client.Do(request)
	if err != nil {
		k.t.Fatalf("webhook: %v", err)
	}
	response.Body.Close()
	return response.StatusCode
}

// recordingStatus is the status of the recording's row.
func (k *klisi) recordingStatus(r *recording) string {
	k.t.Helper()
	row, err := k.db.RecordingByID(context.Background(), r.id)
	if err != nil {
		k.t.Fatal(err)
	}
	return row.Status
}

// transcriptKey is where klisi keeps a recording's transcript in format:
// beside the recording, under its basename.
func (r *recording) transcriptKey(format string) string {
	return strings.TrimSuffix(r.key, ".ogg") + "." + format
}

// transcriptLine is the line of the stub's transcript that proves it read
// the recording.
func (r *recording) transcriptLine() string {
	return fmt.Sprintf("It read %d bytes with SHA-256 %s.", len(r.data), r.sum)
}

// watchTranscriptRow follows the recording's transcript row through its
// own connection to klisi's database, which outlives klisi's restarts. The
// function it returns stops following, and returns the row's statuses in
// the order it had them.
func watchTranscriptRow(t *testing.T, k *klisi, r *recording) (statuses func() []string) {
	t.Helper()
	db, err := store.Open(k.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var seen []string
	var failure error
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			row, err := db.Transcript(ctx, r.id)
			switch {
			case errors.Is(err, sql.ErrNoRows), ctx.Err() != nil:
			case err != nil:
				failure = err
				return
			case len(seen) == 0 || seen[len(seen)-1] != row.Status:
				seen = append(seen, row.Status)
			}
			time.Sleep(time.Millisecond)
		}
	}()
	stop := sync.OnceValue(func() []string {
		cancel()
		<-done
		db.Close()
		if failure != nil {
			t.Errorf("following the transcript row: %v", failure)
		}
		return seen
	})
	t.Cleanup(func() { stop() })
	return stop
}

// fill fills in a route's one wildcard.
func fill(route, value string) string {
	start, end := strings.Index(route, "{"), strings.Index(route, "}")
	return route[:start] + value + route[end+1:]
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

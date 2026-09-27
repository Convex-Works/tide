package transcripts_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"
	"git.convex.works/ConvexWorks/moil/sdk/go/moiltest"
	protocol "github.com/livekit/protocol/livekit"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/recording"
	"klisi/internal/rooms"
	"klisi/internal/store"
	"klisi/internal/transcripts"
)

// waitTimeout bounds every wait for something to happen.
const waitTimeout = 10 * time.Second

// An env is klisi's transcript pipeline, composed of its real pieces: a
// moil server that fake machines reach over HTTP and WebSocket, SQLite on
// disk, the recording and rooms handlers, the transcripts service, both
// reconcilers, and object storage over HTTP, all on one clock.
//
// Machines reach moil through a front door that stays put when klisi
// restarts, as klisi's own address does.
type env struct {
	t      *testing.T
	path   string // the database's file
	db     *store.Store
	clock  *clock
	s3     *fakeS3
	bundle *moil.Bundle
	front  *httptest.Server

	mu   sync.Mutex
	moil *moil.Server // the one the front door serves

	service       *transcripts.Service
	recordings    *recording.Handler
	rooms         *rooms.Handler
	cancelService func() // klisi starts stopping
	waitService   func() // and has stopped
	recorded      int
	probes        int
	configure     []func(*transcripts.Config)
}

func newEnv(t *testing.T, configure ...func(*transcripts.Config)) *env {
	t.Helper()
	path := filepath.Join(t.TempDir(), "klisi.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bundle, err := transcripts.Bundle()
	if err != nil {
		t.Fatal(err)
	}
	clock := newClock()
	e := &env{t: t, path: path, db: db, clock: clock, s3: newFakeS3(t, clock), bundle: bundle, configure: configure}
	e.front = httptest.NewServer(http.StripPrefix(api.MoilBasePath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		server := e.moil
		e.mu.Unlock()
		server.Handler().ServeHTTP(w, r)
	})))
	t.Cleanup(e.front.Close)
	e.start()
	t.Cleanup(e.stop)
	return e
}

// start starts klisi's side: a moil server with the transcribe bundle, the
// transcripts service reconciling beside it, and the recording reconciler.
// Only nudges and the pass at start drive the transcripts reconciler, as its
// tick is an hour away; the recording reconciler runs every few
// milliseconds, on the env's clock.
func (e *env) start() {
	e.t.Helper()
	server, err := moil.NewServer(moil.Config{
		Name: "klisi", VerificationURL: e.front.URL + "/machines", Store: e.db,
		MaxDataBytes: 4 << 20, KeepFinished: 5 * time.Minute,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	server.AddBundle(e.bundle)
	cfg := transcripts.Config{Moil: server, Bundle: e.bundle, Store: e.db, Objects: e.s3, Now: e.clock.Now}
	for _, configure := range e.configure {
		configure(&cfg)
	}
	service := transcripts.New(cfg)
	recordings := recording.NewHandler(e.db, noEgress{}, noRoomService{}, e.s3, "", nil)
	recordings.SetClock(e.clock.Now)
	recordings.SetTranscripts(service)
	recordings.SetRecordingsChangedHook(service.Nudge)
	roomsHandler := rooms.NewHandler(e.db, e.s3, nil, nil)
	roomsHandler.SetRoomDeletedHook(service.Nudge)
	e.mu.Lock()
	e.moil, e.service, e.recordings, e.rooms = server, service, recordings, roomsHandler
	e.mu.Unlock()
	e.run()
}

// run runs the transcripts service and the recording reconciler, again
// after stopService.
func (e *env) run() {
	ctx, cancel := context.WithCancel(context.Background())
	var done sync.WaitGroup
	done.Add(2)
	go func() {
		defer done.Done()
		e.service.Run(ctx, time.Hour)
	}()
	go func() {
		defer done.Done()
		e.recordings.RunReconciler(ctx, 5*time.Millisecond)
	}()
	e.cancelService, e.waitService = cancel, done.Wait
}

// stopService stops the transcripts service and the recording reconciler.
func (e *env) stopService() {
	e.cancelService()
	e.waitService()
}

// stop stops the service, then closes moil, as klisi does when it exits.
func (e *env) stop() {
	e.stopService()
	_ = e.moil.Close()
}

// noEgress is LiveKit's egress service out of reach: the recording
// reconciler leaves recordings as they are, and gets on with removing files.
type noEgress struct{}

func (noEgress) StartRoomCompositeEgress(context.Context, *protocol.RoomCompositeEgressRequest) (*protocol.EgressInfo, error) {
	return nil, errors.New("no LiveKit in these tests")
}

func (noEgress) StopEgress(context.Context, *protocol.StopEgressRequest) (*protocol.EgressInfo, error) {
	return nil, errors.New("no LiveKit in these tests")
}

func (noEgress) ListEgress(context.Context, *protocol.ListEgressRequest) (*protocol.ListEgressResponse, error) {
	return nil, errors.New("no LiveKit in these tests")
}

// noRoomService is LiveKit's room service for a room that has emptied.
type noRoomService struct{}

func (noRoomService) UpdateRoomMetadata(_ context.Context, request *protocol.UpdateRoomMetadataRequest) (*protocol.Room, error) {
	return &protocol.Room{Name: request.Room, Metadata: request.Metadata}, nil
}

func (e *env) room(owner, name string) store.Room {
	e.t.Helper()
	room := store.Room{
		ID: "room-" + name, Slug: "slug-" + name, Name: name, OwnerSub: owner, LobbyEnabled: true, CreatedAt: 1,
	}
	if err := e.db.CreateRoom(context.Background(), room); err != nil {
		e.t.Fatal(err)
	}
	return room
}

// record makes a 10-minute audio recording of room that ended at ended,
// the way egress ends one: the file lands in storage, then LiveKit's
// egress_ended webhook completes the row.
func (e *env) record(room store.Room, ended time.Time) store.Recording {
	e.t.Helper()
	return e.recordFor(room, room.Slug, 10*time.Minute, ended)
}

// recordFor makes an audio recording of room lasting duration that ended at
// ended, carrying slug as its room's slug.
func (e *env) recordFor(room store.Room, slug string, duration time.Duration, ended time.Time) store.Recording {
	e.t.Helper()
	e.recorded++
	id := fmt.Sprintf("rec%d", e.recorded)
	started := ended.Add(-duration)
	key := path.Join("recordings", slug, id, started.UTC().Format("2006-01-02 15-04")+" - "+room.Name+".ogg")
	ctx := context.Background()
	if err := e.db.InsertRecording(ctx, store.Recording{
		ID: id, RoomID: room.ID, RoomSlug: slug, EgressID: "egress-" + id,
		Status: "recording", StartedBy: room.OwnerSub, StartedAt: started.Unix(), AudioOnly: true,
	}); err != nil {
		e.t.Fatal(err)
	}
	e.s3.Put(key, []byte("OggS audio of "+id))
	err := e.recordings.HandleWebhookEvent(httptest.NewRequest(http.MethodPost, api.LiveKitWebhookPath, nil), &protocol.WebhookEvent{
		Event: "egress_ended",
		EgressInfo: &protocol.EgressInfo{
			EgressId: "egress-" + id, RoomName: slug, Status: protocol.EgressStatus_EGRESS_COMPLETE,
			EndedAt: ended.UnixNano(),
			FileResults: []*protocol.FileInfo{{
				Filename: key, Duration: int64(duration), Size: 21, EndedAt: ended.UnixNano(),
			}},
		},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	recording, err := e.db.RecordingByID(ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	if recording.Status != "completed" || recording.S3Key == nil || *recording.S3Key != key {
		e.t.Fatalf("recording after egress_ended = %+v", recording)
	}
	return recording
}

// reconciled waits until the recording reconciler has run a pass that
// began after now: one that removes a probe file queued for removal now,
// and so considered every removal due by now, and removed only those. It
// takes one Remove call of storage.
func (e *env) reconciled() {
	e.t.Helper()
	e.probes++
	probe := fmt.Sprintf("probes/%d", e.probes)
	e.s3.Put(probe, []byte("probe"))
	if err := e.db.QueueRemovals(context.Background(), []string{probe}, e.clock.Now().Unix()); err != nil {
		e.t.Fatal(err)
	}
	waitFor(e.t, "a recording reconciler pass", func() bool { _, ok := e.s3.Object(probe); return !ok })
}

// queuedStaging is the staging keys queued for removal, whenever they are
// due.
func (e *env) queuedStaging() []string {
	e.t.Helper()
	keys, err := e.db.DueRemovals(context.Background(), e.clock.Now().Add(100*365*24*time.Hour).Unix(), 1000)
	if err != nil {
		e.t.Fatal(err)
	}
	var staged []string
	for _, key := range keys {
		if strings.HasPrefix(key, "transcripts-staging/") {
			staged = append(staged, key)
		}
	}
	return staged
}

// sql changes klisi's database behind its back, over a connection of its
// own, as an operator or another process could: for what klisi has no API
// for, and to make the database fail.
func (e *env) sql(query string, args ...any) {
	e.t.Helper()
	db, err := sql.Open("sqlite", e.path)
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		e.t.Fatal(err)
	}
	if _, err := db.Exec(query, args...); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
}

// pair pairs a machine for owner. It isn't connected, and approved nothing.
func (e *env) pair(owner string) *moiltest.Machine {
	e.t.Helper()
	return moiltest.Pair(e.t, e.moil, owner, moiltest.At(e.front.URL+api.MoilBasePath), moiltest.WithTimeout(waitTimeout))
}

// machine pairs a machine for owner that approved the transcribe bundle and is
// connected, idle.
func (e *env) machine(owner string) *moiltest.Machine {
	e.t.Helper()
	m := e.pair(owner)
	m.Approve(e.bundle)
	m.Connect()
	return m
}

// submitted waits until moil has the recording's job.
func (e *env) submitted(recording store.Recording) {
	e.t.Helper()
	waitFor(e.t, "the job of "+recording.ID+" to be submitted", func() bool {
		run, ok := e.moil.Run("recording-" + recording.ID)
		return ok && run.State() != moil.Cancelled
	})
}

func session(sub string) *auth.Session { return &auth.Session{Sub: sub} }

var admin = &auth.Session{Sub: "root", IsAdmin: true}

// request builds a request as the session, or signed out when it's nil.
func request(method, target string, as *auth.Session) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	if as != nil {
		r = r.WithContext(auth.WithSession(r.Context(), *as))
	}
	return r
}

// list is the room's recordings as the recording list shows them to its
// owner, by ID.
func (e *env) list(room store.Room) map[string]api.RecordingInfo {
	e.t.Helper()
	r := request(http.MethodGet, "/api/rooms/"+room.Slug+"/recordings", session(room.OwnerSub))
	r.SetPathValue("slug", room.Slug)
	response := httptest.NewRecorder()
	e.recordings.List(response, r)
	if response.Code != http.StatusOK {
		e.t.Fatalf("list: %d %s", response.Code, response.Body)
	}
	var infos []api.RecordingInfo
	if err := json.NewDecoder(response.Body).Decode(&infos); err != nil {
		e.t.Fatal(err)
	}
	byID := make(map[string]api.RecordingInfo)
	for _, info := range infos {
		byID[info.ID] = info
	}
	return byID
}

// transcript is the recording's transcript as the list shows it.
func (e *env) transcript(room store.Room, recording store.Recording) *api.TranscriptInfo {
	e.t.Helper()
	info, ok := e.list(room)[recording.ID]
	if !ok {
		e.t.Fatalf("recording %s isn't listed", recording.ID)
	}
	return info.Transcript
}

// waitTranscript waits until the list shows the recording's transcript as
// ok says, and returns it.
func (e *env) waitTranscript(room store.Room, recording store.Recording, what string, ok func(*api.TranscriptInfo) bool) *api.TranscriptInfo {
	e.t.Helper()
	var info *api.TranscriptInfo
	waitFor(e.t, "the transcript of "+recording.ID+" to be "+what, func() bool {
		info = e.transcript(room, recording)
		return ok(info)
	})
	return info
}

func (e *env) waitStatus(room store.Room, recording store.Recording, status string) *api.TranscriptInfo {
	e.t.Helper()
	return e.waitTranscript(room, recording, status, func(info *api.TranscriptInfo) bool {
		return info != nil && info.Status == status
	})
}

// row is the recording's transcripts row, if it has one.
func (e *env) row(recording store.Recording) (store.Transcript, bool) {
	e.t.Helper()
	rows, err := e.db.TranscriptsByRoom(context.Background(), recording.RoomID)
	if err != nil {
		e.t.Fatal(err)
	}
	row, ok := rows[recording.ID]
	return row, ok
}

func (e *env) requestTranscript(recording store.Recording, as *auth.Session) *httptest.ResponseRecorder {
	r := request(http.MethodPost, "/api/recordings/"+recording.ID+"/transcript", as)
	r.SetPathValue("id", recording.ID)
	response := httptest.NewRecorder()
	e.service.Request(response, r)
	return response
}

func (e *env) download(recording store.Recording, as *auth.Session, format string) *httptest.ResponseRecorder {
	r := request(http.MethodGet, "/api/recordings/"+recording.ID+"/transcript/download?format="+format, as)
	r.SetPathValue("id", recording.ID)
	response := httptest.NewRecorder()
	e.service.Download(response, r)
	return response
}

func (e *env) deleteRecording(recording store.Recording, as *auth.Session) *httptest.ResponseRecorder {
	r := request(http.MethodDelete, "/api/recordings/"+recording.ID, as)
	r.SetPathValue("id", recording.ID)
	response := httptest.NewRecorder()
	e.recordings.Delete(response, r)
	return response
}

func (e *env) deleteRoom(room store.Room, as *auth.Session) *httptest.ResponseRecorder {
	r := request(http.MethodDelete, "/api/rooms/"+room.Slug, as)
	r.SetPathValue("slug", room.Slug)
	response := httptest.NewRecorder()
	e.rooms.Delete(response, r)
	return response
}

// sidecars are where the recording's transcript formats are stored.
func sidecars(recording store.Recording) (txt, vtt string) {
	return recording.TranscriptKey(store.TranscriptFormats[0]), recording.TranscriptKey(store.TranscriptFormats[1])
}

// finish plays a successful transcription: the machine uploads both
// sidecars and reports the speakers it found.
func finish(t *testing.T, a *moiltest.Attempt, speakers int) (txt, vtt []byte) {
	t.Helper()
	txt = fmt.Appendf(nil, "[00:00:01] Speaker 1: Hello from %s.\n", a.JobID)
	vtt = fmt.Appendf(nil, "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<v Speaker 1>Hello from %s.\n", a.JobID)
	a.Output("transcript.txt", txt)
	a.Output("transcript.vtt", vtt)
	a.Succeed(map[string]any{"duration": 600.0, "speakers": speakers, "utterances": 1, "words": 3})
	a.WaitAcked()
	return txt, vtt
}

// waitFor waits until cond holds, checking it whenever it may have changed
// for up to waitTimeout.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("waited %v for %s", waitTimeout, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func errorMessage(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var body api.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("error body %q: %v", response.Body, err)
	}
	return body.Error
}

// watchLogs copies what klisi logs, from now to the end of the test, to
// the buffer it returns.
func watchLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	logs := &lockedBuffer{}
	previous := log.Writer()
	log.SetOutput(io.MultiWriter(previous, logs))
	t.Cleanup(func() { log.SetOutput(previous) })
	return logs
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

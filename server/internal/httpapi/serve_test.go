package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"

	"tide/internal/api"
	"tide/internal/auth"
	"tide/internal/config"
	"tide/internal/store"
)

// These tests stop tide as main does on a signal, while it's busy, and hold
// it to the order Serve promises: requests in flight finish, then moil
// closes, then the background work returns, and only then does the database
// close.

func TestStoppingTideFinishesTheRequestsInFlight(t *testing.T) {
	watchForAClosedDatabase(t)
	k := startTide(t, nil)
	alice := k.signIn(auth.Session{Sub: "alice", Name: "Alice"})
	standup := alice.createRoom("Standup")
	m := alice.pair()
	m.Connect()

	// Alice watches her room's lobby, a stream that stays open, while her
	// browser creates another room: tide is reading that request's body
	// when it's told to stop.
	lobby := alice.watchLobby(standup.Slug)
	retro := alice.startRequest(http.MethodPost, api.RoomsPath, `{"name":"Retro"}`)
	k.shutdown()

	// tide stops taking connections at once, and ends the lobby stream
	// rather than wait for it, as it would for all of its grace.
	k.waitUntilRefused()
	select {
	case err := <-lobby:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("the lobby stream ended with %v, want its end", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the lobby stream held tide open")
	}
	// It waits for the request, with the machine still connected.
	if _, stopped := k.stopped(300 * time.Millisecond); stopped {
		t.Fatal("tide stopped with a request in flight")
	}
	m.Sync()
	status, body := retro()
	var created api.RoomInfo
	if err := json.Unmarshal([]byte(body), &created); status != http.StatusCreated || err != nil || created.Name != "Retro" {
		t.Fatalf("the request in flight = %d %s, want the room created", status, body)
	}

	// Then it closes moil, which tells the machine it's going away, and
	// stops. The room the request created is in the database.
	if code := m.WaitClosed(); code != 1001 {
		t.Fatalf("the machine's channel closed with %d, want 1001 (going away)", code)
	}
	if err, stopped := k.stopped(10 * time.Second); !stopped || err != nil {
		t.Fatalf("tide stopped = %v with %v", stopped, err)
	}
	if room, err := k.db.RoomBySlug(context.Background(), created.Slug); err != nil || room.Name != "Retro" {
		t.Fatalf("the room the request created = %+v, %v", room, err)
	}
}

func TestStoppingTideWaitsForItsBackgroundWork(t *testing.T) {
	watchForAClosedDatabase(t)
	egress := startFakeEgress(t)
	db, dbPath := openStore(t)
	ctx := context.Background()
	if err := db.CreateRoom(ctx, store.Room{
		ID: "room-standup", Slug: "standup", Name: "Standup", OwnerSub: "alice", CreatedAt: time.Now().Add(-2 * time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	// A recording whose egress_ended webhook never came.
	if err := db.InsertRecording(ctx, store.Recording{
		ID: "rec-lost", RoomID: "room-standup", RoomSlug: "standup", EgressID: "EG_lost",
		Status: "recording", StartedBy: "alice", StartedAt: time.Now().Add(-time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	k := startTideOn(t, db, dbPath, func(cfg *config.Config, _ *http.Server) {
		cfg.LiveKitURL = egress.url
	})

	// The recording reconciler's first pass asks LiveKit about the egress.
	// Meanwhile the disk turns slow: another process holds SQLite's write
	// lock. LiveKit doesn't know the egress, so the reconciler goes to mark
	// the recording failed, and waits for the disk.
	egress.waitAsked(t)
	release := lockDatabase(t, dbPath)
	egress.answer()
	waitUntilBusy(t, k.db)

	// tide is told to stop. It waits for the reconciler, which is still in
	// the database, however long the disk takes...
	k.shutdown()
	if _, stopped := k.stopped(500 * time.Millisecond); stopped {
		t.Fatal("tide stopped while its reconciler was writing")
	}
	// ...and stops once it's out, before the database closes.
	release()
	if err, stopped := k.stopped(10 * time.Second); !stopped || err != nil {
		t.Fatalf("tide stopped = %v with %v", stopped, err)
	}
}

// An HTTP server something else closed, without tide being told to stop,
// stops tide all the same: Serve closes moil and returns, as it does on a
// signal, rather than wait for the server to stop a second time.
func TestTideStopsWhenItsServerIsClosedElsewhere(t *testing.T) {
	watchForAClosedDatabase(t)
	var server *http.Server
	k := startTide(t, func(_ *config.Config, s *http.Server) { server = s })
	alice := k.signIn(auth.Session{Sub: "alice"})
	m := alice.pair()
	m.Connect()

	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if err, stopped := k.stopped(10 * time.Second); !stopped || err != nil {
		t.Fatalf("tide stopped = %v with %v", stopped, err)
	}
	if code := m.WaitClosed(); code != 1001 {
		t.Fatalf("the machine's channel closed with %d, want 1001 (going away)", code)
	}
}

// watchForAClosedDatabase fails the test if anything logs that it found the
// database closed, up to the end of the test, after tide has stopped and
// the database has closed. Call it before starting tide.
func watchForAClosedDatabase(t *testing.T) {
	t.Helper()
	logs := &lockedBuffer{}
	previous := log.Writer()
	log.SetOutput(io.MultiWriter(previous, logs))
	t.Cleanup(func() {
		log.SetOutput(previous)
		if text := logs.String(); strings.Contains(text, "database is closed") {
			t.Errorf("something used the database after it closed:\n%s", text)
		}
	})
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

func (h *host) createRoom(name string) api.RoomInfo {
	h.k.t.Helper()
	status, _, body := h.k.request(http.MethodPost, api.RoomsPath, fmt.Sprintf(`{"name":%q}`, name), h.cookie, true)
	var room api.RoomInfo
	if err := json.Unmarshal([]byte(body), &room); status != http.StatusCreated || err != nil {
		h.k.t.Fatalf("creating %s = %d %s", name, status, body)
	}
	return room
}

// watchLobby opens the room's lobby stream, as the host's meeting page does,
// and waits for its first event. The channel receives how the stream ended.
func (h *host) watchLobby(slug string) <-chan error {
	h.k.t.Helper()
	request, err := http.NewRequest(http.MethodGet, h.k.url+fill(api.RoomLobbyPath, slug), nil)
	if err != nil {
		h.k.t.Fatal(err)
	}
	request.AddCookie(h.cookie)
	response, err := (&http.Client{Transport: &http.Transport{}}).Do(request)
	if err != nil {
		h.k.t.Fatal(err)
	}
	h.k.t.Cleanup(func() { response.Body.Close() })
	reader := bufio.NewReader(response.Body)
	if line, err := reader.ReadString('\n'); response.StatusCode != http.StatusOK || err != nil || line != "event: pending\n" {
		h.k.t.Fatalf("the lobby stream = %d, first line %q (%v)", response.StatusCode, line, err)
	}
	ended := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, reader)
		if err == nil {
			err = io.EOF
		}
		ended <- err
	}()
	return ended
}

// startRequest sends a request's head as the SPA does, and waits until
// tide's handler starts reading its body, which it holds back: the request
// is in flight until finish sends the body and returns tide's answer.
func (h *host) startRequest(method, path, body string) (finish func() (int, string)) {
	h.k.t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(h.k.url, "http://"))
	if err != nil {
		h.k.t.Fatal(err)
	}
	h.k.t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: tide\r\nCookie: %s=%s\r\nX-Tide-Csrf: 1\r\n"+
		"Content-Type: application/json\r\nContent-Length: %d\r\nExpect: 100-continue\r\n\r\n",
		method, path, h.cookie.Name, h.cookie.Value, len(body))
	reader := bufio.NewReader(conn)
	// The server says to continue when the handler first reads the body.
	head := textproto.NewReader(reader)
	if line, err := head.ReadLine(); err != nil || line != "HTTP/1.1 100 Continue" {
		h.k.t.Fatalf("%s %s: %q (%v), want 100 Continue", method, path, line, err)
	}
	if line, err := head.ReadLine(); err != nil || line != "" {
		h.k.t.Fatalf("%s %s: %q (%v) after 100 Continue", method, path, line, err)
	}
	return func() (int, string) {
		h.k.t.Helper()
		if _, err := io.WriteString(conn, body); err != nil {
			h.k.t.Fatal(err)
		}
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			h.k.t.Fatalf("%s %s: %v", method, path, err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			h.k.t.Fatal(err)
		}
		return response.StatusCode, string(data)
	}
}

// waitUntilRefused waits until tide refuses new connections.
func (k *tideServer) waitUntilRefused() {
	k.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.Dial("tcp", strings.TrimPrefix(k.url, "http://"))
		if err != nil {
			return
		}
		conn.Close()
		if time.Now().After(deadline) {
			k.t.Fatal("tide still takes connections")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A fakeEgress is LiveKit's egress service, as far as the recording
// reconciler asks it: it knows no egress, and it answers each question only
// when the test says so.
type fakeEgress struct {
	livekit.Egress // other calls aren't made
	url            string
	asked          chan struct{}
	answers        chan struct{}
}

func startFakeEgress(t *testing.T) *fakeEgress {
	t.Helper()
	egress := &fakeEgress{asked: make(chan struct{}, 1), answers: make(chan struct{})}
	server := httptest.NewServer(livekit.NewEgressServer(egress))
	t.Cleanup(server.Close)
	egress.url = "ws://" + server.Listener.Addr().String()
	return egress
}

func (e *fakeEgress) ListEgress(ctx context.Context, _ *livekit.ListEgressRequest) (*livekit.ListEgressResponse, error) {
	select {
	case e.asked <- struct{}{}:
	default:
	}
	select {
	case <-e.answers:
		return &livekit.ListEgressResponse{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *fakeEgress) waitAsked(t *testing.T) {
	t.Helper()
	select {
	case <-e.asked:
	case <-time.After(10 * time.Second):
		t.Fatal("the recording reconciler never asked LiveKit about the recording")
	}
}

func (e *fakeEgress) answer() { e.answers <- struct{}{} }

// lockDatabase takes SQLite's write lock from another connection, as another
// process would, until release, or the end of the test.
func lockDatabase(t *testing.T, path string) (release func()) {
	t.Helper()
	ctx := context.Background()
	locker, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := locker.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"PRAGMA busy_timeout=5000", "BEGIN IMMEDIATE"} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	release = sync.OnceFunc(func() {
		if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
			t.Errorf("releasing the database: %v", err)
		}
		conn.Close()
		locker.Close()
	})
	t.Cleanup(release)
	return release
}

// waitUntilBusy waits until tide's one database connection is taken: a
// query can't have it within 20 milliseconds.
func waitUntilBusy(t *testing.T, db *store.Store) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := db.RoomBySlug(ctx, "standup")
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("tide's database connection stayed free (%v)", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

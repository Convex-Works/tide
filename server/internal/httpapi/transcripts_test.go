package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moiltest"

	"tide/internal/api"
	"tide/internal/auth"
	"tide/internal/config"
	"tide/internal/store"
)

// Transcripts follow recording (ARCHITECTURE.md §8.1): without it, tide has
// no machine, pairing, transcript or moil routes; with it, they are all
// there, and deleting something removes every file of it.

func recordingOff(cfg *config.Config, _ *http.Server) { cfg.Recording = false }

// transcriptRoutes are a request to every route transcripts add, as the
// SPA or a machine would send it.
func transcriptRoutes() []struct{ method, path, body string } {
	return []struct{ method, path, body string }{
		{http.MethodGet, api.MachinesPath, ""},
		{http.MethodDelete, fill(api.MachinePath, "m-1"), ""},
		{http.MethodGet, fill(api.PairingPath, "WDJB-MJHT"), ""},
		{http.MethodPost, fill(api.PairingConfirmPath, "WDJB-MJHT"), ""},
		{http.MethodPost, fill(api.PairingDenyPath, "WDJB-MJHT"), ""},
		{http.MethodPost, fill(api.RecordingTranscriptPath, "rec-1"), ""},
		{http.MethodGet, fill(api.RecordingTranscriptDownloadPath, "rec-1") + "?format=txt", ""},
		{http.MethodGet, api.MoilBasePath + "/v1/info", ""},
		{http.MethodPost, api.MoilBasePath + "/v1/pair", `{"name":"Laptop"}`},
		{http.MethodGet, api.MoilBasePath + "/v1/connect", ""},
		// A method a route doesn't take: with the route there, 405.
		{http.MethodPut, api.MachinesPath, ""},
		{http.MethodGet, fill(api.PairingConfirmPath, "WDJB-MJHT"), ""},
	}
}

// send makes a request as the SPA does, fails the test unless tide answers
// want, and decodes the answer into out if set.
func (h *host) send(method, path string, want int, out any) {
	h.k.t.Helper()
	status, _, body := h.k.request(method, path, "", h.cookie, true)
	if status != want {
		h.k.t.Fatalf("%s %s as %s = %d %s, want %d", method, path, h.sub, status, body, want)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(body), out); err != nil {
			h.k.t.Fatalf("%s %s = %s: %v", method, path, body, err)
		}
	}
}

func (h *host) me() api.Me {
	h.k.t.Helper()
	var me api.Me
	h.send(http.MethodGet, api.MePath, http.StatusOK, &me)
	return me
}

func TestWithoutRecordingTranscriptRoutesAnswer404(t *testing.T) {
	k := startTide(t, recordingOff)
	alice := k.signIn(auth.Session{Sub: "alice", Name: "Alice", Email: "alice@example.com"})

	if me := alice.me(); me.Recording || me.Sub != "alice" || me.Email != "alice@example.com" {
		t.Fatalf("/api/me = %+v, want alice without recording", me)
	}
	for _, route := range transcriptRoutes() {
		status, _, body := k.request(route.method, route.path, route.body, alice.cookie, true)
		if status != http.StatusNotFound {
			t.Errorf("%s %s = %d %s, want 404", route.method, route.path, status, body)
		}
	}
	// The rest of tide is there.
	if room := alice.createRoom("Standup"); room.Name != "Standup" {
		t.Fatalf("created %+v", room)
	}
}

func TestWithRecordingTranscriptRoutesAreServed(t *testing.T) {
	k := startTide(t, nil)
	alice := k.signIn(auth.Session{Sub: "alice", Name: "Alice"})

	if me := alice.me(); !me.Recording {
		t.Fatalf("/api/me = %+v, want recording", me)
	}
	// Every route is there: none answers 404 for want of a route. Those
	// naming something that doesn't exist answer 404 in JSON, from their
	// handler, rather than the API's catch-all.
	for _, route := range transcriptRoutes() {
		status, _, body := k.request(route.method, route.path, route.body, alice.cookie, true)
		if status == http.StatusNotFound && strings.Contains(body, "API route not found") {
			t.Errorf("%s %s = %d %s, want the route", route.method, route.path, status, body)
		}
	}
	alice.machines() // 200
	status, _, body := k.request(http.MethodGet, api.MoilBasePath+"/v1/info", "", nil, false)
	if status != http.StatusOK {
		t.Fatalf("GET /moil/v1/info = %d %s", status, body)
	}
}

// The SPA answers every path it doesn't serve a file for with its index,
// but not the moil paths: a moil app pairing with a tide without recording
// gets 404, not a web page.
func TestWithoutRecordingMoilPathsAreNotTheSPA(t *testing.T) {
	db, _ := openStore(t)
	web := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>tide</title>")}}
	handler, background, err := New(config.Config{
		BaseURL: "http://localhost:8080", SessionSecret: "test-session-secret",
		LiveKitURL: "ws://livekit.example", LiveKitAPIKey: "devkey",
		LiveKitAPISecret: "test-livekit-secret-with-enough-bytes",
	}, web, db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := background.Close(); err != nil {
		t.Fatalf("closing tide without recording: %v", err)
	}
	for path, want := range map[string]int{
		"/machines":                      http.StatusOK, // the SPA shows "not enabled"
		"/moil":                          http.StatusNotFound,
		api.MoilBasePath + "/v1/info":    http.StatusNotFound,
		api.MoilBasePath + "/v1/connect": http.StatusNotFound,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != want || (want == http.StatusNotFound && strings.Contains(response.Body.String(), "doctype")) {
			t.Errorf("GET %s = %d %q, want %d", path, response.Code, response.Body.String(), want)
		}
	}
}

// fakeStorage is S3 as far as removing objects goes: it records each key
// tide deletes.
type fakeStorage struct {
	mu      sync.Mutex
	removed []string
}

func startFakeStorage(t *testing.T) (*fakeStorage, string) {
	t.Helper()
	storage := &fakeStorage{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		storage.mu.Lock()
		storage.removed = append(storage.removed, strings.TrimPrefix(r.URL.Path, "/tide-recordings/"))
		storage.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return storage, server.URL
}

func (s *fakeStorage) removedKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.removed)
}

func withStorage(endpoint string) func(*config.Config, *http.Server) {
	return func(cfg *config.Config, _ *http.Server) {
		cfg.S3Endpoint, cfg.S3PublicEndpoint = endpoint, endpoint
		cfg.S3Bucket, cfg.S3Region = "tide-recordings", "us-east-1"
		cfg.S3AccessKey, cfg.S3SecretKey = "tide", "tide-test-secret"
	}
}

// addRecording adds a completed recording of room to the database, with a
// transcript row.
func addRecording(t *testing.T, db *store.Store, room api.RoomInfo, id string) store.Recording {
	t.Helper()
	ctx := context.Background()
	ended, duration, size := time.Now().Add(-time.Hour).Unix(), int64(600), int64(1000)
	key := "recordings/" + room.Slug + "/" + id + "/2026-09-27 14-00 - Standup.ogg"
	recording := store.Recording{
		ID: id, RoomID: room.ID, RoomSlug: room.Slug, EgressID: "EG_" + id, Status: "completed",
		StartedBy: "alice", StartedAt: ended - duration, AudioOnly: true,
		EndedAt: &ended, DurationS: &duration, S3Key: &key, SizeBytes: &size,
	}
	if err := db.InsertRecording(ctx, recording); err != nil {
		t.Fatal(err)
	}
	if requested, err := db.RequestTranscript(ctx, id, ended); err != nil || !requested {
		t.Fatalf("requesting a transcript of %s = %v, %v", id, requested, err)
	}
	return recording
}

func wantRemoved(t *testing.T, storage *fakeStorage, db *store.Store, keys []string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		removed := storage.removedKeys()
		missing := slices.DeleteFunc(slices.Clone(keys), func(key string) bool { return slices.Contains(removed, key) })
		if len(missing) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("storage never removed %q; it removed %q", missing, removed)
		}
		time.Sleep(10 * time.Millisecond)
	}
	queued, err := db.DueRemovals(context.Background(), time.Now().Add(24*time.Hour).Unix(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Fatalf("still queued for removal: %q", queued)
	}
}

func TestDeletingARecordingRemovesItsTranscriptFiles(t *testing.T) {
	storage, endpoint := startFakeStorage(t)
	k := startTide(t, withStorage(endpoint))
	alice := k.signIn(auth.Session{Sub: "alice", Name: "Alice"})
	room := alice.createRoom("Standup")
	recording := addRecording(t, k.db, room, "rec-1")

	alice.send(http.MethodDelete, fill(api.RecordingPath, recording.ID), http.StatusNoContent, nil)
	keys := recording.ObjectKeys()
	if len(keys) != 4 || !strings.HasSuffix(keys[1], ".txt") || !strings.HasSuffix(keys[2], ".vtt") ||
		keys[3] != path.Join(path.Dir(keys[0]), "speakers.json") {
		t.Fatalf("ObjectKeys = %q, want the recording, both transcript files and the speaker timeline", keys)
	}
	wantRemoved(t, storage, k.db, keys)
	if _, err := k.db.Transcript(context.Background(), recording.ID); err == nil {
		t.Fatal("the transcript row outlived its recording")
	}
}

func TestDeletingARoomRemovesItsTranscriptFiles(t *testing.T) {
	storage, endpoint := startFakeStorage(t)
	k := startTide(t, withStorage(endpoint))
	alice := k.signIn(auth.Session{Sub: "alice", Name: "Alice"})
	room := alice.createRoom("Standup")
	first, second := addRecording(t, k.db, room, "rec-1"), addRecording(t, k.db, room, "rec-2")

	alice.send(http.MethodDelete, fill(api.RoomPath, room.Slug), http.StatusNoContent, nil)
	wantRemoved(t, storage, k.db, append(first.ObjectKeys(), second.ObjectKeys()...))
}

// The recording reconciler owns every queued removal, so a staging key
// Prepare queued is removed when it comes due, even after a restart.
func TestQueuedStagingKeysAreRemoved(t *testing.T) {
	storage, endpoint := startFakeStorage(t)
	db, dbPath := openStore(t)
	staged := "transcripts-staging/rec-1/1-0123456789abcdef/transcript.txt"
	if err := db.QueueRemovals(context.Background(), []string{staged}, time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	startTideOn(t, db, dbPath, withStorage(endpoint))
	wantRemoved(t, storage, db, []string{staged})
}

// Recording going away takes nothing with it: rows stay and paired machines
// stay paired, and they are all there when recording comes back.
func TestRecordingOffKeepsMachinesAndTranscripts(t *testing.T) {
	ctx := context.Background()
	db, dbPath := openStore(t)
	session := auth.Session{Sub: "alice", Name: "Alice"}

	first := startTideOn(t, db, dbPath, nil)
	alice := first.signIn(session)
	room := alice.createRoom("Standup")
	paired := alice.pair(moiltest.WithName("Studio"))
	addRecording(t, db, room, "rec-1")
	if err := first.stop(); err != nil {
		t.Fatal(err)
	}

	off := startTideOn(t, db, dbPath, recordingOff)
	if status, _, body := off.request(http.MethodGet, api.MachinesPath, "", off.signIn(session).cookie, false); status != http.StatusNotFound {
		t.Fatalf("machines with recording off = %d %s", status, body)
	}
	if _, err := db.Transcript(ctx, "rec-1"); err != nil {
		t.Fatalf("the transcript row with recording off: %v", err)
	}
	if err := off.stop(); err != nil {
		t.Fatal(err)
	}

	back := startTideOn(t, db, dbPath, nil)
	if machines := back.signIn(session).machines().Machines; len(machines) != 1 || machines[0].ID != paired.ID() || machines[0].Name != "Studio" {
		t.Fatalf("machines with recording back = %+v, want Studio", machines)
	}
	if _, err := db.Transcript(ctx, "rec-1"); err != nil {
		t.Fatalf("the transcript row with recording back: %v", err)
	}
}

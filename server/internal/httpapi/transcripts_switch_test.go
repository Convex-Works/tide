package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/config"
	"klisi/internal/store"
)

// KLISI_TRANSCRIPTS (ARCHITECTURE.md §8.1): with it off, klisi has no
// machine, pairing, transcript or moil routes, says so on /api/me, and
// still removes every file of what is deleted.

func transcriptsOff(cfg *config.Config, _ *http.Server) { cfg.Transcripts = false }

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

// send makes a request as the SPA does, fails the test unless klisi answers
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

func TestTranscriptsOffAnswer404ForTheirRoutes(t *testing.T) {
	k := startKlisi(t, transcriptsOff)
	alice := k.signIn(auth.Session{Sub: "alice", Name: "Alice", Email: "alice@example.com"})

	if me := alice.me(); me.Transcripts || me.Sub != "alice" || me.Email != "alice@example.com" {
		t.Fatalf("/api/me = %+v, want alice without transcripts", me)
	}
	for _, route := range transcriptRoutes() {
		status, _, body := k.request(route.method, route.path, route.body, alice.cookie, true)
		if status != http.StatusNotFound {
			t.Errorf("%s %s = %d %s, want 404", route.method, route.path, status, body)
		}
	}
	// The rest of klisi is there.
	if room := alice.createRoom("Standup"); room.Name != "Standup" {
		t.Fatalf("created %+v", room)
	}
}

func TestTranscriptsOnServeTheirRoutes(t *testing.T) {
	k := startKlisi(t, nil)
	alice := k.signIn(auth.Session{Sub: "alice", Name: "Alice"})

	if me := alice.me(); !me.Transcripts {
		t.Fatalf("/api/me = %+v, want transcripts", me)
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
// but not the moil paths: a moil app pairing with a klisi without
// transcripts gets 404, not a web page.
func TestTranscriptsOffMoilPathsAreNotTheSPA(t *testing.T) {
	db, _ := openStore(t)
	web := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>klisi</title>")}}
	handler, background, err := New(config.Config{
		BaseURL: "http://localhost:8080", SessionSecret: "test-session-secret",
		LiveKitURL: "ws://livekit.example", LiveKitAPIKey: "devkey",
		LiveKitAPISecret: "test-livekit-secret-with-enough-bytes",
	}, web, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := background.Close(); err != nil {
		t.Fatalf("closing klisi without transcripts: %v", err)
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
// klisi deletes.
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
		storage.removed = append(storage.removed, strings.TrimPrefix(r.URL.Path, "/klisi-recordings/"))
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
		cfg.Transcripts = false
		cfg.S3Endpoint, cfg.S3PublicEndpoint = endpoint, endpoint
		cfg.S3Bucket, cfg.S3Region = "klisi-recordings", "us-east-1"
		cfg.S3AccessKey, cfg.S3SecretKey = "klisi", "klisi-test-secret"
	}
}

// addRecording adds a completed recording of room to the database, with a
// transcript row, as if transcripts had been on when it was made.
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

func TestTranscriptsOffDeletingARecordingRemovesItsTranscriptFiles(t *testing.T) {
	storage, endpoint := startFakeStorage(t)
	k := startKlisi(t, withStorage(endpoint))
	alice := k.signIn(auth.Session{Sub: "alice", Name: "Alice"})
	room := alice.createRoom("Standup")
	recording := addRecording(t, k.db, room, "rec-1")

	// The list says nothing about its transcript...
	var listed []api.RecordingInfo
	alice.send(http.MethodGet, fill(api.RoomRecordingsPath, room.Slug), http.StatusOK, &listed)
	if len(listed) != 1 || listed[0].Transcript != nil {
		t.Fatalf("recordings = %+v, want one without a transcript", listed)
	}
	// ...but deleting the recording removes its transcript files with it.
	alice.send(http.MethodDelete, fill(api.RecordingPath, recording.ID), http.StatusNoContent, nil)
	keys := recording.ObjectKeys()
	if len(keys) != 3 || !strings.HasSuffix(keys[1], ".txt") || !strings.HasSuffix(keys[2], ".vtt") {
		t.Fatalf("ObjectKeys = %q, want the recording and both transcript files", keys)
	}
	wantRemoved(t, storage, k.db, keys)
	if _, err := k.db.Transcript(context.Background(), recording.ID); err == nil {
		t.Fatal("the transcript row outlived its recording")
	}
}

func TestTranscriptsOffDeletingARoomRemovesItsTranscriptFiles(t *testing.T) {
	storage, endpoint := startFakeStorage(t)
	k := startKlisi(t, withStorage(endpoint))
	alice := k.signIn(auth.Session{Sub: "alice", Name: "Alice"})
	room := alice.createRoom("Standup")
	first, second := addRecording(t, k.db, room, "rec-1"), addRecording(t, k.db, room, "rec-2")

	alice.send(http.MethodDelete, fill(api.RoomPath, room.Slug), http.StatusNoContent, nil)
	wantRemoved(t, storage, k.db, append(first.ObjectKeys(), second.ObjectKeys()...))
}

// A staging key Prepare queued while transcripts were on is removed when
// it comes due with them off: the recording reconciler, which always runs,
// owns every queued removal.
func TestTranscriptsOffStagingKeysAreStillRemoved(t *testing.T) {
	storage, endpoint := startFakeStorage(t)
	db, dbPath := openStore(t)
	staged := "transcripts-staging/rec-1/1-0123456789abcdef/transcript.txt"
	if err := db.QueueRemovals(context.Background(), []string{staged}, time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	startKlisiOn(t, db, dbPath, withStorage(endpoint))
	wantRemoved(t, storage, db, []string{staged})
}

// /api/me is the SPA's one source for whether to show transcripts.
func TestMeReportsTheTranscriptsSwitch(t *testing.T) {
	for _, on := range []bool{false, true} {
		k := startKlisi(t, func(cfg *config.Config, _ *http.Server) { cfg.Transcripts = on })
		status, _, body := k.request(http.MethodGet, api.MePath, "", k.signIn(auth.Session{Sub: "alice"}).cookie, false)
		var me map[string]any
		if err := json.Unmarshal([]byte(body), &me); status != http.StatusOK || err != nil {
			t.Fatalf("/api/me = %d %s", status, body)
		}
		if me["transcripts"] != on {
			t.Fatalf("with KLISI_TRANSCRIPTS=%v, /api/me = %s", on, body)
		}
	}
}

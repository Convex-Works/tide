package rooms

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/recording"
	"klisi/internal/store"
)

type fakeLiveSource struct {
	rooms map[string]LiveRoom
	err   error
}

func (f *fakeLiveSource) ActiveRooms(context.Context) (map[string]LiveRoom, error) {
	return f.rooms, f.err
}

type fakeObjectStore struct {
	mu        sync.Mutex
	removed   []string
	removeErr error
	tries     int // calls of Remove, whether they removed or not
}

func (f *fakeObjectStore) Remove(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tries++
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, key)
	return nil
}

// PresignedGet makes the fake the recording handler's object store too.
func (f *fakeObjectStore) PresignedGet(context.Context, string, time.Duration) (string, error) {
	return "", errors.New("no downloads in these tests")
}

func (f *fakeObjectStore) setRemoveErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeErr = err
}

func (f *fakeObjectStore) removeTries() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tries
}

func (f *fakeObjectStore) removedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.removed)
}

func deleteTestHandler(t *testing.T) (*Handler, *fakeObjectStore, *store.Store, store.Room) {
	t.Helper()
	db, err := store.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	room := store.Room{
		ID: "room-1", Slug: "calm-otter-412", Name: "Weekly",
		OwnerSub: "owner", LobbyEnabled: true, CreatedAt: 1,
	}
	if err := db.CreateRoom(context.Background(), room); err != nil {
		t.Fatal(err)
	}
	objects := &fakeObjectStore{}
	return NewHandler(db, objects, nil, nil), objects, db, room
}

func TestListEnrichesLiveState(t *testing.T) {
	db, err := store.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	const owner = "list-owner"
	if err := db.CreateRoom(ctx, store.Room{ID: "r-live", Slug: "live-one", Name: "Live", OwnerSub: owner, CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRoom(ctx, store.Room{ID: "r-idle", Slug: "idle-one", Name: "Idle", OwnerSub: owner, CreatedAt: 2}); err != nil {
		t.Fatal(err)
	}
	if err := db.TouchRoomActive(ctx, "idle-one", 1000); err != nil {
		t.Fatal(err)
	}

	live := &fakeLiveSource{rooms: map[string]LiveRoom{"live-one": {NumParticipants: 2, Recording: true}}}
	handler := NewHandler(db, &fakeObjectStore{}, live, nil)

	request := httptest.NewRequest(http.MethodGet, "/api/rooms", nil)
	request = request.WithContext(auth.WithSession(request.Context(), auth.Session{Sub: owner}))
	recorder := httptest.NewRecorder()
	handler.List(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var listed []api.RoomInfo
	if err := json.Unmarshal(recorder.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	bySlug := make(map[string]api.RoomInfo, len(listed))
	for _, room := range listed {
		bySlug[room.Slug] = room
	}
	if got := bySlug["live-one"]; !got.Active || got.NumParticipants != 2 || !got.Recording {
		t.Errorf("live-one not enriched: %+v", got)
	}
	if got := bySlug["idle-one"]; got.Active || got.LastActiveAt == nil || *got.LastActiveAt != 1000 {
		t.Errorf("idle-one wrong: %+v", got)
	}
}

func TestListAdminSeesRoomsFromEveryOwner(t *testing.T) {
	db, err := store.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	for _, room := range []store.Room{
		{ID: "room-a", Slug: "room-a", Name: "A", OwnerSub: "owner-a", CreatedAt: 1},
		{ID: "room-b", Slug: "room-b", Name: "B", OwnerSub: "owner-b", CreatedAt: 2},
	} {
		if err := db.CreateRoom(ctx, room); err != nil {
			t.Fatal(err)
		}
	}
	handler := NewHandler(db, &fakeObjectStore{}, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/rooms", nil)
	request = request.WithContext(auth.WithSession(request.Context(), auth.Session{
		Sub: "admin", IsAdmin: true,
	}))
	response := httptest.NewRecorder()

	handler.List(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var listed []api.RoomInfo
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed rooms = %#v", listed)
	}
}

func updateRequest(slug, sessionSub string, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPatch, "/api/rooms/"+slug, bytes.NewBufferString(body))
	request.SetPathValue("slug", slug)
	return request.WithContext(auth.WithSession(request.Context(), auth.Session{Sub: sessionSub}))
}

func TestUpdateChangesSlugAndKeepsRecordingsAddressable(t *testing.T) {
	handler, _, db, room := deleteTestHandler(t)
	if err := db.InsertRecording(context.Background(), store.Recording{
		ID: "rec-rename", RoomID: room.ID, RoomSlug: room.Slug, EgressID: "egress-rename",
		Status: "completed", StartedBy: "owner", StartedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.Update(response, updateRequest(room.Slug, "owner", `{"slug":"Team-Weekly"}`))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	updated, err := db.RoomBySlug(context.Background(), "team-weekly")
	if err != nil || updated.ID != room.ID {
		t.Fatalf("updated room = %#v, %v", updated, err)
	}
	if _, err := db.RoomBySlug(context.Background(), room.Slug); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old slug should be unavailable, err = %v", err)
	}
	recordings, err := db.RecordingsByRoomSlug(context.Background(), "team-weekly")
	if err != nil || len(recordings) != 1 || recordings[0].RoomSlug != "team-weekly" {
		t.Fatalf("renamed recordings = %#v, %v", recordings, err)
	}
}

func TestAdminCanUpdateRoomOwnedBySomeoneElse(t *testing.T) {
	handler, _, db, room := deleteTestHandler(t)
	request := updateRequest(room.Slug, "admin", `{"name":"Admin renamed"}`)
	request = request.WithContext(auth.WithSession(request.Context(), auth.Session{
		Sub: "admin", IsAdmin: true,
	}))
	response := httptest.NewRecorder()

	handler.Update(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	updated, err := db.RoomBySlug(context.Background(), room.Slug)
	if err != nil || updated.Name != "Admin renamed" {
		t.Fatalf("updated room = %#v, %v", updated, err)
	}
}

// Names are counted in characters, not bytes.
func TestUpdateCountsNameInCharacters(t *testing.T) {
	for _, test := range []struct {
		characters int
		code       int
	}{
		{characters: 60, code: http.StatusOK},
		{characters: 101, code: http.StatusBadRequest},
	} {
		t.Run(strconv.Itoa(test.characters), func(t *testing.T) {
			name := strings.Repeat("λ", test.characters)
			handler, _, db, room := deleteTestHandler(t)
			response := httptest.NewRecorder()
			handler.Update(response, updateRequest(room.Slug, "owner", `{"name":"`+name+`"}`))
			if response.Code != test.code {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			stored, err := db.RoomBySlug(context.Background(), room.Slug)
			if err != nil {
				t.Fatal(err)
			}
			if renamed := stored.Name == name; renamed != (test.code == http.StatusOK) {
				t.Fatalf("stored name = %q", stored.Name)
			}
		})
	}
}

func TestPublicReportsAdminManagementCapability(t *testing.T) {
	handler, _, _, room := deleteTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/api/rooms/"+room.Slug, nil)
	request.SetPathValue("slug", room.Slug)
	request = request.WithContext(auth.WithSession(request.Context(), auth.Session{
		Sub: "admin", IsAdmin: true,
	}))
	response := httptest.NewRecorder()

	handler.Public(response, request)

	var info api.PublicRoomInfo
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !info.CanManage {
		t.Fatalf("status/info = %d/%#v", response.Code, info)
	}
}

func TestUpdateRejectsInvalidOrUnavailableSlug(t *testing.T) {
	tests := []struct {
		name string
		live *fakeLiveSource
		body string
		code int
	}{
		{name: "invalid", body: `{"slug":"not valid!"}`, code: http.StatusBadRequest},
		{
			name: "active",
			live: &fakeLiveSource{rooms: map[string]LiveRoom{
				"calm-otter-412": {NumParticipants: 1},
			}},
			body: `{"slug":"new-link"}`,
			code: http.StatusConflict,
		},
		{
			name: "live state unavailable",
			live: &fakeLiveSource{err: errors.New("livekit unavailable")},
			body: `{"slug":"new-link"}`,
			code: http.StatusServiceUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, db, room := deleteTestHandler(t)
			handler := NewHandler(db, &fakeObjectStore{}, test.live, nil)
			response := httptest.NewRecorder()
			handler.Update(response, updateRequest(room.Slug, "owner", test.body))
			if response.Code != test.code {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if _, err := db.RoomBySlug(context.Background(), room.Slug); err != nil {
				t.Fatalf("original room must remain: %v", err)
			}
		})
	}
}

func TestUpdateRejectsDuplicateSlug(t *testing.T) {
	handler, _, db, room := deleteTestHandler(t)
	if err := db.CreateRoom(context.Background(), store.Room{
		ID: "room-2", Slug: "taken-link", Name: "Taken", OwnerSub: "owner", CreatedAt: 2,
	}); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.Update(response, updateRequest(room.Slug, "owner", `{"slug":"taken-link"}`))

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func deleteRequest(slug, sessionSub string) *http.Request {
	request := httptest.NewRequest(http.MethodDelete, "/api/rooms/"+slug, nil)
	request.SetPathValue("slug", slug)
	return request.WithContext(auth.WithSession(request.Context(), auth.Session{Sub: sessionSub}))
}

func TestDeleteRefusesWhileRecordingActive(t *testing.T) {
	handler, _, db, room := deleteTestHandler(t)
	if err := db.InsertRecording(context.Background(), store.Recording{
		ID: "rec-1", RoomID: room.ID, RoomSlug: room.Slug, EgressID: "egress-1",
		Status: "recording", StartedBy: "owner", StartedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.Delete(response, deleteRequest(room.Slug, "owner"))

	if response.Code != http.StatusConflict {
		t.Fatalf("expected 409 while recording is active, got %d", response.Code)
	}
	if _, err := db.RoomBySlug(context.Background(), room.Slug); err != nil {
		t.Fatal("room must survive a refused delete")
	}
}

func TestDeleteRemovesFilesAndCascadesRows(t *testing.T) {
	handler, objects, db, room := deleteTestHandler(t)
	key := "recordings/calm-otter-412/100.mp4"
	if err := db.InsertRecording(context.Background(), store.Recording{
		ID: "rec-1", RoomID: room.ID, RoomSlug: room.Slug, EgressID: "egress-1",
		Status: "completed", StartedBy: "owner", StartedAt: 100, S3Key: &key,
	}); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.Delete(response, deleteRequest(room.Slug, "owner"))

	if response.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", response.Code)
	}
	// The recording goes with its transcript sidecars (ARCHITECTURE.md §8.1).
	want := []string{key, "recordings/calm-otter-412/100.txt", "recordings/calm-otter-412/100.vtt"}
	if removed := objects.removedKeys(); !slices.Equal(removed, want) {
		t.Fatalf("removed objects = %#v, want %#v", removed, want)
	}
	if _, err := db.RoomBySlug(context.Background(), room.Slug); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("room should be gone, err = %v", err)
	}
	// The FK cascade (foreign_keys=ON) must remove the recording row.
	if _, err := db.RecordingByID(context.Background(), "rec-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("recording row should cascade with the room, err = %v", err)
	}
}

// Deleting a room succeeds while storage is down: the files of its
// recordings stay queued, and the recording reconciler removes them once
// storage is back.
func TestDeleteSucceedsWhileStorageIsDown(t *testing.T) {
	handler, objects, db, room := deleteTestHandler(t)
	objects.setRemoveErr(errors.New("s3 unavailable"))
	key := "recordings/calm-otter-412/100.mp4"
	if err := db.InsertRecording(context.Background(), store.Recording{
		ID: "rec-1", RoomID: room.ID, RoomSlug: room.Slug, EgressID: "egress-1",
		Status: "completed", StartedBy: "owner", StartedAt: 100, S3Key: &key,
	}); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.Delete(response, deleteRequest(room.Slug, "owner"))

	if response.Code != http.StatusNoContent {
		t.Fatalf("expected 204 while storage is down, got %d %s", response.Code, response.Body)
	}
	if _, err := db.RoomBySlug(context.Background(), room.Slug); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("room should be gone, err = %v", err)
	}
	if _, err := db.RecordingByID(context.Background(), "rec-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("recording row should be gone, err = %v", err)
	}
	files := []string{key, "recordings/calm-otter-412/100.txt", "recordings/calm-otter-412/100.vtt"}
	if queued, err := db.DueRemovals(context.Background(), time.Now().Unix(), 10); err != nil || !slices.Equal(queued, files) {
		t.Fatalf("queued for removal = %q, %v; want %q", queued, err, files)
	}

	// The recording reconciler retries every pass until storage takes them:
	// two passes later, it has tried each file twice more, and storage
	// still has them all.
	reconciler := recording.NewHandler(db, nil, nil, objects, "", nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	tried := objects.removeTries()
	go func() {
		defer close(done)
		reconciler.RunReconciler(ctx, 5*time.Millisecond)
	}()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(10 * time.Second)
	for objects.removeTries() < tried+2*len(files) {
		if time.Now().After(deadline) {
			t.Fatalf("the reconciler tried %d removals in 10 s, want %d", objects.removeTries()-tried, 2*len(files))
		}
		time.Sleep(time.Millisecond)
	}
	if removed := objects.removedKeys(); len(removed) != 0 {
		t.Fatalf("removed while storage is down: %q", removed)
	}
	if queued, err := db.DueRemovals(context.Background(), time.Now().Unix(), 10); err != nil || !slices.Equal(queued, files) {
		t.Fatalf("queued while storage is down = %q, %v; want %q", queued, err, files)
	}
	objects.setRemoveErr(nil)
	for {
		removed := objects.removedKeys()
		slices.Sort(removed)
		if slices.Equal(removed, files) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("removed %q, want %q", removed, files)
		}
		time.Sleep(5 * time.Millisecond)
	}
	queued, err := db.DueRemovals(context.Background(), time.Now().Unix(), 10)
	for len(queued) != 0 && err == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		queued, err = db.DueRemovals(context.Background(), time.Now().Unix(), 10)
	}
	if err != nil || len(queued) != 0 {
		t.Fatalf("still queued: %q, %v", queued, err)
	}
}

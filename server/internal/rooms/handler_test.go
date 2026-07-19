package rooms

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"klisi/internal/auth"
	"klisi/internal/store"
)

type fakeObjectStore struct {
	removed   []string
	removeErr error
}

func (f *fakeObjectStore) Remove(_ context.Context, key string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, key)
	return nil
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
	return NewHandler(db, objects), objects, db, room
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
	if len(objects.removed) != 1 || objects.removed[0] != key {
		t.Fatalf("removed objects = %#v", objects.removed)
	}
	if _, err := db.RoomBySlug(context.Background(), room.Slug); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("room should be gone, err = %v", err)
	}
	// The FK cascade (foreign_keys=ON) must remove the recording row.
	if _, err := db.RecordingByID(context.Background(), "rec-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("recording row should cascade with the room, err = %v", err)
	}
}

func TestDeleteAbortsWhenFileRemovalFails(t *testing.T) {
	handler, objects, db, room := deleteTestHandler(t)
	objects.removeErr = errors.New("s3 unavailable")
	key := "recordings/calm-otter-412/100.mp4"
	if err := db.InsertRecording(context.Background(), store.Recording{
		ID: "rec-1", RoomID: room.ID, RoomSlug: room.Slug, EgressID: "egress-1",
		Status: "completed", StartedBy: "owner", StartedAt: 100, S3Key: &key,
	}); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.Delete(response, deleteRequest(room.Slug, "owner"))

	if response.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when file removal fails, got %d", response.Code)
	}
	if _, err := db.RoomBySlug(context.Background(), room.Slug); err != nil {
		t.Fatal("room must survive an aborted delete")
	}
	if _, err := db.RecordingByID(context.Background(), "rec-1"); err != nil {
		t.Fatal("recording row must survive an aborted delete")
	}
}

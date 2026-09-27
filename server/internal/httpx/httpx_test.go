package httpx_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"klisi/internal/auth"
	"klisi/internal/httpx"
	"klisi/internal/store"
)

type roomLoaderFunc func(context.Context, string) (store.Room, error)

func (f roomLoaderFunc) RoomBySlug(ctx context.Context, slug string) (store.Room, error) {
	return f(ctx, slug)
}

func TestWriteError(t *testing.T) {
	recorder := httptest.NewRecorder()
	httpx.WriteError(recorder, http.StatusForbidden, "Forbidden.")

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Body.String(); got != "{\"error\":\"Forbidden.\"}\n" {
		t.Fatalf("body = %q", got)
	}
}

func TestDecodeJSON(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "one value", body: `{"name":"Room"}`},
		{name: "unknown field", body: `{"name":"Room","extra":true}`, wantErr: `json: unknown field "extra"`},
		{name: "trailing value", body: `{"name":"Room"} {}`, wantErr: "request body must contain one JSON value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			var target struct {
				Name string `json:"name"`
			}
			err := httpx.DecodeJSON(recorder, request, &target)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("DecodeJSON() error = %v", err)
				}
				if target.Name != "Room" {
					t.Fatalf("name = %q", target.Name)
				}
				return
			}
			if err == nil || err.Error() != test.wantErr {
				t.Fatalf("DecodeJSON() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestDecodeJSONLimitsBody(t *testing.T) {
	recorder := httptest.NewRecorder()
	body := `{"name":"` + strings.Repeat("x", 1<<20) + `"}`
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	var target struct {
		Name string `json:"name"`
	}

	err := httpx.DecodeJSON(recorder, request, &target)
	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("DecodeJSON() error = %v, want MaxBytesError", err)
	}
}

func TestRequireRoomManager(t *testing.T) {
	databaseError := errors.New("database unavailable")
	tests := []struct {
		name       string
		sessionSub string
		admin      bool
		room       store.Room
		loadErr    error
		wantStatus int
		wantBody   string
		wantOK     bool
	}{
		{name: "no session", wantStatus: http.StatusUnauthorized, wantBody: "{\"error\":\"Authentication required.\"}\n"},
		{name: "missing room", sessionSub: "owner", loadErr: sql.ErrNoRows, wantStatus: http.StatusNotFound, wantBody: "{\"error\":\"Room not found.\"}\n"},
		{name: "load failure", sessionSub: "owner", loadErr: databaseError, wantStatus: http.StatusInternalServerError, wantBody: "{\"error\":\"Could not load the room. Try again.\"}\n"},
		{name: "not owner", sessionSub: "guest", room: store.Room{OwnerSub: "owner"}, wantStatus: http.StatusForbidden, wantBody: "{\"error\":\"Owners only.\"}\n"},
		{name: "owner", sessionSub: "owner", room: store.Room{Slug: "room", OwnerSub: "owner"}, wantStatus: http.StatusOK, wantOK: true},
		{name: "global admin", sessionSub: "admin", admin: true, room: store.Room{Slug: "room", OwnerSub: "owner"}, wantStatus: http.StatusOK, wantOK: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/rooms/room", nil)
			if test.sessionSub != "" {
				request = request.WithContext(auth.WithSession(request.Context(), auth.Session{
					Sub: test.sessionSub, IsAdmin: test.admin,
				}))
			}
			loader := roomLoaderFunc(func(context.Context, string) (store.Room, error) {
				return test.room, test.loadErr
			})
			room, session, ok := httpx.RequireRoomManager(recorder, request, loader, "room", "Owners only.")
			if ok != test.wantOK {
				t.Fatalf("ok = %t, want %t", ok, test.wantOK)
			}
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			if got := recorder.Body.String(); got != test.wantBody {
				t.Fatalf("body = %q, want %q", got, test.wantBody)
			}
			if ok && (room != test.room || session.Sub != test.sessionSub) {
				t.Fatalf("room/session = %#v/%#v", room, session)
			}
		})
	}
}

// A recording route answers only its room's managers, finding the room by
// its ID: the slug copied onto a recording is only a denormalized copy.
func TestRequireRecordingManager(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "klisi.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, room := range []store.Room{
		{ID: "room-alice", Slug: "standup", Name: "Standup", OwnerSub: "alice", CreatedAt: 1},
		{ID: "room-bob", Slug: "retro", Name: "Retro", OwnerSub: "bob", CreatedAt: 1},
	} {
		if err := db.CreateRoom(ctx, room); err != nil {
			t.Fatal(err)
		}
	}
	// Alice's recording, with a slug that names Bob's room.
	if err := db.InsertRecording(ctx, store.Recording{
		ID: "rec-1", RoomID: "room-alice", RoomSlug: "retro", EgressID: "egress-1",
		Status: "completed", StartedBy: "alice", StartedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	call := func(session *auth.Session, id string) (*httptest.ResponseRecorder, store.Recording, store.Room, bool) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/recordings/"+id, nil)
		if session != nil {
			request = request.WithContext(auth.WithSession(request.Context(), *session))
		}
		recording, room, _, ok := httpx.RequireRecordingManager(recorder, request, db, id, "Owners only.")
		return recorder, recording, room, ok
	}
	for _, test := range []struct {
		name     string
		session  *auth.Session
		id       string
		status   int
		body     string
		wantRoom string
	}{
		{name: "signed out", id: "rec-1", status: http.StatusUnauthorized, body: `{"error":"Authentication required."}`},
		{name: "signed out, no such recording", id: "nope", status: http.StatusUnauthorized, body: `{"error":"Authentication required."}`},
		{name: "no such recording", session: &auth.Session{Sub: "alice"}, id: "nope", status: http.StatusNotFound, body: `{"error":"Recording not found."}`},
		{name: "the slug's room owner", session: &auth.Session{Sub: "bob"}, id: "rec-1", status: http.StatusForbidden, body: `{"error":"Owners only."}`},
		{name: "the room's owner", session: &auth.Session{Sub: "alice"}, id: "rec-1", status: http.StatusOK, wantRoom: "room-alice"},
		{name: "an administrator", session: &auth.Session{Sub: "root", IsAdmin: true}, id: "rec-1", status: http.StatusOK, wantRoom: "room-alice"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder, recording, room, ok := call(test.session, test.id)
			if recorder.Code != test.status || strings.TrimSpace(recorder.Body.String()) != test.body {
				t.Fatalf("answer = %d %s, want %d %s", recorder.Code, recorder.Body, test.status, test.body)
			}
			if ok != (test.wantRoom != "") || (ok && (recording.ID != test.id || room.ID != test.wantRoom)) {
				t.Fatalf("ok = %t, recording %q, room %q", ok, recording.ID, room.ID)
			}
		})
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	recorder, _, _, ok := call(&auth.Session{Sub: "alice"}, "rec-1")
	if ok || recorder.Code != http.StatusInternalServerError ||
		strings.TrimSpace(recorder.Body.String()) != `{"error":"Could not load the recording. Try again."}` {
		t.Fatalf("with the database closed: %d %s", recorder.Code, recorder.Body)
	}
}

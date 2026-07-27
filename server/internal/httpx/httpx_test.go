package httpx_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
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

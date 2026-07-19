package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/config"
	"klisi/internal/store"
)

func TestJoinPolicyMatrix(t *testing.T) {
	db, err := store.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	rooms := []store.Room{
		{ID: "on", Slug: "calm-otter-412", Name: "Lobby on", OwnerSub: "owner", LobbyEnabled: true, CreatedAt: 1},
		{ID: "off", Slug: "quiet-fox-713", Name: "Lobby off", OwnerSub: "owner", LobbyEnabled: false, CreatedAt: 2},
	}
	for _, room := range rooms {
		if err := db.CreateRoom(ctx, room); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{
		BaseURL: "http://localhost:8080", SessionSecret: "test-session-secret",
		LiveKitAPIKey: "devkey", LiveKitAPISecret: "test-livekit-secret-with-enough-bytes",
		LiveKitPublicURL: "ws://public.example", DevMode: true,
	}
	handler, _ := New(cfg, nil, db)
	ownerCookie := makeSessionCookie(t, cfg, auth.Session{Sub: "owner", Email: "owner@example.com", Name: "Owner"})

	tests := []struct {
		name        string
		slug        string
		cookie      *http.Cookie
		wantStatus  string
		wantToken   bool
		wantRequest bool
	}{
		{name: "owner bypasses lobby", slug: rooms[0].Slug, cookie: ownerCookie, wantStatus: "admitted", wantToken: true},
		{name: "guest waits when lobby enabled", slug: rooms[0].Slug, wantStatus: "waiting", wantRequest: true},
		{name: "guest enters when lobby disabled", slug: rooms[1].Slug, wantStatus: "admitted", wantToken: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, _ := json.Marshal(api.JoinRequest{Name: "Guest"})
			request := httptest.NewRequest(http.MethodPost, "/api/rooms/"+test.slug+"/join", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Klisi-Csrf", "1")
			if test.cookie != nil {
				request.AddCookie(test.cookie)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			var response api.JoinResponse
			if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
				t.Fatal(err)
			}
			if response.Status != test.wantStatus || (response.Token != "") != test.wantToken || (response.RequestID != "") != test.wantRequest {
				t.Fatalf("response = %#v", response)
			}
			if test.wantToken && response.WSURL != cfg.LiveKitPublicURL {
				t.Fatalf("ws_url = %q", response.WSURL)
			}
		})
	}
}

func makeSessionCookie(t *testing.T, cfg config.Config, session auth.Session) *http.Cookie {
	t.Helper()
	sessions := auth.NewSessions(cfg.SessionSecret, cfg.BaseURL)
	recorder := httptest.NewRecorder()
	if err := sessions.Set(recorder, session); err != nil {
		t.Fatal(err)
	}
	return recorder.Result().Cookies()[0]
}

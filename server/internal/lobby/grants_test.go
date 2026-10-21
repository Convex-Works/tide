package lobby

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	protocolauth "github.com/livekit/protocol/auth"

	"tide/internal/api"
	"tide/internal/auth"
	"tide/internal/config"
	tidelivekit "tide/internal/livekit"
	"tide/internal/store"
)

const grantsTestSecret = "test-livekit-secret-with-enough-bytes"

// Every way into a room mints a token, and only the room's managers get
// admin in it (ARCHITECTURE.md §4): its owner, and a global administrator.
// Everyone else, signed in or not, admitted directly or through the lobby,
// is a guest.
func TestAdmissionTokenGrants(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "tide.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rooms := map[bool]store.Room{
		true:  {ID: "on", Slug: "calm-otter-412", Name: "Lobby on", OwnerSub: "owner", LobbyEnabled: true, CreatedAt: 1},
		false: {ID: "off", Slug: "quiet-fox-713", Name: "Lobby off", OwnerSub: "owner", CreatedAt: 2},
	}
	for _, room := range rooms {
		if err := db.CreateRoom(context.Background(), room); err != nil {
			t.Fatal(err)
		}
	}
	minter := tidelivekit.NewMinter(config.Config{
		LiveKitAPIKey: "devkey", LiveKitAPISecret: grantsTestSecret, LiveKitPublicURL: "ws://public.example",
	})
	handler := NewHandler(db, NewRegistry(time.Minute), minter)

	owner := &auth.Session{Sub: "owner", Name: "Owner"}
	admin := &auth.Session{Sub: "admin", Name: "Admin", IsAdmin: true}
	other := &auth.Session{Sub: "other", Name: "Other"}
	anonymous := &auth.Session{Sub: "anon:" + strings.Repeat("a", 26)}
	tests := []struct {
		name     string
		session  *auth.Session
		lobby    bool
		host     bool
		identity string
		display  string
	}{
		{name: "owner", session: owner, lobby: true, host: true, identity: "host:owner:", display: "Owner"},
		{name: "owner, lobby off", session: owner, host: true, identity: "host:owner:", display: "Owner"},
		{name: "global admin", session: admin, lobby: true, host: true, identity: "host:admin:", display: "Admin"},
		{name: "signed-in non-owner through the lobby", session: other, lobby: true, identity: "guest:", display: "Typed"},
		{name: "signed-in non-owner, lobby off", session: other, identity: "guest:", display: "Typed"},
		{name: "anonymous session through the lobby", session: anonymous, lobby: true, identity: "guest:", display: "Typed"},
		{name: "guest without a session through the lobby", lobby: true, identity: "guest:", display: "Typed"},
		{name: "guest without a session, lobby off", identity: "guest:", display: "Typed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			room := rooms[test.lobby]
			recorder := httptest.NewRecorder()
			handler.Join(recorder, withSession(
				httptest.NewRequest(http.MethodPost, "/api/rooms/"+room.Slug+"/join", strings.NewReader(`{"name":"Typed"}`)),
				test.session, "slug", room.Slug,
			))
			if recorder.Code != http.StatusOK {
				t.Fatalf("join = %d %s", recorder.Code, recorder.Body)
			}
			var joined api.JoinResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &joined); err != nil {
				t.Fatal(err)
			}
			token := joined.Token
			if test.lobby && !test.host {
				if joined.Status != string(StatusWaiting) || token != "" {
					t.Fatalf("join = %+v, want the lobby", joined)
				}
				token = approve(t, handler, owner, joined.RequestID)
			} else if joined.Status != string(StatusAdmitted) || joined.WSURL != "ws://public.example" {
				t.Fatalf("join = %+v, want admitted", joined)
			}
			assertGrants(t, token, room.Slug, test.host, test.identity, test.display)
		})
	}
}

// approve admits a waiting request as manager would, and returns the token
// its guest is handed.
func approve(t *testing.T, handler *Handler, manager *auth.Session, id string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.Approve(recorder, withSession(
		httptest.NewRequest(http.MethodPost, "/api/lobby/"+id+"/approve", nil), manager, "id", id,
	))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("approve = %d %s", recorder.Code, recorder.Body)
	}
	admitted, ok := handler.registry.Get(id)
	if !ok || admitted.Status != StatusAdmitted || admitted.WSURL != "ws://public.example" {
		t.Fatalf("approved request = %+v", admitted)
	}
	return admitted.Token
}

func withSession(r *http.Request, session *auth.Session, key, value string) *http.Request {
	r.SetPathValue(key, value)
	if session != nil {
		r = r.WithContext(auth.WithSession(r.Context(), *session))
	}
	return r
}

// assertGrants verifies token as the media server would and fails unless it
// grants exactly what a host, or a guest, gets in room.
func assertGrants(t *testing.T, token, room string, host bool, identityPrefix, name string) {
	t.Helper()
	verifier, err := protocolauth.ParseAPIToken(token)
	if err != nil {
		t.Fatal(err)
	}
	registered, claims, err := verifier.Verify(grantsTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	if ttl := registered.ExpiresAt.Sub(registered.NotBefore.Time); ttl != TokenTTL {
		t.Errorf("token lives %s, want %s", ttl, TokenTTL)
	}
	if !strings.HasPrefix(claims.Identity, identityPrefix) || len(claims.Identity) <= len(identityPrefix) {
		t.Errorf("identity = %q, want %q and a nonce", claims.Identity, identityPrefix)
	}
	yes := true
	want := &protocolauth.ClaimGrants{
		Identity: claims.Identity, Name: name,
		Video: &protocolauth.VideoGrant{
			RoomJoin: true, Room: room, RoomAdmin: host,
			CanPublish: &yes, CanSubscribe: &yes,
			CanPublishSources: []string{"camera", "microphone", "screen_share", "screen_share_audio"},
		},
	}
	if host {
		want.Metadata = `{"role":"host"}`
	}
	if !reflect.DeepEqual(claims, want) {
		t.Errorf("grants\n%+v %+v\nwant\n%+v %+v", claims, claims.Video, want, want.Video)
	}
}

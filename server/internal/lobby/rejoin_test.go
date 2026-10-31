package lobby

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	protocolauth "github.com/livekit/protocol/auth"
	protocol "github.com/livekit/protocol/livekit"

	"tide/internal/api"
	"tide/internal/auth"
	"tide/internal/config"
	tidelivekit "tide/internal/livekit"
	"tide/internal/store"
)

// fakeLiveRooms is LiveKit's view of the meetings running: slug → room.
type fakeLiveRooms map[string]*protocol.Room

func (f fakeLiveRooms) ListRooms(_ context.Context, request *protocol.ListRoomsRequest) (*protocol.ListRoomsResponse, error) {
	response := &protocol.ListRoomsResponse{}
	for _, name := range request.Names {
		if room, ok := f[name]; ok {
			response.Rooms = append(response.Rooms, room)
		}
	}
	return response, nil
}

type fakeBans map[string]bool

func (f fakeBans) Banned(room, identity string) bool { return f[room+"\x00"+identity] }

type rejoinFixture struct {
	handler *Handler
	live    fakeLiveRooms
	bans    fakeBans
	room    store.Room
	other   store.Room
	owner   *auth.Session
}

func newRejoinFixture(t *testing.T) *rejoinFixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "tide.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	room := store.Room{ID: "on", Slug: "calm-otter-412", Name: "Lobby on", OwnerSub: "owner", LobbyEnabled: true, CreatedAt: 1}
	other := store.Room{ID: "other", Slug: "quiet-fox-713", Name: "Other", OwnerSub: "owner", LobbyEnabled: true, CreatedAt: 2}
	for _, r := range []store.Room{room, other} {
		if err := db.CreateRoom(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	handler := NewHandler(db, NewRegistry(time.Minute), tidelivekit.NewMinter(config.Config{
		LiveKitAPIKey: "devkey", LiveKitAPISecret: grantsTestSecret, LiveKitPublicURL: "ws://public.example",
	}))
	live := fakeLiveRooms{
		room.Slug:  {Name: room.Slug, Sid: "RM_meeting", NumParticipants: 2},
		other.Slug: {Name: other.Slug, Sid: "RM_meeting", NumParticipants: 2},
	}
	bans := fakeBans{}
	handler.EnableRejoin(live, bans)
	return &rejoinFixture{handler: handler, live: live, bans: bans, room: room, other: other, owner: &auth.Session{Sub: "owner", Name: "Owner"}}
}

// admit puts a guest through room's lobby and the owner's approval, and
// returns the identity they were admitted as and the grant they were handed.
func (f *rejoinFixture) admit(t *testing.T, room store.Room) (string, string) {
	t.Helper()
	joined := f.join(t, room, "Admitted", "")
	if joined.Status != string(StatusWaiting) {
		t.Fatalf("first join = %+v, want the lobby", joined)
	}
	token := approve(t, f.handler, f.owner, joined.RequestID)
	admitted, _ := f.handler.registry.Get(joined.RequestID)
	if admitted.Rejoin == "" {
		t.Fatal("an admitted guest got no rejoin grant")
	}
	return tokenIdentity(t, token), admitted.Rejoin
}

func (f *rejoinFixture) join(t *testing.T, room store.Room, name, grant string) api.JoinResponse {
	t.Helper()
	body, _ := json.Marshal(api.JoinRequest{Name: name, Rejoin: grant})
	recorder := httptest.NewRecorder()
	f.handler.Join(recorder, withSession(
		httptest.NewRequest(http.MethodPost, "/api/rooms/"+room.Slug+"/join", strings.NewReader(string(body))),
		nil, "slug", room.Slug,
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("join = %d %s", recorder.Code, recorder.Body)
	}
	var joined api.JoinResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &joined); err != nil {
		t.Fatal(err)
	}
	return joined
}

func tokenIdentity(t *testing.T, token string) string {
	t.Helper()
	verifier, err := protocolauth.ParseAPIToken(token)
	if err != nil {
		t.Fatal(err)
	}
	_, claims, err := verifier.Verify(grantsTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	return claims.Identity
}

// A guest the host admitted who leaves, loses the connection or reloads
// must get back into the meeting without the host admitting them again,
// and as the same participant with the name the host let in, so moderation
// keyed on the identity keeps applying.
func TestRejoinGrantReadmitsWithoutTheLobby(t *testing.T) {
	f := newRejoinFixture(t)
	identity, grant := f.admit(t, f.room)

	for range 2 {
		rejoined := f.join(t, f.room, "Someone else", grant)
		if rejoined.Status != string(StatusAdmitted) || rejoined.Token == "" {
			t.Fatalf("rejoin = %+v, want admitted without the lobby", rejoined)
		}
		assertGrants(t, rejoined.Token, f.room.Slug, false, "guest:", "Admitted")
		if got := tokenIdentity(t, rejoined.Token); got != identity {
			t.Fatalf("rejoined as %q, want the admitted identity %q", got, identity)
		}
	}
	if pending := f.handler.registry.Pending(f.room.Slug); len(pending) != 0 {
		t.Fatalf("a rejoin queued %d lobby requests", len(pending))
	}

	// The grant reaches the guest in the admitted event.
	joined := f.join(t, f.room, "Next", "")
	approve(t, f.handler, f.owner, joined.RequestID)
	recorder := httptest.NewRecorder()
	f.handler.Wait(recorder, withSession(
		httptest.NewRequest(http.MethodGet, "/api/lobby/"+joined.RequestID+"/wait", nil), nil, "id", joined.RequestID,
	))
	if !strings.Contains(recorder.Body.String(), `"rejoin":"`) {
		t.Fatalf("admitted event carries no grant: %q", recorder.Body)
	}
}

// Every grant that does not hold sends its holder to the lobby, never to
// an error: the host decides again.
func TestRejoinGrantRefusedFallsBackToTheLobby(t *testing.T) {
	tests := []struct {
		name  string
		grant func(*testing.T, *rejoinFixture) string
	}{
		{name: "kicked", grant: func(t *testing.T, f *rejoinFixture) string {
			identity, grant := f.admit(t, f.room)
			f.bans[f.room.Slug+"\x00"+identity] = true
			return grant
		}},
		{name: "meeting ended", grant: func(t *testing.T, f *rejoinFixture) string {
			_, grant := f.admit(t, f.room)
			delete(f.live, f.room.Slug)
			return grant
		}},
		{name: "everyone left", grant: func(t *testing.T, f *rejoinFixture) string {
			_, grant := f.admit(t, f.room)
			f.live[f.room.Slug] = &protocol.Room{Name: f.room.Slug, Sid: "RM_meeting"}
			return grant
		}},
		{name: "a later meeting in the room", grant: func(t *testing.T, f *rejoinFixture) string {
			_, grant := f.admit(t, f.room)
			f.live[f.room.Slug] = &protocol.Room{Name: f.room.Slug, Sid: "RM_later", NumParticipants: 2}
			return grant
		}},
		{name: "past the cap", grant: func(t *testing.T, f *rejoinFixture) string {
			_, grant := f.admit(t, f.room)
			f.handler.rejoin.now = func() time.Time { return time.Now().Add(RejoinTTL + time.Second) }
			return grant
		}},
		{name: "another room's grant", grant: func(t *testing.T, f *rejoinFixture) string {
			_, grant := f.admit(t, f.other)
			return grant
		}},
		{name: "another server's grant", grant: func(t *testing.T, f *rejoinFixture) string {
			_, grant := f.admit(t, f.room)
			f.handler.EnableRejoin(f.live, f.bans)
			return grant
		}},
		{name: "claims altered", grant: func(t *testing.T, f *rejoinFixture) string {
			_, grant := f.admit(t, f.room)
			payload, signature, _ := strings.Cut(grant, ".")
			raw, err := base64.RawURLEncoding.DecodeString(payload)
			if err != nil {
				t.Fatal(err)
			}
			var claims rejoinClaims
			if err := json.Unmarshal(raw, &claims); err != nil {
				t.Fatal(err)
			}
			claims.Identity = "guest:victim"
			raw, _ = json.Marshal(claims)
			return base64.RawURLEncoding.EncodeToString(raw) + "." + signature
		}},
		{name: "garbage", grant: func(*testing.T, *rejoinFixture) string { return "not.a-grant" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newRejoinFixture(t)
			grant := test.grant(t, f)
			rejoined := f.join(t, f.room, "Admitted", grant)
			if rejoined.Status != string(StatusWaiting) || rejoined.Token != "" || rejoined.RequestID == "" {
				t.Fatalf("rejoin = %+v, want the lobby", rejoined)
			}
		})
	}
}

// Just inside its cap a grant still admits, so the cap, not some other
// check, is what refuses it a second later.
func TestRejoinGrantHoldsUntilTheCap(t *testing.T) {
	f := newRejoinFixture(t)
	_, grant := f.admit(t, f.room)
	f.handler.rejoin.now = func() time.Time { return time.Now().Add(RejoinTTL - time.Minute) }
	if rejoined := f.join(t, f.room, "Admitted", grant); rejoined.Status != string(StatusAdmitted) {
		t.Fatalf("rejoin = %+v, want admitted inside the cap", rejoined)
	}
}

// A guest admitted when no meeting is running (the host approved from
// outside it) gets no grant, and is admitted all the same.
func TestApproveWithoutAMeetingIssuesNoGrant(t *testing.T) {
	f := newRejoinFixture(t)
	delete(f.live, f.room.Slug)
	joined := f.join(t, f.room, "Admitted", "")
	approve(t, f.handler, f.owner, joined.RequestID)
	if admitted, _ := f.handler.registry.Get(joined.RequestID); admitted.Rejoin != "" {
		t.Fatalf("grant %q bound to no meeting", admitted.Rejoin)
	}
}

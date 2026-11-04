package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	protocolauth "github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"

	"tide/internal/api"
	"tide/internal/auth"
	"tide/internal/config"
	"tide/internal/lobby"
	"tide/internal/store"
)

// The agent session route (docs/MEETING-AGENTS.md), through tide's real
// handler over HTTP.

const agentRoomSlug = "calm-otter-412"

// signedInAgents starts a signed-in tide with one room, belonging to "owner".
func signedInAgents(t *testing.T, configure func(*config.Config)) *tideServer {
	t.Helper()
	k := startTide(t, func(cfg *config.Config, _ *http.Server) {
		cfg.Recording = false
		cfg.LiveKitPublicURL = "ws://public.example"
		cfg.JoinRateLimit = 100
		behindProxy(cfg)
		if configure != nil {
			configure(cfg)
		}
	})
	if err := k.db.CreateRoom(context.Background(), store.Room{
		ID: "r1", Slug: agentRoomSlug, Name: "Standup", OwnerSub: "owner", LobbyEnabled: true, CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	return k
}

func (k *tideServer) signedIn(from string, session auth.Session) *browser {
	k.t.Helper()
	return &browser{k: k, from: from, cookie: makeSessionCookie(k.t, k.cfg, session)}
}

// Only the room's owner gets a session, which is narrower than the room's
// managers: an administrator can record any room, but an agent is a new live
// listener and the room's audio stays with its owner (§8.1).
func TestAgentSessionIsTheOwnersAlone(t *testing.T) {
	k := signedInAgents(t, nil)
	path := fill(api.AgentSessionPath, agentRoomSlug)
	tests := []struct {
		name    string
		browser *browser
		path    string
		want    int
	}{
		{name: "owner", browser: k.signedIn("192.0.2.1", auth.Session{Sub: "owner"}), path: path, want: http.StatusOK},
		{name: "administrator", browser: k.signedIn("192.0.2.2", auth.Session{Sub: "admin", IsAdmin: true}), path: path, want: http.StatusForbidden},
		{name: "another host", browser: k.signedIn("192.0.2.3", auth.Session{Sub: "other"}), path: path, want: http.StatusForbidden},
		// Someone with no session at all has no sub to compare, as on every
		// room mutation.
		{name: "guest without a session", browser: k.browser("192.0.2.4"), path: path, want: http.StatusUnauthorized},
		{name: "unknown room", browser: k.signedIn("192.0.2.5", auth.Session{Sub: "owner"}), path: fill(api.AgentSessionPath, "no-such-room"), want: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.browser.want(http.MethodPost, test.path, "", test.want, nil)
		})
	}
}

func TestAgentSessionRefusesAMissingCSRFHeader(t *testing.T) {
	k := signedInAgents(t, nil)
	owner := k.signedIn("192.0.2.1", auth.Session{Sub: "owner"})
	status, _, answer := k.requestFrom(owner.from, http.MethodPost, fill(api.AgentSessionPath, agentRoomSlug), "", owner.cookie, false)
	if status != http.StatusForbidden || !strings.Contains(answer, "CSRF") {
		t.Fatalf("without the CSRF header = %d %s", status, answer)
	}
}

func TestAgentSessionOnlyTakesPost(t *testing.T) {
	k := signedInAgents(t, nil)
	owner := k.signedIn("192.0.2.1", auth.Session{Sub: "owner"})
	header := owner.want(http.MethodGet, fill(api.AgentSessionPath, agentRoomSlug), "", http.StatusMethodNotAllowed, nil)
	if header.Get("Allow") != http.MethodPost {
		t.Fatalf("Allow = %q", header.Get("Allow"))
	}
}

// The token is the whole of what an agent may do (§4): this is what the
// media server will read.
func TestAgentSessionCarriesTheAgentGrants(t *testing.T) {
	k := signedInAgents(t, nil)
	owner := k.signedIn("192.0.2.1", auth.Session{Sub: "owner"})
	before := time.Now()
	var session api.AgentSession
	owner.want(http.MethodPost, fill(api.AgentSessionPath, agentRoomSlug), `{"name":"Notes"}`, http.StatusOK, &session)

	if session.WSURL != "ws://public.example" || session.Room != agentRoomSlug || session.Name != "Notes" ||
		session.OwnerIdentity != "host:owner" {
		t.Fatalf("session = %+v", session)
	}
	// 32 bits from crypto/rand, as a guest's identity has.
	if len(session.Identity) != len("agent:")+8 || !strings.HasPrefix(session.Identity, "agent:") {
		t.Fatalf("identity = %q", session.Identity)
	}
	verifier, err := protocolauth.ParseAPIToken(session.Token)
	if err != nil {
		t.Fatal(err)
	}
	registered, claims, err := verifier.Verify(k.cfg.LiveKitAPISecret)
	if err != nil {
		t.Fatal(err)
	}
	// expires_at says when the token stops connecting, in seconds.
	if got, want := session.ExpiresAt, registered.ExpiresAt.Unix(); got > want || want-got > 1 {
		t.Fatalf("expires_at = %d, the token says %d", got, want)
	}
	if ttl := time.Unix(session.ExpiresAt, 0).Sub(before); ttl < lobby.TokenTTL-2*time.Second || ttl > lobby.TokenTTL+2*time.Second {
		t.Fatalf("expires_at is %s away, want %s", ttl, lobby.TokenTTL)
	}
	yes := true
	want := &protocolauth.ClaimGrants{
		Identity: session.Identity, Name: "Notes", Metadata: `{"role":"agent"}`,
		Video: &protocolauth.VideoGrant{
			RoomJoin: true, Room: agentRoomSlug,
			CanPublish: &yes, CanSubscribe: &yes, CanPublishData: &yes,
			CanPublishSources: []string{"microphone", "screen_share", "screen_share_audio"},
		},
	}
	want.SetParticipantKind(livekit.ParticipantInfo_AGENT)
	if !reflect.DeepEqual(claims, want) {
		t.Fatalf("grants\n%+v %+v\nwant\n%+v %+v", claims, claims.Video, want, want.Video)
	}
}

// Every session is a new identity, or a second one would push the first out
// of the room: LiveKit allows one participant per identity.
func TestAgentSessionsAreDistinct(t *testing.T) {
	k := signedInAgents(t, nil)
	owner := k.signedIn("192.0.2.1", auth.Session{Sub: "owner"})
	path := fill(api.AgentSessionPath, agentRoomSlug)
	var first, second api.AgentSession
	owner.want(http.MethodPost, path, "", http.StatusOK, &first)
	owner.want(http.MethodPost, path, "", http.StatusOK, &second)
	if first.Identity == second.Identity {
		t.Fatalf("both sessions are %s", first.Identity)
	}
}

// A name is held to a guest's rules; only leaving it out gives the default.
func TestAgentSessionName(t *testing.T) {
	k := signedInAgents(t, nil)
	owner := k.signedIn("192.0.2.1", auth.Session{Sub: "owner"})
	path := fill(api.AgentSessionPath, agentRoomSlug)
	longest := strings.Repeat("a", 100)
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{name: "no body", body: "", want: "Agent"},
		{name: "no name", body: `{}`, want: "Agent"},
		{name: "a name", body: `{"name":"Notes"}`, want: "Notes"},
		{name: "trimmed", body: `{"name":"  Notes  "}`, want: "Notes"},
		{name: "as long as a guest's", body: fmt.Sprintf(`{"name":%q}`, longest), want: longest},
	} {
		t.Run(test.name, func(t *testing.T) {
			var session api.AgentSession
			owner.want(http.MethodPost, path, test.body, http.StatusOK, &session)
			if session.Name != test.want || grants(t, k.cfg, session.Token).Name != test.want {
				t.Fatalf("session = %+v, want the name %q", session, test.want)
			}
		})
	}
	for _, test := range []struct{ name, body, message string }{
		{"empty", `{"name":""}`, "Display name"},
		{"blank", `{"name":"   "}`, "Display name"},
		{"too long", fmt.Sprintf(`{"name":%q}`, longest+"a"), "Display name"},
		{"not JSON", `{"name":`, "valid JSON"},
		{"an unknown field", `{"role":"host"}`, "valid JSON"},
		{"not a string", `{"name":7}`, "valid JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, _, answer := owner.send(http.MethodPost, path, test.body)
			if status != http.StatusBadRequest || !strings.Contains(answer, test.message) {
				t.Fatalf("%s = %d %s", test.body, status, answer)
			}
		})
	}
}

// A session is a token for the room, and shares the join limit: a client gets
// that many of the two together.
func TestAgentSessionCountsAsAJoin(t *testing.T) {
	const limit = 3
	k := signedInAgents(t, func(cfg *config.Config) { cfg.JoinRateLimit = limit })
	owner := k.signedIn("192.0.2.1", auth.Session{Sub: "owner"})
	path := fill(api.AgentSessionPath, agentRoomSlug)
	owner.join(agentRoomSlug, "Owner")
	for range limit - 1 {
		owner.want(http.MethodPost, path, "", http.StatusOK, nil)
	}
	owner.want(http.MethodPost, path, "", http.StatusTooManyRequests, nil)
	// A join from the same client is over the limit too, and it's per client.
	status, _, answer := owner.send(http.MethodPost, fill(api.RoomJoinPath, agentRoomSlug), `{"name":"Owner"}`)
	if status != http.StatusTooManyRequests {
		t.Fatalf("join past the limit = %d %s", status, answer)
	}
	k.signedIn("192.0.2.2", auth.Session{Sub: "owner"}).want(http.MethodPost, path, "", http.StatusOK, nil)
}

// It needs no machine, so it works with no sign-in at all (§4.1): an
// anonymous owner is an owner, every other session a guest.
func TestAgentSessionInAnonymousMode(t *testing.T) {
	k := startTide(t, anonymousMode(100))
	owner := k.browser("192.0.2.30")
	owner.me()
	var room api.RoomInfo
	owner.want(http.MethodPost, api.RoomsPath, `{"name":"Open"}`, http.StatusCreated, &room)
	stored, err := k.db.RoomBySlug(context.Background(), room.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastActiveAt != nil {
		t.Fatal("a room nobody joined is active")
	}

	var session api.AgentSession
	owner.want(http.MethodPost, fill(api.AgentSessionPath, room.Slug), "", http.StatusOK, &session)
	if session.Token == "" || session.Name != "Agent" {
		t.Fatalf("session = %+v", session)
	}
	if kind := grants(t, k.cfg, session.Token).GetParticipantKind(); kind != livekit.ParticipantInfo_AGENT {
		t.Fatalf("kind = %v", kind)
	}
	// Every token marks its room active before it leaves tide (§5), or the
	// sweep could delete the room under the agent.
	stored, err = k.db.RoomBySlug(context.Background(), room.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastActiveAt == nil {
		t.Fatal("the session left without marking the room active")
	}

	guest := k.browser("192.0.2.31")
	guest.me()
	guest.want(http.MethodPost, fill(api.AgentSessionPath, room.Slug), "", http.StatusForbidden, nil)
}

// The room's public face says who owns it, for the SPA to show the owner's
// control: an administrator manages the room but doesn't own it.
func TestRoomInfoNamesOnlyTheOwner(t *testing.T) {
	k := signedInAgents(t, nil)
	path := fill(api.RoomPath, agentRoomSlug)
	for _, test := range []struct {
		name    string
		browser *browser
		manage  bool
		owner   bool
	}{
		{"owner", k.signedIn("192.0.2.1", auth.Session{Sub: "owner"}), true, true},
		{"administrator", k.signedIn("192.0.2.2", auth.Session{Sub: "admin", IsAdmin: true}), true, false},
		{"another host", k.signedIn("192.0.2.3", auth.Session{Sub: "other"}), false, false},
		{"guest without a session", k.browser("192.0.2.4"), false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var info api.PublicRoomInfo
			test.browser.want(http.MethodGet, path, "", http.StatusOK, &info)
			if info.CanManage != test.manage || info.IsOwner != test.owner {
				t.Fatalf("info = %+v", info)
			}
		})
	}
}

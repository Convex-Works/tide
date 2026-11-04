package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"

	"tide/internal/auth"
	"tide/internal/config"
	tidelivekit "tide/internal/livekit"
	"tide/internal/lobby"
	"tide/internal/moderation"
	"tide/internal/store"
)

// Bots and tide's own observer on a real media server, with the tokens
// tide mints (ARCHITECTURE.md §4, docs/BOTS.md).

const botTestRoom = "abc-defg-hij"

func testMinter() *tidelivekit.Minter {
	return tidelivekit.NewMinter(config.Config{LiveKitAPIKey: testKey, LiveKitAPISecret: testSecret})
}

// joinAs connects a participant the way a client holding a tide token does.
func joinAs(t *testing.T, s *Server, identity string, role tidelivekit.Role, callback *lksdk.RoomCallback) *lksdk.Room {
	t.Helper()
	token, err := testMinter().MintToken(identity, identity, botTestRoom, role, lobby.TokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	return connect(t, s, identity, token, callback)
}

func connect(t *testing.T, s *Server, identity, token string, callback *lksdk.RoomCallback) *lksdk.Room {
	t.Helper()
	if callback == nil {
		callback = lksdk.NewRoomCallback()
	}
	room, err := lksdk.ConnectToRoomWithToken(s.URL(), token, callback, lksdk.WithAutoSubscribe(false))
	if err != nil {
		t.Fatalf("%s connects: %v", identity, err)
	}
	t.Cleanup(room.Disconnect)
	return room
}

func participants(t *testing.T, service *lksdk.RoomServiceClient) map[string]*livekit.ParticipantInfo {
	t.Helper()
	response, err := service.ListParticipants(context.Background(), &livekit.ListParticipantsRequest{Room: botTestRoom})
	if err != nil {
		t.Fatalf("list participants: %v", err)
	}
	found := map[string]*livekit.ParticipantInfo{}
	for _, participant := range response.Participants {
		found[participant.Identity] = participant
	}
	return found
}

// A bot doesn't hold a room open: once the last person leaves, the room
// closes as it would have, and the bot is told. Otherwise a program left
// running would keep a recording going, and a meeting alive, forever.
func TestRoomClosesWhenOnlyAnBotRemains(t *testing.T) {
	opts := testOptions(t)
	opts.Dev = true // 5 seconds' departure timeout rather than 20.
	s := startServer(t, opts)
	service := lksdk.NewRoomServiceClient(s.URL(), testKey, testSecret)

	disconnected := make(chan lksdk.DisconnectionReason, 1)
	callback := lksdk.NewRoomCallback()
	callback.OnDisconnectedWithReason = func(reason lksdk.DisconnectionReason) {
		disconnected <- reason
	}
	guest := joinAs(t, s, "guest:person", tidelivekit.RoleGuest, nil)
	bot := joinAs(t, s, "bot:program", tidelivekit.RoleBot, callback)
	eventually(t, 10*time.Second, "both in the room", func() bool {
		return len(participants(t, service)) == 2
	})

	guest.Disconnect()
	eventually(t, 10*time.Second, "the person to leave", func() bool {
		return len(participants(t, service)) == 1
	})
	// The room is open with the bot alone in it, for now.
	if bot.ConnectionState() != lksdk.ConnectionStateConnected {
		t.Fatalf("bot = %v once the person left", bot.ConnectionState())
	}
	eventually(t, 15*time.Second, "the room to close", func() bool {
		rooms, err := service.ListRooms(context.Background(), &livekit.ListRoomsRequest{})
		return err == nil && len(rooms.Rooms) == 0
	})
	select {
	case reason := <-disconnected:
		if reason != lksdk.RoomClosed {
			t.Fatalf("bot disconnected with %v", reason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the bot was not told the room closed")
	}
}

// Clients see a bot, as kind BOT, and never tide's hidden observer,
// whether it arrived before them or after: that kind alone is what makes a
// client say a program is in the room, and the observer has it too.
func TestClientsSeeBotsAndNeverTheObserver(t *testing.T) {
	s := startServer(t, testOptions(t))
	service := lksdk.NewRoomServiceClient(s.URL(), testKey, testSecret)

	joinAs(t, s, "tide:observer:before", tidelivekit.RoleObserver, nil)
	var mu sync.Mutex
	var announced []string
	callback := lksdk.NewRoomCallback()
	callback.OnParticipantConnected = func(participant *lksdk.RemoteParticipant) {
		mu.Lock()
		defer mu.Unlock()
		announced = append(announced, participant.Identity())
	}
	guest := joinAs(t, s, "guest:person", tidelivekit.RoleGuest, callback)
	joinAs(t, s, "tide:observer:after", tidelivekit.RoleObserver, nil)
	joinAs(t, s, "bot:program", tidelivekit.RoleBot, nil)

	eventually(t, 10*time.Second, "the bot to reach the guest", func() bool {
		return guest.GetParticipantByIdentity("bot:program") != nil
	})
	// The observers really are in the room, and the server says so.
	inRoom := participants(t, service)
	for _, identity := range []string{"tide:observer:before", "tide:observer:after"} {
		observer, ok := inRoom[identity]
		if !ok || observer.Kind != livekit.ParticipantInfo_AGENT {
			t.Fatalf("%s in the room = %v", identity, observer)
		}
	}
	// A later joiner gets the room's list at once, and it's no different.
	late := joinAs(t, s, "guest:late", tidelivekit.RoleGuest, nil)
	eventually(t, 10*time.Second, "the bot to reach a late guest", func() bool {
		return late.GetParticipantByIdentity("bot:program") != nil
	})
	for _, client := range []*lksdk.Room{guest, late} {
		for _, participant := range client.GetRemoteParticipants() {
			if identity := participant.Identity(); identity != "bot:program" && identity != "guest:person" && identity != "guest:late" {
				t.Fatalf("a client can see %s", identity)
			}
		}
	}
	if bot := guest.GetParticipantByIdentity("bot:program"); bot.Kind() != lksdk.ParticipantAgent {
		t.Fatalf("the bot's kind reaches clients as %v", bot.Kind())
	}
	mu.Lock()
	defer mu.Unlock()
	for _, identity := range announced {
		if identity != "bot:program" && identity != "guest:late" {
			t.Fatalf("a client was told %s joined", identity)
		}
	}
}

// A token only authorizes connecting: a connected bot stays past expires_at
// for as long as the meeting runs (docs/BOTS.md).
func TestAConnectedBotOutlivesItsToken(t *testing.T) {
	s := startServer(t, testOptions(t))
	service := lksdk.NewRoomServiceClient(s.URL(), testKey, testSecret)
	joinAs(t, s, "guest:person", tidelivekit.RoleGuest, nil)

	const identity = "bot:program"
	const ttl = 2 * time.Second
	token, err := testMinter().MintToken(identity, "Notes", botTestRoom, tidelivekit.RoleBot, ttl)
	if err != nil {
		t.Fatal(err)
	}
	bot := connect(t, s, identity, token, nil)
	time.Sleep(ttl + time.Second)

	if bot.ConnectionState() != lksdk.ConnectionStateConnected || participants(t, service)[identity] == nil {
		t.Fatalf("the bot's connection ended with its token: %v", bot.ConnectionState())
	}
}

type oneRoom struct{ room store.Room }

func (o oneRoom) RoomBySlug(context.Context, string) (store.Room, error) { return o.room, nil }

// A host removes a bot as they would anyone: moderation finds it among
// the participants, the media server removes it, and the token it still
// holds gets it nowhere, since tide removes it again on the rejoin.
func TestHostCanKickAnBot(t *testing.T) {
	s := startServer(t, testOptions(t))
	service := lksdk.NewRoomServiceClient(s.URL(), testKey, testSecret)
	moderator := moderation.NewHandler(
		oneRoom{store.Room{Slug: botTestRoom, OwnerSub: "owner"}},
		service,
		moderation.NewDenylist(lobby.TokenTTL),
	)

	const identity = "bot:program"
	token, err := testMinter().MintToken(identity, "Notes", botTestRoom, tidelivekit.RoleBot, lobby.TokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	joinAs(t, s, "host:owner:1234", tidelivekit.RoleHost, nil)
	kicked := make(chan lksdk.DisconnectionReason, 1)
	callback := lksdk.NewRoomCallback()
	callback.OnDisconnectedWithReason = func(reason lksdk.DisconnectionReason) {
		kicked <- reason
	}
	connect(t, s, identity, token, callback)
	eventually(t, 10*time.Second, "the bot in the room", func() bool {
		return participants(t, service)[identity] != nil
	})

	request := httptest.NewRequest(http.MethodPost, "/kick", nil)
	request.SetPathValue("slug", botTestRoom)
	request.SetPathValue("identity", identity)
	request = request.WithContext(auth.WithSession(request.Context(), auth.Session{Sub: "owner"}))
	response := httptest.NewRecorder()
	moderator.Kick(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("kick = %d %s", response.Code, response.Body)
	}
	select {
	case reason := <-kicked:
		if reason != lksdk.ParticipantRemoved {
			t.Fatalf("bot disconnected with %v", reason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the kick didn't reach the bot")
	}
	eventually(t, 10*time.Second, "the bot gone", func() bool {
		return participants(t, service)[identity] == nil
	})

	// The media server can't revoke the token, so this works, until the
	// participant_joined webhook reaches EnforceOnJoin, called here by hand.
	connect(t, s, identity, token, nil)
	eventually(t, 10*time.Second, "the bot's rejoin", func() bool {
		return participants(t, service)[identity] != nil
	})
	moderator.EnforceOnJoin(context.Background(), botTestRoom, identity)
	eventually(t, 10*time.Second, "the bot removed again", func() bool {
		return participants(t, service)[identity] == nil
	})
}

package moderation

import (
	"context"
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
	"tide/internal/lobby"
	"tide/internal/store"
)

type liveMeeting struct{ room *protocol.Room }

func (l liveMeeting) ListRooms(context.Context, *protocol.ListRoomsRequest) (*protocol.ListRoomsResponse, error) {
	return &protocol.ListRoomsResponse{Rooms: []*protocol.Room{l.room}}, nil
}

// A kicked guest must not use the lobby's rejoin grant to come back into
// the meeting unasked, for as long as the grant lives: not only while the
// denylist still covers their admission token, but long after it.
func TestKickedGuestCannotRejoinWithTheirGrant(t *testing.T) {
	const secret = "test-livekit-secret-with-enough-bytes"
	db, err := store.Open(filepath.Join(t.TempDir(), "tide.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	room := store.Room{ID: "on", Slug: "calm-otter-412", Name: "Lobby on", OwnerSub: "owner", LobbyEnabled: true, CreatedAt: 1}
	if err := db.CreateRoom(context.Background(), room); err != nil {
		t.Fatal(err)
	}
	registry := lobby.NewRegistry(time.Minute)
	lobbyHandler := lobby.NewHandler(db, registry, tidelivekit.NewMinter(config.Config{
		LiveKitAPIKey: "devkey", LiveKitAPISecret: secret, LiveKitPublicURL: "ws://public.example",
	}))
	denylist := NewKickDenylist()
	current := time.Now()
	denylist.now = func() time.Time { return current }
	lobbyHandler.EnableRejoin(liveMeeting{&protocol.Room{Name: room.Slug, Sid: "RM_meeting", NumParticipants: 2}}, denylist)
	owner := auth.Session{Sub: "owner"}

	join := func(grant string) api.JoinResponse {
		t.Helper()
		body, _ := json.Marshal(api.JoinRequest{Name: "Guest", Rejoin: grant})
		request := httptest.NewRequest(http.MethodPost, "/api/rooms/"+room.Slug+"/join", strings.NewReader(string(body)))
		request.SetPathValue("slug", room.Slug)
		recorder := httptest.NewRecorder()
		lobbyHandler.Join(recorder, request)
		var joined api.JoinResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &joined); err != nil {
			t.Fatalf("join = %d %s", recorder.Code, recorder.Body)
		}
		return joined
	}

	waiting := join("")
	approval := httptest.NewRequest(http.MethodPost, "/api/lobby/"+waiting.RequestID+"/approve", nil)
	approval.SetPathValue("id", waiting.RequestID)
	recorder := httptest.NewRecorder()
	lobbyHandler.Approve(recorder, approval.WithContext(auth.WithSession(approval.Context(), owner)))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("approve = %d %s", recorder.Code, recorder.Body)
	}
	admitted, _ := registry.Get(waiting.RequestID)
	verifier, err := protocolauth.ParseAPIToken(admitted.Token)
	if err != nil {
		t.Fatal(err)
	}
	_, claims, err := verifier.Verify(secret)
	if err != nil {
		t.Fatal(err)
	}
	if rejoined := join(admitted.Rejoin); rejoined.Status != string(lobby.StatusAdmitted) {
		t.Fatalf("rejoin before the kick = %+v, want admitted", rejoined)
	}

	service := &fakeRoomService{participants: []*protocol.ParticipantInfo{{Identity: claims.Identity}}}
	moderator := NewHandler(fakeRoomStore{room: room}, service, denylist)
	kick := httptest.NewRequest(http.MethodPost, "/api/rooms/"+room.Slug+"/participants/"+claims.Identity+"/kick", nil)
	kick.SetPathValue("slug", room.Slug)
	kick.SetPathValue("identity", claims.Identity)
	recorder = httptest.NewRecorder()
	moderator.Kick(recorder, kick.WithContext(auth.WithSession(kick.Context(), owner)))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("kick = %d %s", recorder.Code, recorder.Body)
	}

	for _, after := range []time.Duration{0, lobby.TokenTTL + time.Minute, lobby.RejoinTTL - time.Minute} {
		current = time.Now().Add(after)
		if rejoined := join(admitted.Rejoin); rejoined.Status != string(lobby.StatusWaiting) {
			t.Fatalf("%s after the kick, rejoin = %+v, want the lobby", after, rejoined)
		}
	}
}

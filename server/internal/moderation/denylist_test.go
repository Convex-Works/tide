package moderation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	protocol "github.com/livekit/protocol/livekit"

	"klisi/internal/auth"
	"klisi/internal/store"
)

func TestDenylistBanExpires(t *testing.T) {
	denylist := NewDenylist(10 * time.Minute)
	current := time.Unix(1_000_000, 0)
	denylist.now = func() time.Time { return current }

	denylist.Ban("calm-otter-412", "guest:1234")
	if !denylist.Banned("calm-otter-412", "guest:1234") {
		t.Fatal("identity should be banned immediately after Ban")
	}
	if denylist.Banned("calm-otter-412", "guest:9999") {
		t.Fatal("other identities must not be banned")
	}
	if denylist.Banned("other-room", "guest:1234") {
		t.Fatal("bans must be scoped to the room")
	}

	current = current.Add(10*time.Minute + time.Second)
	if denylist.Banned("calm-otter-412", "guest:1234") {
		t.Fatal("ban should expire after the TTL")
	}
	if len(denylist.entries) != 0 {
		t.Fatalf("expired entries should be pruned, have %d", len(denylist.entries))
	}
}

func TestKickBansIdentity(t *testing.T) {
	room := store.Room{Slug: "calm-otter-412", OwnerSub: "owner"}
	service := &fakeRoomService{participants: []*protocol.ParticipantInfo{{Identity: "guest:1234"}}}
	denylist := NewDenylist(10 * time.Minute)
	handler := NewHandler(fakeRoomStore{room: room}, service, denylist)

	request := kickRequest(room.Slug, "guest:1234", "owner")
	response := httptest.NewRecorder()
	handler.Kick(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", response.Code)
	}
	if !denylist.Banned(room.Slug, "guest:1234") {
		t.Fatal("kicked guest must be denylisted")
	}
}

func TestKickNeverBansOwner(t *testing.T) {
	room := store.Room{Slug: "calm-otter-412", OwnerSub: "owner"}
	service := &fakeRoomService{participants: []*protocol.ParticipantInfo{{Identity: "host:owner"}}}
	denylist := NewDenylist(10 * time.Minute)
	handler := NewHandler(fakeRoomStore{room: room}, service, denylist)

	request := kickRequest(room.Slug, "host:owner", "owner")
	handler.Kick(httptest.NewRecorder(), request)

	if denylist.Banned(room.Slug, "host:owner") {
		t.Fatal("the owner's own identity must never be banned")
	}
}

func TestEnforceOnJoinRemovesBannedIdentity(t *testing.T) {
	room := store.Room{Slug: "calm-otter-412", OwnerSub: "owner"}
	service := &fakeRoomService{}
	denylist := NewDenylist(10 * time.Minute)
	handler := NewHandler(fakeRoomStore{room: room}, service, denylist)

	handler.EnforceOnJoin(context.Background(), room.Slug, "guest:1234")
	if service.removed != nil {
		t.Fatal("unbanned identities must not be removed")
	}

	denylist.Ban(room.Slug, "guest:1234")
	handler.EnforceOnJoin(context.Background(), room.Slug, "guest:1234")
	if service.removed == nil {
		t.Fatal("banned identity should be re-removed on join")
	}
	if service.removed.Room != room.Slug || service.removed.Identity != "guest:1234" {
		t.Fatalf("unexpected removal target: %+v", service.removed)
	}
}

func kickRequest(slug, identity, sessionSub string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/rooms/"+slug+"/participants/"+identity+"/kick", nil)
	request.SetPathValue("slug", slug)
	request.SetPathValue("identity", identity)
	if sessionSub != "" {
		request = request.WithContext(auth.WithSession(request.Context(), auth.Session{Sub: sessionSub}))
	}
	return request
}

func TestIsOwnerIdentity(t *testing.T) {
	if !isOwnerIdentity("host:owner", "owner") || !isOwnerIdentity("host:owner:ab12", "owner") {
		t.Fatal("owner identities (with and without nonce) must match")
	}
	if isOwnerIdentity("host:owner2", "owner") || isOwnerIdentity("guest:owner", "owner") {
		t.Fatal("non-owner identities must not match")
	}
}

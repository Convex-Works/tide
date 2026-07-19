package rooms

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"klisi/internal/store"
)

func TestSlugFormat(t *testing.T) {
	slug, err := GenerateSlug()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[a-z]+-[a-z]+-[1-9][0-9]{2}$`).MatchString(slug) {
		t.Fatalf("GenerateSlug() = %q", slug)
	}
}

type collisionStore struct {
	calls int
	room  store.Room
}

func (s *collisionStore) CreateRoom(_ context.Context, room store.Room) error {
	s.calls++
	if s.calls == 1 {
		return errors.New("constraint failed: UNIQUE constraint failed: rooms.slug (2067)")
	}
	s.room = room
	return nil
}

func TestCreateRetriesSlugCollision(t *testing.T) {
	roomStore := &collisionStore{}
	service := NewService(roomStore)
	candidates := []string{"calm-otter-412", "quiet-fox-713"}
	service.generateSlug = func() (string, error) {
		next := candidates[0]
		candidates = candidates[1:]
		return next, nil
	}
	service.generateID = func() (string, error) { return "room-id", nil }
	service.now = func() time.Time { return time.Unix(123, 0) }

	room, err := service.Create(context.Background(), "Weekly", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if roomStore.calls != 2 || room.Slug != "quiet-fox-713" || roomStore.room != room {
		t.Fatalf("Create() = %#v after %d calls", room, roomStore.calls)
	}
}

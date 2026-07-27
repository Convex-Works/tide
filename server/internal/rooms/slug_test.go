package rooms

import (
	"bytes"
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
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(slug) {
		t.Fatalf("GenerateSlug() = %q", slug)
	}
}

func TestGenerateSlugSetsUUIDVersionAndVariant(t *testing.T) {
	slug, err := generateSlug(bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatal(err)
	}
	if slug != "00000000-0000-4000-8000-000000000000" {
		t.Fatalf("generateSlug() = %q", slug)
	}
}

func TestCustomSlugValidation(t *testing.T) {
	valid := []string{"abc", "team-weekly", "room-42", "00000000-0000-4000-8000-000000000000"}
	for _, slug := range valid {
		if err := validateSlug(slug); err != nil {
			t.Errorf("validateSlug(%q) = %v", slug, err)
		}
	}
	invalid := []string{"ab", "-team", "team-", "team--weekly", "team weekly", "team_weekly", "Team"}
	for _, slug := range invalid {
		if err := validateSlug(slug); err == nil {
			t.Errorf("validateSlug(%q) unexpectedly succeeded", slug)
		}
	}
	if got := normalizeSlug("  Team-Weekly "); got != "team-weekly" {
		t.Errorf("normalizeSlug() = %q", got)
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
	candidates := []string{
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000002",
	}
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
	if roomStore.calls != 2 ||
		room.Slug != "00000000-0000-4000-8000-000000000002" ||
		roomStore.room != room {
		t.Fatalf("Create() = %#v after %d calls", room, roomStore.calls)
	}
}

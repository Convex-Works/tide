package rooms

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"klisi/internal/store"
)

const slugAttempts = 32

type roomCreator interface {
	CreateRoom(context.Context, store.Room) error
}

type Service struct {
	store        roomCreator
	generateSlug func() (string, error)
	generateID   func() (string, error)
	now          func() time.Time
}

func NewService(roomStore roomCreator) *Service {
	return &Service{
		store: roomStore, generateSlug: GenerateSlug, generateID: randomID, now: time.Now,
	}
}

// ErrSlugTaken is Create's answer when the slug it was given belongs to
// another room.
var ErrSlugTaken = errors.New("room slug is taken")

// Create creates a room owned by ownerSub (ARCHITECTURE.md §5). slug, if
// not empty, must be valid already, and a room that has it makes Create
// return ErrSlugTaken; an empty slug is generated, again on each collision.
// An empty name becomes the room's slug.
func (s *Service) Create(ctx context.Context, name, slug, ownerSub string) (store.Room, error) {
	id, err := s.generateID()
	if err != nil {
		return store.Room{}, fmt.Errorf("generate room id: %w", err)
	}
	given := slug != ""
	attempts := slugAttempts
	if given {
		attempts = 1
	}
	for range attempts {
		if !given {
			if slug, err = s.generateSlug(); err != nil {
				return store.Room{}, fmt.Errorf("generate room slug: %w", err)
			}
		}
		room := store.Room{
			ID: id, Slug: slug, Name: name, OwnerSub: ownerSub,
			LobbyEnabled: true, CreatedAt: s.now().Unix(),
		}
		if room.Name == "" {
			room.Name = slug
		}
		if err := s.store.CreateRoom(ctx, room); err != nil {
			if store.IsSlugConflict(err) {
				if given {
					return store.Room{}, ErrSlugTaken
				}
				continue
			}
			return store.Room{}, err
		}
		return room, nil
	}
	return store.Room{}, errors.New("could not allocate a unique room slug")
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

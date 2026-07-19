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

func (s *Service) Create(ctx context.Context, name, ownerSub string) (store.Room, error) {
	id, err := s.generateID()
	if err != nil {
		return store.Room{}, fmt.Errorf("generate room id: %w", err)
	}
	for range slugAttempts {
		slug, err := s.generateSlug()
		if err != nil {
			return store.Room{}, fmt.Errorf("generate room slug: %w", err)
		}
		room := store.Room{
			ID: id, Slug: slug, Name: name, OwnerSub: ownerSub,
			LobbyEnabled: true, CreatedAt: s.now().Unix(),
		}
		if err := s.store.CreateRoom(ctx, room); err != nil {
			if store.IsSlugConflict(err) {
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

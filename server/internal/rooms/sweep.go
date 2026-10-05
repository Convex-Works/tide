package rooms

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"tide/internal/store"
)

// An anonymous deployment's rooms live in memory until nobody has used them
// for a day (ARCHITECTURE.md §4.1); the sweep looks every five minutes.
const (
	AnonymousRoomIdle = 24 * time.Hour
	SweepInterval     = 5 * time.Minute
)

// A Sweeper deletes the rooms nobody has used for a while: those created, and
// last joined, more than its idle time ago, unless a meeting is live in
// them.
type Sweeper struct {
	store *store.Store
	live  LiveRoomSource
	ender MeetingEnder
	idle  time.Duration
	now   func() time.Time
}

// NewSweeper makes a sweeper of the rooms in roomStore, which asks live
// which rooms have a meeting in them, and ender (if set) to end any meeting
// that started in a room as it was deleted.
func NewSweeper(roomStore *store.Store, live LiveRoomSource, ender MeetingEnder, idle time.Duration) *Sweeper {
	return &Sweeper{store: roomStore, live: live, ender: ender, idle: idle, now: time.Now}
}

// SetClock replaces the sweeper's clock, for tests that move time on.
func (s *Sweeper) SetClock(now func() time.Time) {
	s.now = now
}

// Run sweeps at once, then every interval, until ctx is done.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if deleted, err := s.Sweep(ctx); err != nil {
			log.Printf("rooms: sweep: %v", err)
		} else if len(deleted) > 0 {
			log.Printf("rooms: deleted %d rooms nobody used for %s", len(deleted), s.idle)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sweep deletes the idle rooms once, and returns their slugs. When it can't
// tell which rooms are live it deletes none: a room in use is never swept on
// a guess. A room is deleted only if it is still idle as it is deleted, so
// one joined after the list (every token tide mints touches its room first)
// stays; and the meeting in a deleted room, should one have started between
// the two, is ended.
func (s *Sweeper) Sweep(ctx context.Context) ([]string, error) {
	now := s.now()
	cutoff := now.Add(-s.idle).Unix()
	idle, err := s.store.IdleRooms(ctx, cutoff)
	if err != nil || len(idle) == 0 {
		return nil, err
	}
	live, err := s.live.ActiveRooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("live rooms unknown, so none deleted: %w", err)
	}
	var deleted []string
	for _, room := range idle {
		if live[room.Slug].NumParticipants > 0 {
			continue
		}
		// An anonymous room has no recordings, so no files to queue; a
		// signed-in one is never swept.
		if _, err := s.store.DeleteIdleRoom(ctx, room.ID, cutoff, now.Unix()); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue // joined, or deleted by its owner, since the list
			}
			return deleted, fmt.Errorf("delete %s: %w", room.Slug, err)
		}
		endMeeting(ctx, s.ender, room.Slug)
		deleted = append(deleted, room.Slug)
	}
	return deleted, nil
}

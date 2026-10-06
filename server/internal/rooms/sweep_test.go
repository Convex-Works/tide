package rooms

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"

	"tide/internal/store"
)

func sweepStore(t *testing.T, now time.Time) *store.Store {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	hour := int64(time.Hour / time.Second)
	at := now.Unix()
	for _, room := range []struct {
		slug      string
		created   int64
		lastJoint int64 // 0: never joined
	}{
		{"unused-for-two-days", at - 48*hour, 0},
		{"joined-two-days-ago", at - 72*hour, at - 48*hour},
		{"live-since-yesterday", at - 72*hour, at - 30*hour},
		{"joined-an-hour-ago", at - 72*hour, at - hour},
		{"made-an-hour-ago", at - hour, 0},
		{"made-just-under-a-day-ago", at - 23*hour, 0},
	} {
		if err := db.CreateRoom(ctx, store.Room{
			ID: room.slug, Slug: room.slug, Name: room.slug, OwnerSub: "anon:owner", CreatedAt: room.created,
		}); err != nil {
			t.Fatal(err)
		}
		if room.lastJoint != 0 {
			if err := db.TouchRoomActive(ctx, room.slug, room.lastJoint); err != nil {
				t.Fatal(err)
			}
		}
	}
	return db
}

func remaining(t *testing.T, db *store.Store) []string {
	t.Helper()
	rooms, err := db.Rooms(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, room := range rooms {
		slugs = append(slugs, room.Slug)
	}
	slices.Sort(slugs)
	return slugs
}

func TestSweepDeletesRoomsUnusedForADayUnlessLive(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	db := sweepStore(t, now)
	live := &fakeLiveSource{rooms: map[string]LiveRoom{
		"live-since-yesterday": {NumParticipants: 3},
		// An empty room the media server still holds is not a meeting.
		"unused-for-two-days": {NumParticipants: 0},
	}}
	sweeper := NewSweeper(db, live, nil, AnonymousRoomIdle)
	sweeper.SetClock(func() time.Time { return now })

	deleted, err := sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(deleted)
	if want := []string{"joined-two-days-ago", "unused-for-two-days"}; !slices.Equal(deleted, want) {
		t.Fatalf("deleted %q, want %q", deleted, want)
	}
	if got, want := remaining(t, db), []string{
		"joined-an-hour-ago", "live-since-yesterday", "made-an-hour-ago", "made-just-under-a-day-ago",
	}; !slices.Equal(got, want) {
		t.Fatalf("remaining %q, want %q", got, want)
	}

	// Two hours on, the day is up for the one made just under a day ago;
	// the meeting has ended in the room that was live.
	now = now.Add(2 * time.Hour)
	live.rooms = nil
	deleted, err = sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(deleted)
	if want := []string{"live-since-yesterday", "made-just-under-a-day-ago"}; !slices.Equal(deleted, want) {
		t.Fatalf("deleted %q, want %q", deleted, want)
	}
	if _, err := db.RoomBySlug(context.Background(), "made-just-under-a-day-ago"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("swept room lookup = %v", err)
	}
}

// Without knowing which rooms are live, the sweep deletes nothing: a room in
// use is never swept on a guess.
func TestSweepDeletesNothingWhenLiveRoomsAreUnknown(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	db := sweepStore(t, now)
	sweeper := NewSweeper(db, &fakeLiveSource{err: errors.New("media server unavailable")}, nil, AnonymousRoomIdle)
	sweeper.SetClock(func() time.Time { return now })
	before := remaining(t, db)
	if deleted, err := sweeper.Sweep(context.Background()); err == nil || len(deleted) != 0 {
		t.Fatalf("Sweep = %q, %v; want an error and nothing deleted", deleted, err)
	}
	if after := remaining(t, db); !slices.Equal(after, before) {
		t.Fatalf("rooms %q became %q", before, after)
	}
}

func TestSweeperRunsAtOnceAndStopsWithItsContext(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	db := sweepStore(t, now)
	sweeper := NewSweeper(db, &fakeLiveSource{}, nil, AnonymousRoomIdle)
	sweeper.SetClock(func() time.Time { return now })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sweeper.Run(ctx, time.Hour)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(remaining(t, db)) == 6 {
		if time.Now().After(deadline) {
			t.Fatal("Run never swept")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't stop with its context")
	}
}

// touchingLiveSource is a live source that, as the sweep asks it which
// rooms are live, lets someone join a room: the moment between the sweep
// listing a room idle and deleting it.
type touchingLiveSource struct {
	db   *store.Store
	slug string
	at   int64
}

func (s *touchingLiveSource) ActiveRooms(ctx context.Context) (map[string]LiveRoom, error) {
	return nil, s.db.TouchRoomActive(ctx, s.slug, s.at)
}

// fakeEnder records the meetings it ends.
type fakeEnder struct {
	ended []string
	err   error
}

func (f *fakeEnder) EndMeeting(_ context.Context, slug string) error {
	f.ended = append(f.ended, slug)
	return f.err
}

// A room joined after the sweep listed it idle stays: the delete checks
// again (ARCHITECTURE.md §5). The meetings in the rooms it does delete are
// ended, even when ending one fails.
func TestSweepSparesARoomJoinedAsItSweeps(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	db := sweepStore(t, now)
	ender := &fakeEnder{err: errors.New("media server unavailable")}
	sweeper := NewSweeper(db, &touchingLiveSource{db: db, slug: "unused-for-two-days", at: now.Unix()}, ender, AnonymousRoomIdle)
	sweeper.SetClock(func() time.Time { return now })

	deleted, err := sweeper.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(deleted)
	if want := []string{"joined-two-days-ago", "live-since-yesterday"}; !slices.Equal(deleted, want) {
		t.Fatalf("deleted %q, want %q", deleted, want)
	}
	if _, err := db.RoomBySlug(context.Background(), "unused-for-two-days"); err != nil {
		t.Fatalf("the room joined during the sweep: %v", err)
	}
	slices.Sort(ender.ended)
	if !slices.Equal(ender.ended, deleted) {
		t.Fatalf("ended meetings in %q, want in every deleted room %q", ender.ended, deleted)
	}
}

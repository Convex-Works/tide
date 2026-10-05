package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// Anonymous mode keeps its rooms in ":memory:" (ARCHITECTURE.md §4.1): the
// same schema and code on one connection that must never be recycled, since
// a new connection would be a new, empty database.
func TestMemoryStoreIsWholeAndPrivate(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	room := Room{ID: "room-1", Slug: "abc-defg-hij", Name: "Standup", OwnerSub: "anon:owner", LobbyEnabled: true, CreatedAt: 100}
	if err := db.CreateRoom(ctx, room); err != nil {
		t.Fatal(err)
	}
	if err := db.TouchRoomActive(ctx, room.Slug, 200); err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeSession(ctx, "sid-1", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	// Many queries later, under concurrency, it is still the same database.
	errs := make(chan error, 20)
	for range 20 {
		go func() {
			got, err := db.RoomBySlug(ctx, room.Slug)
			if err == nil && (got.LastActiveAt == nil || *got.LastActiveAt != 200) {
				err = errors.New("the room lost its activity")
			}
			errs <- err
		}()
	}
	for range 20 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if revoked, err := db.IsSessionRevoked(ctx, "sid-1"); err != nil || !revoked {
		t.Fatalf("IsSessionRevoked = %v, %v", revoked, err)
	}
	if _, err := db.DeleteRoom(ctx, room.ID, 300); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RoomBySlug(ctx, room.Slug); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted room = %v", err)
	}

	// Another store on ":memory:" is another database.
	other, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	if err := other.CreateRoom(ctx, Room{ID: "x", Slug: "xyz-wxyz-xyz", Name: "x", OwnerSub: "o", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RoomBySlug(ctx, "xyz-wxyz-xyz"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("one memory store sees another's room: %v", err)
	}
}

func TestIdleRoomsCountsTheLaterOfCreationAndLastJoin(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, room := range []Room{
		{ID: "old-unused", Slug: "old-unused", CreatedAt: 10},
		{ID: "old-joined-lately", Slug: "old-joined-lately", CreatedAt: 10},
		{ID: "old-joined-long-ago", Slug: "old-joined-long-ago", CreatedAt: 20},
		{ID: "new-unused", Slug: "new-unused", CreatedAt: 100},
	} {
		room.Name, room.OwnerSub = room.Slug, "anon:owner"
		if err := db.CreateRoom(ctx, room); err != nil {
			t.Fatal(err)
		}
	}
	for slug, at := range map[string]int64{"old-joined-lately": 150, "old-joined-long-ago": 30} {
		if err := db.TouchRoomActive(ctx, slug, at); err != nil {
			t.Fatal(err)
		}
	}
	idle, err := db.IdleRooms(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, room := range idle {
		slugs = append(slugs, room.Slug)
	}
	if len(slugs) != 2 || slugs[0] != "old-unused" || slugs[1] != "old-joined-long-ago" {
		t.Fatalf("IdleRooms(100) = %q, want the two unused since before 100, oldest first", slugs)
	}
}

// DeleteIdleRoom checks idleness as it deletes: a room used since the cutoff
// stays, with nothing of its recordings queued, and one still idle goes.
func TestDeleteIdleRoomChecksAgainAsItDeletes(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	room := Room{ID: "room", Slug: "room", Name: "Room", OwnerSub: "anon:owner", CreatedAt: 10}
	if err := db.CreateRoom(ctx, room); err != nil {
		t.Fatal(err)
	}
	key := "recordings/room/rec/old.ogg"
	if err := db.InsertRecording(ctx, Recording{
		ID: "rec", RoomID: room.ID, RoomSlug: room.Slug, EgressID: "egress", Status: "completed",
		StartedBy: "anon:owner", StartedAt: 10, S3Key: &key,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.TouchRoomActive(ctx, room.Slug, 200); err != nil {
		t.Fatal(err)
	}
	if count, err := db.CountRooms(ctx); err != nil || count != 1 {
		t.Fatalf("CountRooms = %d, %v", count, err)
	}
	if _, err := db.DeleteIdleRoom(ctx, room.ID, 100, 300); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleting a room used since the cutoff = %v, want sql.ErrNoRows", err)
	}
	if due, err := db.DueRemovals(ctx, 1<<40, 10); err != nil || len(due) != 0 {
		t.Fatalf("a room that stayed queued %q (%v)", due, err)
	}
	keys, err := db.DeleteIdleRoom(ctx, room.ID, 250, 300)
	if err != nil || len(keys) == 0 {
		t.Fatalf("deleting a room idle since the cutoff = %q, %v", keys, err)
	}
	if count, err := db.CountRooms(ctx); err != nil || count != 0 {
		t.Fatalf("CountRooms after = %d, %v", count, err)
	}
	if _, err := db.DeleteIdleRoom(ctx, room.ID, 0, 300); err == nil {
		t.Fatal("DeleteIdleRoom without a cutoff deleted unconditionally")
	}
}

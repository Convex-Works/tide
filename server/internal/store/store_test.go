package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestRoomCRUD(t *testing.T) {
	db, err := Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	want := Room{ID: "room-1", Slug: "calm-otter-412", Name: "Weekly", OwnerSub: "owner", LobbyEnabled: true, CreatedAt: 123}
	if err := db.CreateRoom(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := db.RoomBySlug(ctx, want.Slug)
	if err != nil || got != want {
		t.Fatalf("RoomBySlug() = %#v, %v; want %#v", got, err, want)
	}
	rooms, err := db.RoomsByOwner(ctx, "owner")
	if err != nil || len(rooms) != 1 || rooms[0] != want {
		t.Fatalf("RoomsByOwner() = %#v, %v", rooms, err)
	}

	want.Name = "Renamed"
	oldSlug := want.Slug
	want.Slug = "weekly-team"
	want.LobbyEnabled = false
	if err := db.UpdateRoom(ctx, want, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	got, err = db.RoomBySlug(ctx, want.Slug)
	if err != nil || got != want {
		t.Fatalf("updated room = %#v, %v; want %#v", got, err, want)
	}
	if _, err := db.RoomBySlug(ctx, oldSlug); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old slug should be unavailable, err = %v", err)
	}

	if keys, err := db.DeleteRoom(ctx, want.ID, 1); err != nil || len(keys) != 0 {
		t.Fatalf("DeleteRoom() = %q, %v", keys, err)
	}
	if _, err := db.RoomBySlug(ctx, want.Slug); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("RoomBySlug after delete error = %v", err)
	}
}

func TestRoomsReturnsEveryOwner(t *testing.T) {
	db, err := Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	for _, room := range []Room{
		{ID: "room-1", Slug: "one", Name: "One", OwnerSub: "owner-1", CreatedAt: 1},
		{ID: "room-2", Slug: "two", Name: "Two", OwnerSub: "owner-2", CreatedAt: 2},
	} {
		if err := db.CreateRoom(ctx, room); err != nil {
			t.Fatal(err)
		}
	}
	rooms, err := db.Rooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 2 || rooms[0].ID != "room-2" || rooms[1].ID != "room-1" {
		t.Fatalf("Rooms() = %#v", rooms)
	}
}

func TestRecordingCRUD(t *testing.T) {
	db, err := Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	room := Room{ID: "room-1", Slug: "calm-otter-412", Name: "Weekly", OwnerSub: "owner", LobbyEnabled: true, CreatedAt: 1}
	if err := db.CreateRoom(ctx, room); err != nil {
		t.Fatal(err)
	}
	recording := Recording{
		ID: "rec-1", RoomID: room.ID, RoomSlug: room.Slug, EgressID: "egress-1",
		Status: "starting", StartedBy: "owner", StartedAt: 100,
	}
	if err := db.InsertRecording(ctx, recording); err != nil {
		t.Fatal(err)
	}
	active, err := db.ActiveRecordingByRoomID(ctx, room.ID)
	if err != nil || active.ID != recording.ID {
		t.Fatalf("active recording = %#v, %v", active, err)
	}
	if err := db.InsertRecording(ctx, Recording{
		ID: "rec-2", RoomID: room.ID, RoomSlug: room.Slug, EgressID: "egress-2",
		Status: "recording", StartedBy: "owner", StartedAt: 101,
	}); !IsActiveRecordingConflict(err) {
		t.Fatalf("second active recording error = %v", err)
	}
	endedAt, duration, size := int64(110), int64(10), int64(12345)
	key := "recordings/calm-otter-412/100.mp4"
	if err := db.UpdateRecordingByEgress(ctx, recording.EgressID, RecordingUpdate{
		Status: "completed", EndedAt: &endedAt, DurationS: &duration,
		S3Key: &key, SizeBytes: &size,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := db.RecordingByID(ctx, recording.ID)
	if err != nil || got.Status != "completed" || got.DurationS == nil || *got.DurationS != duration {
		t.Fatalf("updated recording = %#v, %v", got, err)
	}
	list, err := db.RecordingsByRoomSlug(ctx, room.Slug)
	if err != nil || len(list) != 1 || list[0].ID != recording.ID {
		t.Fatalf("recording list = %#v, %v", list, err)
	}
	keys, err := db.DeleteRecording(ctx, recording.ID, 1)
	if want := []string{key, "recordings/calm-otter-412/100.txt", "recordings/calm-otter-412/100.vtt"}; err != nil || !slices.Equal(keys, want) {
		t.Fatalf("DeleteRecording() = %q, %v; want %q", keys, err, want)
	}
	if _, err := db.RecordingByID(ctx, recording.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("RecordingByID after delete error = %v", err)
	}
}

func TestRecordingStatusTransitionsAreMonotonic(t *testing.T) {
	db, err := Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	room := Room{ID: "room-1", Slug: "calm-otter-412", Name: "Weekly", OwnerSub: "owner", LobbyEnabled: true, CreatedAt: 1}
	if err := db.CreateRoom(ctx, room); err != nil {
		t.Fatal(err)
	}
	recording := Recording{
		ID: "rec-1", RoomID: room.ID, RoomSlug: room.Slug, EgressID: "egress-1",
		Status: "recording", StartedBy: "owner", StartedAt: 100,
	}
	if err := db.InsertRecording(ctx, recording); err != nil {
		t.Fatal(err)
	}

	// Forward transitions apply.
	if err := db.UpdateRecordingByEgress(ctx, "egress-1", RecordingUpdate{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	// A stale lower-ranked write (the Stop/egress_ended race) is a silent no-op.
	if err := db.UpdateRecordingByEgress(ctx, "egress-1", RecordingUpdate{Status: "finalizing"}); err != nil {
		t.Fatalf("stale downgrade should be a no-op, got %v", err)
	}
	// Terminal states can never be overwritten, even by another terminal.
	if err := db.UpdateRecordingByEgress(ctx, "egress-1", RecordingUpdate{Status: "failed"}); err != nil {
		t.Fatalf("terminal overwrite should be a no-op, got %v", err)
	}
	got, err := db.RecordingByID(ctx, recording.ID)
	if err != nil || got.Status != "completed" {
		t.Fatalf("recording = %#v, %v", got, err)
	}
	// Unknown egress IDs still surface an error.
	if err := db.UpdateRecordingByEgress(ctx, "egress-missing", RecordingUpdate{Status: "recording"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown egress error = %v", err)
	}

	// Same-status updates still apply (metadata merges from egress_updated).
	if err := db.InsertRecording(ctx, Recording{
		ID: "rec-2", RoomID: room.ID, RoomSlug: room.Slug, EgressID: "egress-2",
		Status: "recording", StartedBy: "owner", StartedAt: 200,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateRecordingByEgress(ctx, "egress-2", RecordingUpdate{Status: "recording"}); err != nil {
		t.Fatalf("same-status update should apply, got %v", err)
	}
	active, err := db.ListActiveRecordings(ctx)
	if err != nil || len(active) != 1 || active[0].ID != "rec-2" {
		t.Fatalf("active recordings = %#v, %v", active, err)
	}
}

func TestSessionRevocationStore(t *testing.T) {
	db, err := Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	future := time.Now().Add(time.Hour).Unix()

	if revoked, err := db.IsSessionRevoked(ctx, "sid-1"); err != nil || revoked {
		t.Fatalf("fresh sid revoked=%v err=%v", revoked, err)
	}
	if err := db.RevokeSession(ctx, "sid-1", future); err != nil {
		t.Fatal(err)
	}
	if revoked, err := db.IsSessionRevoked(ctx, "sid-1"); err != nil || !revoked {
		t.Fatalf("revoked sid revoked=%v err=%v", revoked, err)
	}
	// Rows past their expiry no longer count and are pruned on next revoke.
	if err := db.RevokeSession(ctx, "sid-expired", time.Now().Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if revoked, err := db.IsSessionRevoked(ctx, "sid-expired"); err != nil || revoked {
		t.Fatalf("expired sid revoked=%v err=%v", revoked, err)
	}
}

// A slug freed by deleting or renaming its room stays unavailable for as
// long as a token minted for the old room could still be used
// (ARCHITECTURE.md §5), then comes free.
func TestFreedSlugsAreHeld(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const now = int64(1_800_000_000)
	hold := int64(FreedSlugHold / time.Second)
	room := func(id, slug string, at int64) Room {
		return Room{ID: id, Slug: slug, Name: slug, OwnerSub: "alice", LobbyEnabled: true, CreatedAt: at}
	}
	if err := db.CreateRoom(ctx, room("r1", "standup", now)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DeleteRoom(ctx, "r1", now); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRoom(ctx, room("r2", "standup", now+hold-1)); !IsSlugConflict(err) {
		t.Fatalf("creating a just-deleted room's slug = %v, want a slug conflict", err)
	}
	if err := db.CreateRoom(ctx, room("r2", "standup", now+hold)); err != nil {
		t.Fatalf("the slug once its hold ended: %v", err)
	}

	// Renaming frees the old slug the same way, and can't take a held one.
	if err := db.CreateRoom(ctx, room("r3", "retro", now+hold)); err != nil {
		t.Fatal(err)
	}
	renamed := room("r3", "retro-2", now+hold)
	if err := db.UpdateRoom(ctx, renamed, now+hold); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRoom(ctx, room("r4", "retro", now+hold+1)); !IsSlugConflict(err) {
		t.Fatalf("creating a just-renamed room's old slug = %v, want a slug conflict", err)
	}
	other := room("r2", "retro", now+hold+1)
	if err := db.UpdateRoom(ctx, other, now+hold+1); !IsSlugConflict(err) {
		t.Fatalf("renaming another room to a held slug = %v, want a slug conflict", err)
	}
	// Saving a room without changing its slug holds nothing.
	renamed.Name = "Retro"
	if err := db.UpdateRoom(ctx, renamed, now+hold+2); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRoom(ctx, room("r5", "retro-3", now+hold+2)); err != nil {
		t.Fatal(err)
	}
}

// Marking a room active says whether it still exists, even when it was
// marked in the same second already.
func TestMarkRoomActiveFindsOnlyRoomsThatExist(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateRoom(ctx, Room{ID: "r1", Slug: "standup", Name: "Standup", OwnerSub: "alice", CreatedAt: 100}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if found, err := db.MarkRoomActive(ctx, "r1", 200); err != nil || !found {
			t.Fatalf("mark an existing room = %v, %v", found, err)
		}
	}
	got, err := db.RoomBySlug(ctx, "standup")
	if err != nil || got.LastActiveAt == nil || *got.LastActiveAt != 200 {
		t.Fatalf("last active = %v, %v", got.LastActiveAt, err)
	}
	if found, err := db.MarkRoomActive(ctx, "r1", 150); err != nil || !found {
		t.Fatalf("an older mark = %v, %v", found, err)
	}
	if got, _ := db.RoomBySlug(ctx, "standup"); *got.LastActiveAt != 200 {
		t.Fatalf("an older mark moved last active back to %d", *got.LastActiveAt)
	}
	if _, err := db.DeleteRoom(ctx, "r1", 300); err != nil {
		t.Fatal(err)
	}
	if found, err := db.MarkRoomActive(ctx, "r1", 300); err != nil || found {
		t.Fatalf("mark a deleted room = %v, %v, want not found", found, err)
	}
}

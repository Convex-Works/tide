package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
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
	want.LobbyEnabled = false
	if err := db.UpdateRoom(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err = db.RoomBySlug(ctx, want.Slug)
	if err != nil || got != want {
		t.Fatalf("updated room = %#v, %v; want %#v", got, err, want)
	}

	if err := db.DeleteRoom(ctx, want.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RoomBySlug(ctx, want.Slug); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("RoomBySlug after delete error = %v", err)
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
	if err := db.DeleteRecording(ctx, recording.ID); err != nil {
		t.Fatal(err)
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

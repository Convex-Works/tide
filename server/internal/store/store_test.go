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

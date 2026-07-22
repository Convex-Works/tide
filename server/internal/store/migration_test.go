package store

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// TestOpenMigratesLegacyRoomsTable proves Open() adds last_active_at to a rooms
// table created before the column existed (the real dev DB's situation), and
// that TouchRoomActive then persists and advances monotonically.
func TestOpenMigratesLegacyRoomsTable(t *testing.T) {
	path := t.TempDir() + "/legacy.db"

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE rooms (
		id TEXT PRIMARY KEY,
		slug TEXT UNIQUE NOT NULL,
		name TEXT NOT NULL,
		owner_sub TEXT NOT NULL,
		lobby_enabled INTEGER NOT NULL DEFAULT 1,
		created_at INTEGER NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(
		`INSERT INTO rooms (id, slug, name, owner_sub, lobby_enabled, created_at)
		 VALUES ('r1','legacy-slug','Legacy','owner',1,100)`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	// Open must run the ensureColumn migration without error.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrate: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	room, err := s.RoomBySlug(ctx, "legacy-slug")
	if err != nil {
		t.Fatal(err)
	}
	// The one-time backfill seeds last_active_at from created_at (100) so
	// pre-existing rooms don't all read "new".
	if room.LastActiveAt == nil || *room.LastActiveAt != 100 {
		t.Fatalf("expected backfilled last_active_at=100, got %v", room.LastActiveAt)
	}

	if err := s.TouchRoomActive(ctx, "legacy-slug", 12_345); err != nil {
		t.Fatal(err)
	}
	room, err = s.RoomBySlug(ctx, "legacy-slug")
	if err != nil {
		t.Fatal(err)
	}
	if room.LastActiveAt == nil || *room.LastActiveAt != 12_345 {
		t.Fatalf("touch not persisted: %v", room.LastActiveAt)
	}

	// An older timestamp must not move it backwards.
	if err := s.TouchRoomActive(ctx, "legacy-slug", 999); err != nil {
		t.Fatal(err)
	}
	room, _ = s.RoomBySlug(ctx, "legacy-slug")
	if room.LastActiveAt == nil || *room.LastActiveAt != 12_345 {
		t.Fatalf("monotonic guard failed: %v", room.LastActiveAt)
	}
}

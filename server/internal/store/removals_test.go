package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
)

// The removal queue hands out each key once it's due, never postpones one,
// and lets go of a key only once it was due.
func TestRemovalQueue(t *testing.T) {
	ctx := context.Background()
	db := transcriptsTestStore(t)
	if err := db.QueueRemovals(ctx, []string{"b", "a"}, 100); err != nil {
		t.Fatal(err)
	}
	if err := db.QueueRemovals(ctx, []string{"staging"}, 500); err != nil {
		t.Fatal(err)
	}
	due := func(now int64) []string {
		t.Helper()
		keys, err := db.DueRemovals(ctx, now, 10)
		if err != nil {
			t.Fatal(err)
		}
		return keys
	}
	if keys := due(99); len(keys) != 0 {
		t.Fatalf("due before their time: %q", keys)
	}
	if keys := due(100); !slices.Equal(keys, []string{"a", "b"}) {
		t.Fatalf("due at 100: %q", keys)
	}
	// Queued again, a key keeps the earlier time.
	if err := db.QueueRemovals(ctx, []string{"a"}, 900); err != nil {
		t.Fatal(err)
	}
	if err := db.QueueRemovals(ctx, []string{"staging"}, 200); err != nil {
		t.Fatal(err)
	}
	if keys := due(200); !slices.Equal(keys, []string{"a", "b", "staging"}) {
		t.Fatalf("due at 200: %q", keys)
	}
	if keys, err := db.DueRemovals(ctx, 200, 2); err != nil || !slices.Equal(keys, []string{"a", "b"}) {
		t.Fatalf("the first two due: %q, %v", keys, err)
	}
	// A key isn't done before it was due: it may still be written.
	if err := db.RemovalDone(ctx, "staging", 199); err != nil {
		t.Fatal(err)
	}
	if err := db.RemovalDone(ctx, "a", 200); err != nil {
		t.Fatal(err)
	}
	if keys := due(1000); !slices.Equal(keys, []string{"b", "staging"}) {
		t.Fatalf("left after removing a: %q", keys)
	}
}

// Deleting a recording or a room queues every file of every recording it
// deletes, in the transaction that deletes the rows: never one without the
// other.
func TestDeletionsQueueTheirFiles(t *testing.T) {
	ctx := context.Background()
	db := transcriptsTestStore(t)
	alice := Room{ID: "room-a", Slug: "a", Name: "Standup", OwnerSub: "alice", CreatedAt: 1}
	bob := Room{ID: "room-b", Slug: "b", Name: "Retro", OwnerSub: "bob", CreatedAt: 1}
	for _, room := range []Room{alice, bob} {
		if err := db.CreateRoom(ctx, room); err != nil {
			t.Fatal(err)
		}
	}
	addRecording(t, db, alice, "one", "completed", int64p(10), stringp("recordings/a/one/x.ogg"))
	addRecording(t, db, alice, "two", "failed", int64p(10), stringp("recordings/a/two/y.mp4"))
	addRecording(t, db, alice, "no-file", "failed", int64p(10), nil)
	addRecording(t, db, bob, "bobs", "completed", int64p(10), stringp("recordings/b/bobs/z.ogg"))
	if ok, err := db.RequestTranscript(ctx, "one", 5); err != nil || !ok {
		t.Fatalf("request: %t, %v", ok, err)
	}
	queued := func() []string {
		t.Helper()
		keys, err := db.DueRemovals(ctx, 1<<40, 100)
		if err != nil {
			t.Fatal(err)
		}
		return keys
	}

	// While the database refuses to delete, nothing is queued.
	if _, err := db.db.Exec(`CREATE TRIGGER keep_recordings BEFORE DELETE ON recordings
		BEGIN SELECT RAISE(ABORT, 'deleting recordings is refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DeleteRecording(ctx, "one", 50); err == nil {
		t.Fatal("DeleteRecording succeeded against the trigger")
	}
	if _, err := db.DeleteRoom(ctx, alice.ID, 50); err == nil {
		t.Fatal("DeleteRoom succeeded against the trigger")
	}
	if keys := queued(); len(keys) != 0 {
		t.Fatalf("queued by failed deletions: %q", keys)
	}
	if _, err := db.RecordingByID(ctx, "one"); err != nil {
		t.Fatalf("the recording after a failed deletion: %v", err)
	}
	if _, err := db.db.Exec(`DROP TRIGGER keep_recordings`); err != nil {
		t.Fatal(err)
	}

	keys, err := db.DeleteRecording(ctx, "one", 50)
	want := []string{"recordings/a/one/x.ogg", "recordings/a/one/x.txt", "recordings/a/one/x.vtt"}
	if err != nil || !slices.Equal(keys, want) || !slices.Equal(queued(), want) {
		t.Fatalf("DeleteRecording() = %q, %v; queued %q; want %q", keys, err, queued(), want)
	}
	if _, err := db.Transcript(ctx, "one"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("the deleted recording's transcript: %v", err)
	}
	if _, err := db.DeleteRecording(ctx, "one", 60); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleting it again: %v", err)
	}

	keys, err = db.DeleteRoom(ctx, alice.ID, 60)
	roomKeys := []string{"recordings/a/two/y.mp4", "recordings/a/two/y.txt", "recordings/a/two/y.vtt"}
	if err != nil || !slices.Equal(keys, roomKeys) {
		t.Fatalf("DeleteRoom() = %q, %v", keys, err)
	}
	if got := queued(); !slices.Equal(got, append(want, roomKeys...)) {
		t.Fatalf("queued after deleting the room: %q", got)
	}
	for _, id := range []string{"two", "no-file"} {
		if _, err := db.RecordingByID(ctx, id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("recording %s of the deleted room: %v", id, err)
		}
	}
	if _, err := db.RecordingByID(ctx, "bobs"); err != nil {
		t.Fatalf("another room's recording: %v", err)
	}
	if _, err := db.DeleteRoom(ctx, alice.ID, 70); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleting the room again: %v", err)
	}
}

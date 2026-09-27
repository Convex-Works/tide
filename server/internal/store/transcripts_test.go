package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"
)

func transcriptsTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "klisi.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func addRecording(t *testing.T, db *Store, room Room, id, status string, endedAt *int64, key *string) {
	t.Helper()
	if err := db.InsertRecording(context.Background(), Recording{
		ID: id, RoomID: room.ID, RoomSlug: room.Slug, EgressID: "egress-" + id, Status: status,
		StartedBy: room.OwnerSub, StartedAt: 1, EndedAt: endedAt, S3Key: key,
	}); err != nil {
		t.Fatal(err)
	}
}

func int64p(v int64) *int64    { return &v }
func stringp(v string) *string { return &v }

// Pairing a machine opts its owner into transcripts of the recordings that
// end from then on: completed ones, with a file.
func TestCreateTranscriptsFromPairingOn(t *testing.T) {
	ctx := context.Background()
	db := transcriptsTestStore(t)
	alice := Room{ID: "room-a", Slug: "a", Name: "Standup", OwnerSub: "alice", CreatedAt: 1}
	bob := Room{ID: "room-b", Slug: "b", Name: "Retro", OwnerSub: "bob", CreatedAt: 1}
	for _, room := range []Room{alice, bob} {
		if err := db.CreateRoom(ctx, room); err != nil {
			t.Fatal(err)
		}
	}
	const paired = 1_000_000
	if err := db.AddMachine(ctx, moil.MachineRecord{
		ID: "m_1", Owner: "alice", TokenHash: "hash", PairedAt: time.Unix(paired, 0),
	}); err != nil {
		t.Fatal(err)
	}
	key := stringp("recordings/a/x.ogg")
	addRecording(t, db, alice, "before", "completed", int64p(paired-1), key)
	addRecording(t, db, alice, "same-second", "completed", int64p(paired), key)
	addRecording(t, db, alice, "after", "completed", int64p(paired+1), key)
	addRecording(t, db, alice, "no-file", "completed", int64p(paired+1), nil)
	addRecording(t, db, alice, "empty-key", "completed", int64p(paired+1), stringp(""))
	addRecording(t, db, alice, "failed", "failed", int64p(paired+1), key)
	addRecording(t, db, alice, "unknown-end", "completed", nil, key)
	addRecording(t, db, alice, "active", "recording", nil, nil)
	addRecording(t, db, alice, "had-one", "completed", int64p(paired+1), key)
	addRecording(t, db, bob, "bobs", "completed", int64p(paired+1), key)
	if ok, err := db.RequestTranscript(ctx, "had-one", 5); err != nil || !ok {
		t.Fatalf("request: %t, %v", ok, err)
	}
	if err := db.FailTranscript(ctx, "had-one", "It failed.", 6); err != nil {
		t.Fatal(err)
	}

	added, err := db.CreateTranscripts(ctx, 2_000_000)
	if err != nil || added != 2 {
		t.Fatalf("added %d, %v", added, err)
	}
	if added, err := db.CreateTranscripts(ctx, 2_000_001); err != nil || added != 0 {
		t.Fatalf("added again %d, %v", added, err)
	}
	pending, err := db.PendingTranscripts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, transcript := range pending {
		ids = append(ids, transcript.ID)
		if transcript.S3Key == nil || *transcript.S3Key != *key || transcript.RoomSlug != "a" ||
			transcript.RoomOwner != "alice" || transcript.RoomName != "Standup" || transcript.RequestedAt != 2_000_000 {
			t.Fatalf("pending transcript = %+v", transcript)
		}
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"after", "same-second"}) {
		t.Fatalf("pending = %v", ids)
	}
	if row, err := db.Transcript(ctx, "had-one"); err != nil || row.Status != "failed" || row.Error != "It failed." {
		t.Fatalf("a failed transcript = %+v, %v", row, err)
	}
	rows, err := db.TranscriptsByRoom(ctx, alice.ID)
	if err != nil || len(rows) != 3 || rows["after"].RequestedAt != 2_000_000 {
		t.Fatalf("alice's transcripts = %+v, %v", rows, err)
	}
	if rows, err := db.TranscriptsByRoom(ctx, bob.ID); err != nil || len(rows) != 0 {
		t.Fatalf("bob's transcripts = %+v, %v", rows, err)
	}
}

// A transcript moves only along its lifecycle: requested when there is
// none, ended while pending, retried when failed. It goes with its
// recording.
func TestTranscriptLifecycle(t *testing.T) {
	ctx := context.Background()
	db := transcriptsTestStore(t)
	room := Room{ID: "room-a", Slug: "a", Name: "Standup", OwnerSub: "alice", CreatedAt: 1}
	if err := db.CreateRoom(ctx, room); err != nil {
		t.Fatal(err)
	}
	addRecording(t, db, room, "active", "recording", nil, nil)
	addRecording(t, db, room, "done", "completed", int64p(10), stringp("recordings/a/done.ogg"))

	if ok, err := db.RequestTranscript(ctx, "active", 1); err != nil || ok {
		t.Fatalf("request of an active recording: %t, %v", ok, err)
	}
	if ok, err := db.RequestTranscript(ctx, "missing", 1); err != nil || ok {
		t.Fatalf("request of a missing recording: %t, %v", ok, err)
	}
	if ok, err := db.RetryTranscript(ctx, "done", 1); err != nil || ok {
		t.Fatalf("retry without a transcript: %t, %v", ok, err)
	}
	if ok, err := db.RequestTranscript(ctx, "done", 100); err != nil || !ok {
		t.Fatalf("request: %t, %v", ok, err)
	}
	if ok, err := db.RequestTranscript(ctx, "done", 101); err != nil || ok {
		t.Fatalf("second request: %t, %v", ok, err)
	}
	if ok, err := db.RetryTranscript(ctx, "done", 102); err != nil || ok {
		t.Fatalf("retry of a pending transcript: %t, %v", ok, err)
	}

	if err := db.FailTranscript(ctx, "done", "The machine couldn't download the recording. Try again.", 110); err != nil {
		t.Fatal(err)
	}
	row, err := db.Transcript(ctx, "done")
	if err != nil || row.Status != "failed" || row.FinishedAt == nil || *row.FinishedAt != 110 ||
		row.Error != "The machine couldn't download the recording. Try again." {
		t.Fatalf("failed transcript = %+v, %v", row, err)
	}
	// Only a pending transcript ends.
	speakers := 3
	if err := db.CompleteTranscript(ctx, "done", &speakers, 111); err != nil {
		t.Fatal(err)
	}
	if row, _ := db.Transcript(ctx, "done"); row.Status != "failed" {
		t.Fatalf("a failed transcript completed: %+v", row)
	}

	if ok, err := db.RetryTranscript(ctx, "done", 120); err != nil || !ok {
		t.Fatalf("retry: %t, %v", ok, err)
	}
	row, err = db.Transcript(ctx, "done")
	if err != nil || row.Status != "pending" || row.RequestedAt != 120 || row.FinishedAt != nil || row.Error != "" {
		t.Fatalf("retried transcript = %+v, %v", row, err)
	}
	if err := db.CompleteTranscript(ctx, "done", &speakers, 130); err != nil {
		t.Fatal(err)
	}
	if err := db.FailTranscript(ctx, "done", "Too late.", 131); err != nil {
		t.Fatal(err)
	}
	row, err = db.Transcript(ctx, "done")
	if err != nil || row.Status != "completed" || row.Speakers == nil || *row.Speakers != 3 || row.Error != "" {
		t.Fatalf("completed transcript = %+v, %v", row, err)
	}
	if ok, err := db.RetryTranscript(ctx, "done", 140); err != nil || ok {
		t.Fatalf("retry of a completed transcript: %t, %v", ok, err)
	}

	if _, err := db.DeleteRecording(ctx, "done", 200); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Transcript(ctx, "done"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("transcript of a deleted recording: %v", err)
	}
}

// A pending transcript's room is its recording's room by ID. The slug a
// recording carries is only a copy, which a rename changes: read in the
// middle of one, it may name another host's room.
func TestPendingTranscriptsFindTheRoomByID(t *testing.T) {
	ctx := context.Background()
	db := transcriptsTestStore(t)
	alice := Room{ID: "room-a", Slug: "standup", Name: "Standup", OwnerSub: "alice", CreatedAt: 1}
	bob := Room{ID: "room-b", Slug: "retro", Name: "Retro", OwnerSub: "bob", CreatedAt: 1}
	for _, room := range []Room{alice, bob} {
		if err := db.CreateRoom(ctx, room); err != nil {
			t.Fatal(err)
		}
	}
	stale := alice
	stale.Slug = bob.Slug
	addRecording(t, db, stale, "rec", "completed", int64p(10), stringp("recordings/standup/rec/x.ogg"))
	if ok, err := db.RequestTranscript(ctx, "rec", 20); err != nil || !ok {
		t.Fatalf("request: %t, %v", ok, err)
	}
	pending, err := db.PendingTranscripts(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	if got := pending[0]; got.RoomOwner != "alice" || got.RoomName != "Standup" || got.RoomID != alice.ID {
		t.Fatalf("pending transcript's room = %q's %q (%s)", got.RoomOwner, got.RoomName, got.RoomID)
	}
}

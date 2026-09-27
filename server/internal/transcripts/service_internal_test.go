package transcripts

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"
	"git.convex.works/ConvexWorks/moil/sdk/go/moiltest"

	"klisi/internal/store"
)

// A run that ended just as klisi started stopping is still recorded: the
// follower goes by the run, not by klisi's context, and writes with a
// context klisi stopping doesn't cancel.
func TestARunThatEndedIsRecordedWhileKlisiStops(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "klisi.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	room := store.Room{ID: "room-a", Slug: "standup", Name: "Standup", OwnerSub: "alice", CreatedAt: 1}
	if err := db.CreateRoom(ctx, room); err != nil {
		t.Fatal(err)
	}
	key := "recordings/standup/rec/x.ogg"
	if err := db.InsertRecording(ctx, store.Recording{
		ID: "rec", RoomID: room.ID, RoomSlug: room.Slug, EgressID: "egress-rec", Status: "completed",
		StartedBy: "alice", StartedAt: 1, S3Key: &key,
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.RequestTranscript(ctx, "rec", 1); err != nil || !ok {
		t.Fatalf("request: %t, %v", ok, err)
	}
	server, err := moil.NewServer(moil.Config{Name: "klisi", VerificationURL: "http://klisi.test/machines", Store: db})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	bundle, err := Bundle()
	if err != nil {
		t.Fatal(err)
	}
	server.AddBundle(bundle)
	machine := moiltest.Pair(t, server, "alice")
	machine.Approve(bundle)
	machine.Connect()
	s := New(Config{Moil: server, Bundle: bundle, Store: db})

	run, err := server.Submit(ctx, moil.Job{ID: "recording-rec", Bundle: bundle, Eligible: moil.OwnedBy("alice")})
	if err != nil {
		t.Fatal(err)
	}
	j := &job{recordingID: "rec", run: run}
	s.jobs["rec"] = j
	a := machine.NextAttempt()
	a.ScriptError("can't read the recording: moov atom not found", false)
	a.WaitAcked()
	select {
	case <-run.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the run never ended")
	}

	stopping, stop := context.WithCancel(ctx)
	stop()
	s.followers.Add(1)
	s.follow(stopping, j)
	row, err := db.Transcript(ctx, "rec")
	if err != nil || row.Status != "failed" || row.Error != "Transcription failed: can't read the recording: moov atom not found." {
		t.Fatalf("row after klisi stopped = %+v, %v", row, err)
	}
	if len(s.jobs) != 0 {
		t.Fatalf("still following %v", s.jobs)
	}
}

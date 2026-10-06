package transcripts_test

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A recording's slug is a copy of its room's, which a rename changes: read
// in the middle of one, it can name another host's room. Whatever the slug
// says, the job goes to the machines of the owner of the recording's room,
// by ID, under that room's name.
func TestJobsFollowTheRoomByIDNotItsSlug(t *testing.T) {
	e := newEnv(t)
	standup := e.room("alice", "Standup")
	retro := e.room("bob", "Retro")
	bob := e.machine("bob")
	alice := e.machine("alice")
	alice.Disconnect()
	// Alice's recording, carrying the slug of Bob's room.
	rec := e.recordFor(standup, retro.Slug, 10*time.Minute, time.Now())
	e.submitted(rec)

	bob.Sync()
	if offers := bob.Offers(); len(offers) != 0 {
		t.Fatalf("bob's machine was offered %+v", offers)
	}
	alice.Connect()
	a := alice.NextAttempt()
	if a.JobID != "recording-"+rec.ID || a.Title != "Standup" {
		t.Fatalf("alice's attempt: job %s, title %q", a.JobID, a.Title)
	}
	finish(t, a, 2)
	waitFor(t, "the transcript to complete", func() bool { row, ok := e.row(rec); return ok && row.Status == "completed" })
	bob.Sync()
	if offers := bob.Offers(); len(offers) != 0 {
		t.Fatalf("bob's machine was offered %+v", offers)
	}
}

// A room that changes hands after its job was submitted doesn't hand the
// recording to the old owner's machines: Prepare checks the owner when a
// machine takes the job, ends it, and the reconciler submits it again for
// the new owner.
func TestPrepareChecksTheRoomsCurrentOwner(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	alice := e.machine("alice")
	alice.Disconnect()
	bob := e.machine("bob")
	rec := e.record(room, time.Now())
	e.submitted(rec)
	first, _ := e.moil.Run("recording-" + rec.ID)

	// tide has no API to give a room away; an operator can.
	e.sql(`UPDATE rooms SET owner_sub = 'bob' WHERE id = ?`, room.ID)
	alice.Connect()
	if offer := alice.NextOffer(); offer.JobID != first.ID() {
		t.Fatalf("alice's machine was offered %s", offer.JobID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if _, err := first.Wait(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the job alice's machine bid for ended with %v", err)
	}
	b := bob.NextAttempt()
	if b.JobID != "recording-"+rec.ID {
		t.Fatalf("bob's attempt is of %s", b.JobID)
	}
	finish(t, b, 2)
	waitFor(t, "the transcript to complete", func() bool { row, ok := e.row(rec); return ok && row.Status == "completed" })
	alice.Sync()
	if offers := alice.Offers(); len(offers) != 1 {
		t.Fatalf("alice's machine was offered %+v", offers)
	}
}

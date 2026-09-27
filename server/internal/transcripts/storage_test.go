package transcripts_test

import (
	"net/http"
	"testing"
	"time"

	"klisi/internal/api"
)

// Deleting a recording succeeds while storage refuses to remove anything,
// and none of its files outlive it: the recording reconciler keeps trying
// until storage takes them.
func TestDeletedFilesAreRemovedOnceStorageIsBack(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())
	finish(t, machine.NextAttempt(), 2)
	e.waitStatus(room, rec, api.TranscriptCompleted)
	files := e.s3.Keys("recordings/")
	if len(files) != 3 {
		t.Fatalf("files of a transcribed recording: %q", files)
	}

	e.s3.Break(opRemove)
	if response := e.deleteRecording(rec, session("alice")); response.Code != http.StatusNoContent {
		t.Fatalf("delete while storage is down: %d %s", response.Code, response.Body)
	}
	if _, listed := e.list(room)[rec.ID]; listed {
		t.Fatal("the deleted recording is still listed")
	}
	// Several reconciler passes later, storage still has them.
	tried := e.s3.Calls(opRemove)
	waitFor(t, "the reconciler to retry", func() bool { return e.s3.Calls(opRemove) >= tried+2*len(files) })
	if keys := e.s3.Keys("recordings/"); len(keys) != len(files) {
		t.Fatalf("files while storage refuses: %q", keys)
	}

	e.s3.Mend(opRemove)
	waitFor(t, "the files to be removed", func() bool { return len(e.s3.Keys("recordings/")) == 0 })
}

// A job whose end the database refuses to record isn't run again: the row
// stays pending, the job is kept, and each pass tries to record its end
// until the database takes it.
func TestAnEndTheDatabaseRefusedIsRecordedLaterNotRunAgain(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())
	a := machine.NextAttempt()
	run, _ := e.moil.Run("recording-" + rec.ID)

	e.sql(`CREATE TRIGGER refuse_ends BEFORE UPDATE ON transcripts
		BEGIN SELECT RAISE(ABORT, 'database or disk is full'); END`)
	finish(t, a, 3)
	for range 20 {
		e.service.Nudge()
		time.Sleep(5 * time.Millisecond)
	}
	machine.Sync()
	if offers := machine.Offers(); len(offers) != 1 {
		t.Fatalf("the machine was offered %+v", offers)
	}
	if current, _ := e.moil.Run("recording-" + rec.ID); current != run {
		t.Fatal("the job was submitted again")
	}
	if row, ok := e.row(rec); !ok || row.Status != "pending" {
		t.Fatalf("row while the database refuses = %+v, %t", row, ok)
	}

	e.sql(`DROP TRIGGER refuse_ends`)
	e.service.Nudge()
	info := e.waitStatus(room, rec, api.TranscriptCompleted)
	if info.Speakers == nil || *info.Speakers != 3 {
		t.Fatalf("transcript = %+v", info)
	}
	machine.Sync()
	if offers := machine.Offers(); len(offers) != 1 {
		t.Fatalf("the machine was offered %+v", offers)
	}
}

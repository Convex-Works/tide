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

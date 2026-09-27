package transcripts_test

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"klisi/internal/api"
	"klisi/internal/store"
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
	// Each try checks the staged files and their copies again: the
	// follower's, then passes'.
	tries := func() int { return e.s3.Calls(opStat) / (2 * len(store.TranscriptFormats)) }
	waitFor(t, "three tries to record the end", func() bool { e.service.Nudge(); return tries() >= 3 })
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

// A transcript file over 16 MiB fails the job, and nothing of it is kept or
// served, not even the files within the limit. One of exactly 16 MiB is
// kept.
func TestAnOversizedTranscriptIsRefused(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())
	txtKey, vttKey := sidecars(rec)
	limit := bytes.Repeat([]byte("a"), 16<<20)

	a := machine.NextAttempt()
	a.Output("transcript.txt", limit)
	a.Output("transcript.vtt", append(bytes.Repeat([]byte("v"), 16<<20), 'v'))
	a.Succeed(map[string]any{"speakers": 2})
	info := e.waitStatus(room, rec, api.TranscriptFailed)
	if info.Error != "The transcript the machine uploaded is larger than 16 MiB, more than klisi keeps. Try again." {
		t.Fatalf("error = %q", info.Error)
	}
	for _, key := range []string{txtKey, vttKey} {
		if _, ok := e.s3.Object(key); ok {
			t.Fatalf("%s was kept", key)
		}
	}
	if keys := e.s3.Keys("transcripts-staging/"); len(keys) != 0 {
		t.Fatalf("staged uploads left: %q", keys)
	}
	if response := e.download(rec, session("alice"), "txt"); response.Code != http.StatusConflict {
		t.Fatalf("download of a refused transcript: %d", response.Code)
	}

	if response := e.requestTranscript(rec, session("alice")); response.Code != http.StatusAccepted {
		t.Fatalf("retry: %d %s", response.Code, response.Body)
	}
	b := machine.NextAttempt()
	b.Output("transcript.txt", limit)
	b.Output("transcript.vtt", []byte("WEBVTT\n"))
	b.Succeed(map[string]any{"speakers": 2})
	e.waitStatus(room, rec, api.TranscriptCompleted)
	if got, _ := e.s3.Object(txtKey); !bytes.Equal(got, limit) {
		t.Fatalf("kept %d bytes of the 16 MiB transcript", len(got))
	}
}

// The URLs a machine holds outlive its job, but they only ever name its
// attempt's staging keys: replayed after the transcript is done, they can't
// replace it, and what they upload is removed once they expire.
func TestAReplayedURLCantReplaceATranscript(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())
	a := machine.NextAttempt()
	txt, vtt := finish(t, a, 2)
	e.waitStatus(room, rec, api.TranscriptCompleted)

	for _, name := range []string{"transcript.txt", "transcript.vtt"} {
		if status := statusOf(t, http.MethodPut, a.Outputs[name].URL, []byte("forged")); status != http.StatusOK {
			t.Fatalf("replaying the %s URL: %d", name, status)
		}
	}
	txtKey, vttKey := sidecars(rec)
	for key, want := range map[string][]byte{txtKey: txt, vttKey: vtt} {
		if got, _ := e.s3.Object(key); !bytes.Equal(got, want) {
			t.Fatalf("%s after the replay = %q", key, got)
		}
	}
	response := e.download(rec, session("alice"), "txt")
	if got, _ := get(t, response.Header().Get("Location")); !bytes.Equal(got, txt) {
		t.Fatalf("download after the replay = %q", got)
	}

	e.clock.Advance(a.Timeout + time.Hour)
	waitFor(t, "the replayed uploads to be removed", func() bool { return len(e.s3.Keys("transcripts-staging/")) == 0 })
	if got, _ := e.s3.Object(txtKey); !bytes.Equal(got, txt) {
		t.Fatalf("the transcript after the sweep = %q", got)
	}
}

// A transcript whose files klisi is still checking when it starts to stop
// is kept: recording a run's end goes on, storage included, until it's done.
func TestATranscriptThatEndsAsKlisiStopsIsKept(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())
	a := machine.NextAttempt()

	stat := e.s3.Hold(opStat)
	txt, _ := finish(t, a, 2)
	stat.Entered(t)
	e.cancelService()
	stat.Release()
	e.waitService()
	if row, ok := e.row(rec); !ok || row.Status != "completed" || row.Speakers == nil || *row.Speakers != 2 {
		t.Fatalf("row after klisi stopped = %+v, %t", row, ok)
	}
	txtKey, _ := sidecars(rec)
	if got, _ := e.s3.Object(txtKey); !bytes.Equal(got, txt) {
		t.Fatalf("stored txt = %q", got)
	}
}

// klisi copies only the file it checked: a machine that replaces its upload
// between the check and the copy, with the URL it still holds, gets it
// checked again, and refused.
func TestAFileReplacedAfterItsCheckIsntCopied(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())
	a := machine.NextAttempt()

	copying := e.s3.Hold(opCopy)
	finish(t, a, 2)
	copying.Entered(t)
	oversized := bytes.Repeat([]byte("a"), 16<<20+1)
	if status := statusOf(t, http.MethodPut, a.Outputs["transcript.txt"].URL, oversized); status != http.StatusOK {
		t.Fatalf("replacing the upload: %d", status)
	}
	copying.Release()
	waitFor(t, "the transcript to fail", func() bool {
		e.service.Nudge()
		row, ok := e.row(rec)
		return ok && row.Status == "failed"
	})
	if row, _ := e.row(rec); row.Error != "The transcript the machine uploaded is larger than 16 MiB, more than klisi keeps. Try again." {
		t.Fatalf("error = %q", row.Error)
	}
	txtKey, _ := sidecars(rec)
	if got, ok := e.s3.Object(txtKey); ok {
		t.Fatalf("kept %d bytes", len(got))
	}
}

// A recording deleted while klisi copies its transcript beside it, after
// the deletion removed its files, doesn't keep the copies.
func TestATranscriptCopiedAsItsRecordingIsDeletedIsRemoved(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())
	a := machine.NextAttempt()

	copying := e.s3.Hold(opCopy)
	finish(t, a, 2)
	copying.Entered(t)
	if response := e.deleteRecording(rec, session("alice")); response.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", response.Code, response.Body)
	}
	if keys := e.s3.Keys("recordings/"); len(keys) != 0 {
		t.Fatalf("files after the deletion: %q", keys)
	}
	copying.Release()
	waitFor(t, "the copies to be removed", func() bool {
		return e.s3.Calls(opCopy) == len(store.TranscriptFormats) && len(e.s3.Keys("recordings/")) == 0 &&
			len(e.s3.Keys("transcripts-staging/")) == 0
	})
	if _, ok := e.row(rec); ok {
		t.Fatal("the transcript outlived its recording")
	}
}

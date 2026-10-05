package transcripts_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"tide/internal/api"
	"tide/internal/store"
)

// These tests hold tide to what it saves of a transcript a machine made:
// only what moil says the machine uploaded, checked again once copied,
// nothing beside a recording deleted meanwhile, and nothing at all once it
// has tried for long enough.

// A transcript format the machine didn't upload is refused on moil's word,
// without asking storage, which may not answer "no such key" for it: AWS
// answers 403 when tide's credentials can't list the bucket.
func TestAMissingOutputIsRefusedWithoutAskingStorage(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())
	e.s3.Break(opStat)

	a := machine.NextAttempt()
	a.Output("transcript.txt", []byte("only the text"))
	a.Succeed(map[string]any{"speakers": 1})
	info := e.waitStatus(room, rec, api.TranscriptFailed)
	if info.Error != "The machine finished without uploading the transcript. Try again." {
		t.Fatalf("error = %q", info.Error)
	}
	if n := e.s3.Calls(opStat); n != 0 {
		t.Fatalf("tide asked storage about %d files", n)
	}
	if keys := e.s3.Keys("transcripts-staging/"); len(keys) != 0 {
		t.Fatalf("staged uploads left: %q", keys)
	}
}

// A staged file that isn't the size the machine reported uploading, one
// replaced or cut short since, is refused, and nothing is copied.
func TestAStagedFileThatIsntWhatTheMachineReportedIsRefused(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())

	a := machine.NextAttempt()
	a.Output("transcript.txt", []byte("[00:00:01] Speaker 1: Hello."))
	a.Output("transcript.vtt", []byte("WEBVTT\n"))
	// Replaced after the machine uploaded it, with the URL it holds.
	if status := statusOf(t, http.MethodPut, a.Outputs["transcript.txt"].URL, []byte("[00:00:01] Speaker 1: Goodbye, then.")); status != http.StatusOK {
		t.Fatalf("replacing the upload: %d", status)
	}
	a.Succeed(map[string]any{"speakers": 1})
	info := e.waitStatus(room, rec, api.TranscriptFailed)
	if info.Error != "The transcript in tide's storage isn't the one the machine reported uploading. Try again." {
		t.Fatalf("error = %q", info.Error)
	}
	if n := e.s3.Calls(opCopy); n != 0 {
		t.Fatalf("tide copied %d files", n)
	}
	if keys := e.s3.Keys("recordings/"); len(keys) != 1 || len(e.s3.Keys("transcripts-staging/")) != 0 {
		t.Fatalf("files beside the recording: %q; staged: %q", keys, e.s3.Keys("transcripts-staging/"))
	}
}

// Storage that reports no entity tags copies whatever is staged when tide
// copies it, not what tide checked: a machine that replaces its upload in
// between gets its replacement copied beside the recording. tide checks
// each copy, and refuses one that isn't what the machine reported,
// removing the copies.
func TestACopyIsCheckedWhereStorageHasNoEntityTags(t *testing.T) {
	for _, test := range []struct {
		name        string
		replacement []byte
		error       string
	}{
		{"replaced with one over 16 MiB", bytes.Repeat([]byte("a"), 16<<20+1),
			"The transcript the machine uploaded is larger than 16 MiB, more than tide keeps. Try again."},
		{"replaced with one of another size", []byte("[00:00:01] Speaker 1: Something else entirely."),
			"The transcript in tide's storage isn't the one the machine reported uploading. Try again."},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := newEnv(t)
			room := e.room("alice", "Standup")
			machine := e.machine("alice")
			rec := e.record(room, time.Now())
			e.s3.NoETags()
			a := machine.NextAttempt()

			copying := e.s3.Hold(opCopy)
			finish(t, a, 2)
			copying.Entered(t) // tide checked the text, and copies it
			if status := statusOf(t, http.MethodPut, a.Outputs["transcript.txt"].URL, test.replacement); status != http.StatusOK {
				t.Fatalf("replacing the upload: %d", status)
			}
			copying.Release()
			info := e.waitStatus(room, rec, api.TranscriptFailed)
			if info.Error != test.error {
				t.Fatalf("error = %q", info.Error)
			}
			if keys := e.s3.Keys("recordings/"); len(keys) != 1 || keys[0] != *rec.S3Key {
				t.Fatalf("files beside the recording: %q", keys)
			}
			if response := e.download(rec, session("alice"), "txt"); response.Code != http.StatusConflict {
				t.Fatalf("download of a refused transcript: %d", response.Code)
			}
		})
	}
}

// A recording deleted while tide copies its transcript beside it, after
// the deletion removed its files, keeps no copy, whatever fails after it
// was deleted: tide tries again, finds the recording gone, and removes the
// copies, whether or not both landed.
func TestCopiesLandingAfterTheirRecordingIsDeletedAreRemoved(t *testing.T) {
	for _, test := range []struct {
		name string
		// fail makes what comes after the text's copy fail, until mend.
		fail func(e *env, rec store.Recording) (mend func())
	}{
		{"the captions' copy fails", func(e *env, rec store.Recording) func() {
			_, vtt := sidecars(rec)
			e.s3.BreakKey(opCopy, vtt)
			return func() { e.s3.MendKey(opCopy, vtt) }
		}},
		{"queueing the copies for removal fails", func(e *env, _ store.Recording) func() {
			e.sql(`CREATE TRIGGER refuse_removals BEFORE INSERT ON object_removals
				BEGIN SELECT RAISE(ABORT, 'database or disk is full'); END`)
			return func() { e.sql(`DROP TRIGGER refuse_removals`) }
		}},
		{"checking the recording again fails", func(e *env, _ store.Recording) func() {
			e.sql(`ALTER TABLE recordings RENAME TO recordings_away`)
			return func() { e.sql(`ALTER TABLE recordings_away RENAME TO recordings`) }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs := watchLogs(t)
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
			mend := test.fail(e, rec)
			copying.Release()
			txtKey, _ := sidecars(rec)
			waitFor(t, "the text's copy to land", func() bool { _, ok := e.s3.Object(txtKey); return ok })
			waitFor(t, "tide to fail to record the job's end", func() bool {
				return strings.Contains(logs.String(), "transcripts: recording "+rec.ID+": record how its job ended, again next pass")
			})
			mend()
			waitFor(t, "the copies to be removed", func() bool {
				e.service.Nudge()
				return len(e.s3.Keys("recordings/")) == 0 && len(e.s3.Keys("transcripts-staging/")) == 0
			})
			if _, ok := e.row(rec); ok {
				t.Fatal("the transcript outlived its recording")
			}
		})
	}
}

// A transcript tide can't save, its storage failing, isn't shown running
// forever: tide tries again every pass for an hour, or until the staged
// files' URLs expire if that's sooner, then fails it, blaming its storage.
func TestATranscriptTideCantSaveFailsInTheEnd(t *testing.T) {
	for _, test := range []struct {
		name string
		// left is how long the attempt's URLs have left when it succeeds,
		// and after is how long tide tries.
		left, after time.Duration
	}{
		{"for an hour", 3 * time.Hour, time.Hour},
		{"until the staged files' URLs expire", 10 * time.Minute, 10 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := newEnv(t)
			room := e.room("alice", "Standup")
			machine := e.machine("alice")
			rec := e.record(room, time.Now())
			a := machine.NextAttempt()
			e.clock.Advance(a.Timeout + 15*time.Minute - test.left)

			e.s3.Break(opStat)
			finish(t, a, 2)
			tries := func() int { return e.s3.Calls(opStat) }
			waitFor(t, "tide to try a few times", func() bool { e.service.Nudge(); return tries() >= 3 })
			e.clock.Advance(test.after - time.Second)
			tried := tries()
			waitFor(t, "tide to try again", func() bool { e.service.Nudge(); return tries() >= tried+2 })
			if info := e.transcript(room, rec); info.Status != api.TranscriptRunning {
				t.Fatalf("transcript a second before tide gives up = %+v", info)
			}

			e.clock.Advance(time.Second)
			info := e.waitTranscript(room, rec, "failed", func(info *api.TranscriptInfo) bool {
				e.service.Nudge()
				return info != nil && info.Status == api.TranscriptFailed
			})
			if info.Error != "The machine made the transcript, but tide couldn't save it to its storage. Try again, and if it keeps failing, ask tide's administrator to check tide's storage." {
				t.Fatalf("error = %q", info.Error)
			}
			if keys := e.s3.Keys("transcripts-staging/"); len(keys) != 0 {
				t.Fatalf("staged uploads left: %q", keys)
			}

			// Once storage is back, requesting it again makes it.
			e.s3.Mend(opStat)
			if response := e.requestTranscript(rec, session("alice")); response.Code != http.StatusAccepted {
				t.Fatalf("retry: %d %s", response.Code, response.Body)
			}
			finish(t, machine.NextAttempt(), 2)
			e.waitStatus(room, rec, api.TranscriptCompleted)
		})
	}
}

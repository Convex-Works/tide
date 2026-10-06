package transcripts_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	"tide/internal/api"
	"tide/internal/transcripts"
)

// A recording that ended after its owner paired a machine is waiting for
// its transcript from the moment it's listed, before the reconciler got to
// it: never offered to be requested, which would then answer that it's on
// its way. A request of a transcript on its way answers with its status.
func TestARecordingAboutToBeTranscribedIsWaiting(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	e.stopService() // the reconciler hasn't got to it yet
	rec := e.record(room, time.Now())
	if _, ok := e.row(rec); ok {
		t.Fatal("the reconciler added a transcript while stopped")
	}
	if info := e.transcript(room, rec); info == nil || info.Status != api.TranscriptWaiting ||
		info.Message != "Waiting for a machine to start it." {
		t.Fatalf("transcript about to be made = %+v", info)
	}

	// The reconciler adds it just as the host asks.
	if _, err := e.db.CreateTranscripts(context.Background(), e.clock.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if info := requested(t, e.requestTranscript(rec, session("alice"))); info.Status != api.TranscriptWaiting {
		t.Fatalf("request of a pending transcript answered %+v", info)
	}
	e.run()
	a := machine.NextAttempt()
	a.Progress(0.5, "transcribed 00:05:00 of 00:10:00")
	e.waitTranscript(room, rec, "halfway", func(info *api.TranscriptInfo) bool {
		return info != nil && info.Progress != nil && *info.Progress == 0.5
	})
	if info := requested(t, e.requestTranscript(rec, session("alice"))); info.Status != api.TranscriptRunning ||
		info.Progress == nil || *info.Progress != 0.5 || info.Message != "Transcribed 00:05:00 of 00:10:00" {
		t.Fatalf("request of a running transcript answered %+v", info)
	}
	finish(t, a, 2)
	e.waitStatus(room, rec, api.TranscriptCompleted)
}

func requested(t *testing.T, response interface {
	Result() *http.Response
}) api.TranscriptInfo {
	t.Helper()
	result := response.Result()
	defer result.Body.Close()
	var info api.TranscriptInfo
	if err := json.NewDecoder(result.Body).Decode(&info); err != nil || result.StatusCode != http.StatusAccepted {
		t.Fatalf("request: %d %+v, %v", result.StatusCode, info, err)
	}
	return info
}

// The recordings list lists recordings even when their transcripts can't
// be loaded, without them.
func TestTheListShowsRecordingsWhenTranscriptsFail(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())
	finish(t, machine.NextAttempt(), 2)
	e.waitStatus(room, rec, api.TranscriptCompleted)

	e.sql(`ALTER TABLE transcripts RENAME TO transcripts_away`)
	listed, ok := e.list(room)[rec.ID]
	if !ok || listed.Transcript != nil || listed.Status != "completed" {
		t.Fatalf("listed without transcripts: %+v, %t", listed, ok)
	}
	e.sql(`ALTER TABLE transcripts_away RENAME TO transcripts`)
	if info := e.transcript(room, rec); info == nil || info.Status != api.TranscriptCompleted {
		t.Fatalf("transcript once it loads again = %+v", info)
	}
}

// What machines say is shown as plain text: no control characters, no
// characters that reorder or hide what's around them.
func TestMachineTextIsShownPlain(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())
	a := machine.NextAttempt()

	a.Phase("warming\x07up")
	e.waitTranscript(room, rec, "warming up", func(info *api.TranscriptInfo) bool {
		return info != nil && info.Message == "Warming up"
	})
	a.Progress(0.5, "finding‮ speakers\x1b[31m\n")
	e.waitTranscript(room, rec, "finding speakers", func(info *api.TranscriptInfo) bool {
		return info != nil && info.Message == "Finding speakers [31m"
	})
	a.ScriptError("can't read​ the recording:\x00 moov⁦ atom not found\r\n", false)
	info := e.waitStatus(room, rec, api.TranscriptFailed)
	if info.Error != "Transcription failed: can't read the recording: moov atom not found." {
		t.Fatalf("error = %q", info.Error)
	}
}

// Where the moil app would refuse the URLs of tide's storage, every
// transcript fails at once, naming the setting to change, and no machine
// is offered one.
func TestStorageMachinesCantUseFailsTranscriptsAtOnce(t *testing.T) {
	problem := transcripts.StorageWarning("http://tide:8080", "http://minio:9000")
	e := newEnv(t, func(cfg *transcripts.Config) { cfg.StorageProblem = problem })
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())
	info := e.waitStatus(room, rec, api.TranscriptFailed)
	if info.Error != "Machines can't use tide's storage: TIDE_S3_PUBLIC_ENDPOINT is plain http, which the moil app accepts only when tide and its storage run on the machine itself. Ask tide's administrator to set it to an https address, then try again." {
		t.Fatalf("error = %q", info.Error)
	}
	machine.Sync()
	if offers := machine.Offers(); len(offers) != 0 {
		t.Fatalf("the machine was offered %+v", offers)
	}
}

// Machines take https URLs, and http ones only to storage on the machine
// itself, from a tide on the machine itself.
func TestStorageWarning(t *testing.T) {
	for _, test := range []struct {
		base, storage string
		refused       bool
	}{
		{"http://localhost:5173", "http://localhost:9000", false},
		{"http://127.0.0.1:8080", "http://[::1]:9000", false},
		{"https://tide.example.com", "https://s3.example.com", false},
		{"http://tide:8080", "https://s3.example.com", false},
		{"https://tide.example.com", "http://minio:9000", true},
		{"http://tide:8080", "http://minio:9000", true},
		{"https://tide.example.com", "http://localhost:9000", true},
		{"http://localhost:8080", "http://192.168.1.5:9000", true},
		{"http://localhost:8080", "minio:9000", true},
	} {
		if refused := transcripts.StorageWarning(test.base, test.storage) != ""; refused != test.refused {
			t.Errorf("tide at %s, storage at %s: refused %t, want %t", test.base, test.storage, refused, test.refused)
		}
	}
}

// When tide can't prepare a machine's attempt, the transcript's error
// says it was tide, not the machine.
func TestTidesOwnFailuresBlameTide(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	e.sql(`CREATE TRIGGER refuse_removals BEFORE INSERT ON object_removals
		BEGIN SELECT RAISE(ABORT, 'database or disk is full'); END`)
	rec := e.record(room, time.Now())
	info := e.waitStatus(room, rec, api.TranscriptFailed)
	if info.Error != "tide couldn't reach its database or storage to hand the recording to a machine. Try again, and if it keeps failing, ask tide's administrator to check them." {
		t.Fatalf("error = %q", info.Error)
	}
	run, _ := e.moil.Run("recording-" + rec.ID)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	var attempt *moil.JobError
	if _, err := run.Wait(ctx); !errors.As(err, &attempt) {
		t.Fatalf("the job ended with %v", err)
	}
	machine.Sync()
	if offers := machine.Offers(); len(offers) != 3 {
		t.Fatalf("the machine was offered %d attempts", len(offers))
	}
}

package transcripts_test

import (
	"net/http"
	"testing"
	"time"

	"klisi/internal/api"
)

// A transcript job tells the machine when its recording was made, for the
// hooks the machine's owner runs after a job succeeds (moil's
// spec/machine.md §2): its params are the recording's times, in RFC 3339 in
// UTC to the second, leaving out a time the recording doesn't have. The
// bundle ignores them with a warning, which changes nothing.
func TestJobParamsAreTheRecordingsTimes(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")

	for _, test := range []struct {
		name           string
		started, ended time.Time
		duration       time.Duration
		params         string
	}{{
		// klisi started it at 22:10; egress wrote 42 minutes 9.4 seconds of
		// it, and ended it at 22:52:14.6.
		name:     "a recording",
		started:  time.Date(2026, 9, 27, 22, 10, 0, 0, time.UTC),
		ended:    time.Date(2026, 9, 27, 22, 52, 14, 600_000_000, time.UTC),
		duration: 2529*time.Second + 400*time.Millisecond,
		params:   `{"recording":{"duration_s":2529,"ended_at":"2026-09-27T22:52:14Z","started_at":"2026-09-27T22:10:00Z"}}`,
	}, {
		name:     "a recording egress didn't say the end of",
		started:  time.Date(2026, 9, 28, 9, 30, 0, 0, time.UTC),
		duration: 5 * time.Minute,
		params:   `{"recording":{"duration_s":300,"started_at":"2026-09-28T09:30:00Z"}}`,
	}} {
		rec := e.recordEgress(room, room.Slug, test.started, test.ended, test.duration)
		// It ended before alice paired her machine, or at no time klisi
		// knows, so it waits for her to ask.
		if response := e.requestTranscript(rec, session("alice")); response.Code != http.StatusAccepted {
			t.Fatalf("%s: request: %d %s", test.name, response.Code, response.Body)
		}
		a := machine.NextAttempt()
		if a.JobID != "recording-"+rec.ID || a.Title != "Standup" {
			t.Fatalf("%s: attempt = job %q, title %q", test.name, a.JobID, a.Title)
		}
		// The params as the script, and then a hook, gets them: the JSON
		// value klisi sent, written as the app writes it, keys in order.
		if string(a.Params) != test.params {
			t.Fatalf("%s: params = %s, want %s", test.name, a.Params, test.params)
		}
		// The bundle logs that it ignores them, as it does every param it
		// doesn't know, and transcribes the recording all the same.
		a.Phase("running")
		a.Log("warn", "ignoring unknown param 'recording'")
		finish(t, a, 2)
		if info := e.waitStatus(room, rec, api.TranscriptCompleted); info.Error != "" || info.Speakers == nil || *info.Speakers != 2 {
			t.Fatalf("%s: transcript = %+v", test.name, info)
		}
	}
}

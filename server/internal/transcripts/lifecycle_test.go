package transcripts_test

import (
	"net/http"
	"testing"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	"klisi/internal/api"
)

// An attempt may take an hour plus twice the recording, and at least three
// hours: a machine's first also downloads the models.
func TestAttemptsGetTimeForTheRecording(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	for _, test := range []struct {
		duration, timeout time.Duration
	}{
		{time.Minute, 3 * time.Hour},
		{time.Hour, 3 * time.Hour},
		{2 * time.Hour, 5 * time.Hour},
	} {
		rec := e.recordFor(room, room.Slug, test.duration, time.Now())
		a := machine.NextAttempt()
		if a.JobID != "recording-"+rec.ID || a.Timeout != test.timeout {
			t.Errorf("a %v recording: attempt of %s, time limit %v, want %v", test.duration, a.JobID, a.Timeout, test.timeout)
		}
		// Its URLs last as long, and a quarter of an hour more.
		e.clock.Advance(test.timeout + 15*time.Minute - time.Second)
		a.Output("transcript.txt", []byte("in time"))
		e.clock.Advance(time.Second)
		if status := statusOf(t, http.MethodPut, a.Outputs["transcript.vtt"].URL, []byte("too late")); status != http.StatusForbidden {
			t.Errorf("a %v recording: upload after its time: %d", test.duration, status)
		}
		// The next attempt gets URLs of its own.
		a.Fail(moil.CodeOutputUpload, "the URL expired")
		finish(t, machine.NextAttempt(), 1)
		e.waitStatus(room, rec, api.TranscriptCompleted)
	}
}

// A transcript no machine made within 14 days of its request fails, saying
// what to do, and its job is cancelled; requesting it again starts the 14
// days over.
func TestAPendingTranscriptFailsAfter14Days(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.pair("alice") // approved, but never online
	machine.Approve(e.bundle)
	rec := e.record(room, time.Now())
	e.submitted(rec)
	run, _ := e.moil.Run("recording-" + rec.ID)
	// A pass has run by the time a later recording's job is submitted.
	pass := func() {
		t.Helper()
		e.submitted(e.record(e.room("alice", "Probe"+time.Now().Format("150405.000000000")), time.Now()))
	}

	e.clock.Advance(14*24*time.Hour - time.Second)
	pass()
	if row, _ := e.row(rec); row.Status != "pending" || run.State() != moil.Queued {
		t.Fatalf("a second before 14 days: row %s, job %s", row.Status, run.State())
	}
	e.clock.Advance(time.Second)
	pass()
	info := e.waitStatus(room, rec, api.TranscriptFailed)
	if info.Error != "No machine transcribed it within 14 days. Check that a paired machine is online and has approved the transcribe bundle in the moil app, then request it again." {
		t.Fatalf("error = %q", info.Error)
	}
	waitFor(t, "its job to be cancelled", func() bool { return run.State() == moil.Cancelled })

	if response := e.requestTranscript(rec, session("alice")); response.Code != http.StatusAccepted {
		t.Fatalf("request again: %d %s", response.Code, response.Body)
	}
	if row, _ := e.row(rec); row.Status != "pending" || row.RequestedAt != e.clock.Now().Unix() {
		t.Fatalf("row requested again = %+v", row)
	}
	e.clock.Advance(13 * 24 * time.Hour)
	pass()
	if row, _ := e.row(rec); row.Status != "pending" {
		t.Fatalf("13 days after it was requested again: %+v", row)
	}
	machine.Connect()
	for {
		a := machine.NextAttempt()
		finish(t, a, 1)
		if a.JobID == "recording-"+rec.ID {
			break
		}
	}
	e.waitStatus(room, rec, api.TranscriptCompleted)
}

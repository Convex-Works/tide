package transcripts_test

import (
	"context"
	"errors"
	"net/http"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	"tide/internal/api"
)

// These tests play machines that misbehave, within what moil lets through.

// A machine that ends every attempt as cancelled, when tide never asked it
// to, can't make tide submit the job again and again, each turn minting
// URLs and queueing staging keys, as fast as it answers. moil counts each
// such attempt as interrupted, so the job ends after its three attempts,
// having queued one set of staging keys, and the transcript fails, saying
// what happened and what to do. Requesting it again retries it.
func TestAMachineThatCancelsUnaskedFailsTheTranscript(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())
	for attempt := 1; attempt <= 3; attempt++ {
		a := machine.NextAttempt()
		if a.Number != attempt {
			t.Fatalf("attempt %d, want %d", a.Number, attempt)
		}
		a.CancelUnasked()
	}
	run, _ := e.moil.Run("recording-" + rec.ID)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	var failure *moil.JobError
	if _, err := run.Wait(ctx); !errors.As(err, &failure) || failure.Code != moil.CodeInterrupted {
		t.Fatalf("the job ended with %v, want an interrupted attempt", err)
	}
	info := e.waitStatus(room, rec, api.TranscriptFailed)
	if !strings.HasPrefix(info.Error, "Transcription was stopped on the machine") {
		t.Fatalf("error = %q", info.Error)
	}
	// Nothing was submitted again, and the machine, idle, was offered
	// nothing more.
	e.service.Nudge()
	e.reconciled()
	machine.Sync()
	if offers := machine.Offers(); len(offers) != 3 {
		t.Fatalf("the machine was offered %d attempts, want 3", len(offers))
	}
	if current, _ := e.moil.Run("recording-" + rec.ID); current != run {
		t.Fatal("the job was submitted again")
	}
	if staged := e.queuedStaging(); len(staged) != 2 {
		t.Fatalf("staging keys queued for removal: %q", staged)
	}

	if response := e.requestTranscript(rec, session("alice")); response.Code != http.StatusAccepted {
		t.Fatalf("retry: %d %s", response.Code, response.Body)
	}
	finish(t, machine.NextAttempt(), 2)
	e.waitStatus(room, rec, api.TranscriptCompleted)
}

// A machine's error goes into tide's log quoted, on one line: a message
// with line breaks can't forge lines of tide's own.
func TestAMachinesErrorCantForgeLogLines(t *testing.T) {
	logs := watchLogs(t)
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())

	const forged = "transcripts: recording rec9: storage says all is well"
	machine.NextAttempt().ScriptError("can't read the recording\n"+forged+"\r\n"+forged, false)
	e.waitStatus(room, rec, api.TranscriptFailed)
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "storage says all is well") && !strings.Contains(line, `can't read the recording\n`) {
			t.Fatalf("a machine's error forged a log line: %q", line)
		}
	}
	if !strings.Contains(logs.String(), `can't read the recording\n`+forged) {
		t.Fatalf("the machine's error isn't logged:\n%s", logs.String())
	}
}

// A machine that takes a job and lets it go before it starts, which moil
// lets it do again and again, costs tide no new staging keys each time:
// taking the job again within ten minutes, it is handed the directory
// tide made for it before, queued for removal once, with URLs that expire
// when the first did. Later, it gets a new one.
func TestAMachineThatKeepsLettingAJobGoGetsTheSameStagingKeys(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	presign := e.s3.Hold(opPresign)
	rec := e.record(room, time.Now())
	prepared := e.clock.Now()

	// The machine disconnects while tide prepares its attempt...
	presign.Entered(t)
	machine.Disconnect()
	// Once moil sees the machine gone, it drops that preparation. Otherwise
	// it could finish after the clock moves on below, with URLs signed then.
	waitFor(t, "moil to see the machine go", func() bool {
		m, err := e.moil.Machine(context.Background(), machine.ID())
		return err == nil && m.State == moil.Offline
	})
	machine.Connect()
	// ...then takes it again, and pauses while tide prepares it.
	presign.Entered(t)
	machine.SetState(moil.Paused)
	machine.Sync()
	presign.Release()
	// The paused attempt's URLs are signed before the clock moves on: the
	// recording's and both transcripts', after the one the dropped
	// preparation got as far as.
	waitFor(t, "tide to sign the paused attempt's URLs", func() bool {
		return e.s3.Calls(opPresign) == 1+3
	})
	if staged := e.queuedStaging(); len(staged) != 2 {
		t.Fatalf("staging keys queued after tide prepared the job twice: %q", staged)
	}

	// Five minutes later it takes the job for good.
	e.clock.Advance(5 * time.Minute)
	machine.SetState(moil.Idle)
	a := machine.NextAttempt()
	staged := e.queuedStaging()
	if len(staged) != 2 {
		t.Fatalf("staging keys queued after tide prepared the job three times: %q", staged)
	}
	for name, output := range a.Outputs {
		if key := e.s3.Key(t, output.URL); !slices.Contains(staged, key) {
			t.Fatalf("the attempt uploads %s to %s, not a key queued for removal: %q", name, key, staged)
		}
		if expires := e.s3.Expires(t, output.URL); !expires.Equal(prepared.Add(a.Timeout + 15*time.Minute)) {
			t.Fatalf("its %s URL expires at %v, %v after tide first prepared the job", name, expires, expires.Sub(prepared))
		}
	}
	if offers := machine.Offers(); len(offers) != 3 {
		t.Fatalf("the machine was offered the job %d times", len(offers))
	}

	// Taken again once too little of its URLs' time is left for a whole
	// attempt, the job gets a new directory.
	e.clock.Advance(5*time.Minute + time.Second)
	a.Fail(moil.CodeOutputUpload, "the network went away")
	b := machine.NextAttempt()
	if key := e.s3.Key(t, b.Outputs["transcript.txt"].URL); slices.Contains(staged, key) {
		t.Fatalf("the next attempt, ten minutes on, uploads to %s again", key)
	}
	if n := len(e.queuedStaging()); n != 4 {
		t.Fatalf("%d staging keys queued after a new directory", n)
	}
	finish(t, b, 2)
	e.waitStatus(room, rec, api.TranscriptCompleted)
}

// Machines never share staging keys: another machine of the owner taking a
// job the first let go gets a directory of its own.
func TestAnotherMachineGetsStagingKeysOfItsOwn(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	laptop := e.machine("alice")
	studio := e.machine("alice")
	studio.Disconnect()
	presign := e.s3.Hold(opPresign)
	rec := e.record(room, time.Now())

	presign.Entered(t)
	laptop.Disconnect()
	// Once moil sees the laptop gone, it drops the laptop's preparation.
	waitFor(t, "moil to see the laptop go", func() bool {
		m, err := e.moil.Machine(context.Background(), laptop.ID())
		return err == nil && m.State == moil.Offline
	})
	presign.Release()
	studio.Connect()
	a := studio.NextAttempt()
	staged := e.queuedStaging()
	if len(staged) != 4 {
		t.Fatalf("staging keys queued after tide prepared the job for two machines: %q", staged)
	}
	// Two of them are the studio's, the other two the laptop's.
	dir := path.Dir(e.s3.Key(t, a.Outputs["transcript.txt"].URL))
	if studios := slices.DeleteFunc(slices.Clone(staged), func(key string) bool { return path.Dir(key) != dir }); len(studios) != 2 {
		t.Fatalf("the studio uploads to %s; queued: %q", dir, staged)
	}
	finish(t, a, 2)
	e.waitStatus(room, rec, api.TranscriptCompleted)
}

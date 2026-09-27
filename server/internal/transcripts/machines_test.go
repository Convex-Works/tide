package transcripts_test

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"
	"git.convex.works/ConvexWorks/moil/sdk/go/moiltest"

	"klisi/internal/api"
)

// These tests play machines that misbehave, within what moil lets through.

// A machine that ends an attempt as cancelled when klisi never asked it to
// doesn't make klisi submit the job again, which a machine answering every
// attempt so would turn into a loop as fast as it answers, each turn
// minting URLs and queueing staging keys. The transcript fails, saying what
// happened and what to do, and requesting it again retries it.
func TestAMachineThatCancelsUnaskedFailsTheTranscript(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())
	a := machine.NextAttempt()
	run, _ := e.moil.Run("recording-" + rec.ID)

	cancelUnasked(machine, a)
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if _, err := run.Wait(ctx); err != moil.ErrCancelled {
		t.Fatalf("the job ended with %v, want moil.ErrCancelled", err)
	}
	info := e.waitStatus(room, rec, api.TranscriptFailed)
	if info.Error != "Transcription was stopped on the machine before it finished. Try again, and if it keeps stopping, check the room owner's machine in the moil app." {
		t.Fatalf("error = %q", info.Error)
	}
	// Nothing was submitted again, and the machine, idle, was offered
	// nothing more.
	e.service.Nudge()
	e.reconciled()
	machine.Sync()
	if offers := machine.Offers(); len(offers) != 1 {
		t.Fatalf("the machine was offered %d jobs", len(offers))
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

// cancelUnasked makes a machine end an attempt that hasn't reported
// anything yet as cancelled, which only an attempt the service cancelled
// may do: a done message, outcome cancelled, with the attempt's first
// sequence number, sent raw, since moiltest sends one only when the
// service cancels. The attempt then fails in the fake machine too, with a
// done moil takes for a replay of the first, so that the machine is idle
// again.
func cancelUnasked(m *moiltest.Machine, a *moiltest.Attempt) {
	m.Send(map[string]any{"type": "done", "job_id": a.JobID, "attempt": a.Number, "seq": 1, "outcome": "cancelled"})
	a.Fail(moil.CodeInternal, "the machine ended the attempt as cancelled")
	a.WaitAcked()
}

// A machine's error goes into klisi's log quoted, on one line: a message
// with line breaks can't forge lines of klisi's own.
func TestAMachinesErrorCantForgeLogLines(t *testing.T) {
	logs := &lockedBuffer{}
	previous := log.Writer()
	log.SetOutput(io.MultiWriter(previous, logs))
	t.Cleanup(func() { log.SetOutput(previous) })
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

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// A machine that takes a job and lets it go before it starts, which moil
// lets it do again and again, costs klisi no new staging keys each time:
// taking the job again within ten minutes, it is handed the directory
// klisi made for it before, queued for removal once, with URLs that expire
// when the first did. Later, it gets a new one.
func TestAMachineThatKeepsLettingAJobGoGetsTheSameStagingKeys(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	presign := e.s3.Hold(opPresign)
	rec := e.record(room, time.Now())
	prepared := e.clock.Now()

	// The machine disconnects while klisi prepares its attempt...
	presign.Entered(t)
	machine.Disconnect()
	machine.Connect()
	// ...then takes it again, and pauses while klisi prepares it.
	presign.Entered(t)
	machine.SetState(moil.Paused)
	machine.Sync()
	presign.Release()
	if staged := e.queuedStaging(); len(staged) != 2 {
		t.Fatalf("staging keys queued after klisi prepared the job twice: %q", staged)
	}

	// Five minutes later it takes the job for good.
	e.clock.Advance(5 * time.Minute)
	machine.SetState(moil.Idle)
	a := machine.NextAttempt()
	staged := e.queuedStaging()
	if len(staged) != 2 {
		t.Fatalf("staging keys queued after klisi prepared the job three times: %q", staged)
	}
	for name, output := range a.Outputs {
		if key := e.s3.Key(t, output.URL); !slices.Contains(staged, key) {
			t.Fatalf("the attempt uploads %s to %s, not a key queued for removal: %q", name, key, staged)
		}
		if expires := e.s3.Expires(t, output.URL); !expires.Equal(prepared.Add(a.Timeout + 15*time.Minute)) {
			t.Fatalf("its %s URL expires at %v, %v after klisi first prepared the job", name, expires, expires.Sub(prepared))
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
	presign.Release()
	studio.Connect()
	a := studio.NextAttempt()
	staged := e.queuedStaging()
	if len(staged) != 4 {
		t.Fatalf("staging keys queued after klisi prepared the job for two machines: %q", staged)
	}
	// Two of them are the studio's, the other two the laptop's.
	dir := path.Dir(e.s3.Key(t, a.Outputs["transcript.txt"].URL))
	if studios := slices.DeleteFunc(slices.Clone(staged), func(key string) bool { return path.Dir(key) != dir }); len(studios) != 2 {
		t.Fatalf("the studio uploads to %s; queued: %q", dir, staged)
	}
	finish(t, a, 2)
	e.waitStatus(room, rec, api.TranscriptCompleted)
}

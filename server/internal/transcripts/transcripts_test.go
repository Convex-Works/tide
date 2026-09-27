package transcripts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"mime"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	"klisi/internal/api"
	"klisi/internal/transcripts"
)

// The transcribe bundle's hash. Machine owners approve the bundle by this
// hash in the moil app, and a machine takes transcript jobs only while its
// owner's approval matches it. Any change to a byte of bundle/ changes the
// hash, and then every owner must read and approve the new bundle before
// their machines transcribe anything again; until they do, transcripts
// wait. So change the bundle only on purpose, and update this pin with it.
const pinnedBundleHash = "42d30e3fb23030627d993330b1e2917a0ac2f4abef567fc6e82328d8acedeae6"

func TestBundleHashIsPinned(t *testing.T) {
	bundle, err := transcripts.Bundle()
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Hash() != pinnedBundleHash {
		t.Fatalf("the transcribe bundle's hash is %s, pinned %s: every machine owner would have to approve it again", bundle.Hash(), pinnedBundleHash)
	}
}

// A recording that ends after its owner paired a machine is transcribed on
// that machine, from the recording's own bytes, and its transcript
// downloads beside it.
func TestTranscribesRecordingOnOwnersMachine(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())

	a := machine.NextAttempt()
	if a.JobID != "recording-"+rec.ID || a.Title != "Standup" || a.BundleHash != pinnedBundleHash {
		t.Fatalf("attempt = job %q, title %q, bundle %s", a.JobID, a.Title, a.BundleHash)
	}
	// The job's files: the recording in, the two sidecars out. Its time
	// limit is an hour plus twice the recording's ten minutes.
	if names := slices.Sorted(maps.Keys(a.Inputs)); !slices.Equal(names, []string{"recording.ogg"}) {
		t.Fatalf("inputs = %v", names)
	}
	if names := slices.Sorted(maps.Keys(a.Outputs)); !slices.Equal(names, []string{"transcript.txt", "transcript.vtt"}) {
		t.Fatalf("outputs = %v", names)
	}
	if a.Timeout != time.Hour+20*time.Minute {
		t.Fatalf("timeout = %v", a.Timeout)
	}
	audio, _ := e.s3.Object(*rec.S3Key)
	if got := a.Input("recording.ogg"); !bytes.Equal(got, audio) {
		t.Fatalf("the machine downloaded %q, not the recording %q", got, audio)
	}
	if row, ok := e.row(rec); !ok || row.Status != "pending" {
		t.Fatalf("row while the machine works = %+v, %t", row, ok)
	}

	a.Phase("running")
	a.Progress(0.5, "transcribed 00:05:00 of 00:10:00")
	e.waitTranscript(room, rec, "halfway", func(info *api.TranscriptInfo) bool {
		return info != nil && info.Status == api.TranscriptRunning && info.Progress != nil && *info.Progress == 0.5 &&
			info.Message == "Transcribed 00:05:00 of 00:10:00"
	})
	a.Phase("uploading")
	e.waitTranscript(room, rec, "uploading", func(info *api.TranscriptInfo) bool {
		return info != nil && info.Status == api.TranscriptRunning && info.Message == "Uploading the transcript"
	})

	txt, vtt := finish(t, a, 3)
	info := e.waitStatus(room, rec, api.TranscriptCompleted)
	if info.Speakers == nil || *info.Speakers != 3 || info.Error != "" {
		t.Fatalf("completed transcript = %+v", info)
	}
	// The sidecars sit beside the recording, under its basename.
	txtKey, vttKey := sidecars(rec)
	recordingName := strings.TrimSuffix(*rec.S3Key, ".ogg")
	if txtKey != recordingName+".txt" || vttKey != recordingName+".vtt" {
		t.Fatalf("sidecars of %q = %q, %q", *rec.S3Key, txtKey, vttKey)
	}
	if got, _ := e.s3.Object(txtKey); !bytes.Equal(got, txt) {
		t.Fatalf("stored txt = %q", got)
	}
	if got, _ := e.s3.Object(vttKey); !bytes.Equal(got, vtt) {
		t.Fatalf("stored vtt = %q", got)
	}

	for _, test := range []struct {
		format, contentType string
		body                []byte
	}{{"vtt", "text/vtt; charset=utf-8", vtt}, {"txt", "text/plain; charset=utf-8", txt}} {
		response := e.download(rec, session("alice"), test.format)
		if response.Code != http.StatusFound {
			t.Fatalf("download %s: %d %s", test.format, response.Code, response.Body)
		}
		location := response.Header().Get("Location")
		got, header := get(t, location)
		if !bytes.Equal(got, test.body) || header.Get("Content-Type") != test.contentType {
			t.Fatalf("download %s = %q as %q", test.format, got, header.Get("Content-Type"))
		}
		// It saves as a file named like the recording.
		_, params, err := mime.ParseMediaType(header.Get("Content-Disposition"))
		if err != nil || params["filename"] != pathBase(recordingName)+"."+test.format {
			t.Fatalf("download %s disposition = %q", test.format, header.Get("Content-Disposition"))
		}
		// And the URL lasts five minutes.
		e.clock.Advance(5*time.Minute + time.Second)
		if status := statusOf(t, http.MethodGet, location, nil); status != http.StatusForbidden {
			t.Fatalf("download URL after five minutes: %d", status)
		}
	}
}

// Nobody but the room's owner is ever offered the job, however willing
// their machine.
func TestOnlyOwnersMachinesAreOffered(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	bob := e.machine("bob")
	alice := e.machine("alice")
	alice.Disconnect()
	rec := e.record(room, time.Now())

	// The job waits for Alice's machine, with Bob's connected, idle and
	// approved.
	e.submitted(rec)
	e.waitTranscript(room, rec, "waiting for alice's machine", func(info *api.TranscriptInfo) bool {
		return info != nil && info.Status == api.TranscriptWaiting && info.Message == "Waiting for a paired machine to come online."
	})
	bob.Sync()
	if offers := bob.Offers(); len(offers) != 0 {
		t.Fatalf("bob's machine was offered %+v", offers)
	}

	alice.Connect()
	a := alice.NextAttempt()
	bob.Sync()
	if offers := bob.Offers(); len(offers) != 0 {
		t.Fatalf("bob's machine was offered %+v", offers)
	}
	finish(t, a, 2)
	e.waitStatus(room, rec, api.TranscriptCompleted)
}

// A restart loses moil's jobs, not the transcripts: klisi submits the
// pending ones again, and the machine that was working on one starts over
// once it reconnects. The row never ends in between.
func TestRestartResubmitsPendingTranscripts(t *testing.T) {
	for _, test := range []struct {
		name string
		stop func(*env)
	}{
		{"service stops first", func(e *env) { e.stop() }},
		// The run ends with moil.ErrClosed while the service still follows it.
		{"moil closes first", func(e *env) { _ = e.moil.Close(); e.stopService() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := newEnv(t)
			room := e.room("alice", "Standup")
			machine := e.machine("alice")
			rec := e.record(room, time.Now())
			first := machine.NextAttempt()
			first.Progress(0.3, "finding speakers")
			e.waitStatus(room, rec, api.TranscriptRunning)

			test.stop(e)
			machine.WaitClosed()
			if row, ok := e.row(rec); !ok || row.Status != "pending" {
				t.Fatalf("row after klisi stopped = %+v, %t", row, ok)
			}

			e.start()
			e.submitted(rec)
			if row, ok := e.row(rec); !ok || row.Status != "pending" {
				t.Fatalf("row after klisi started = %+v, %t", row, ok)
			}
			machine.Connect()
			second := machine.NextAttempt()
			if !first.Abandoned() {
				t.Fatal("the machine kept the attempt the old moil server gave it")
			}
			if second.JobID != first.JobID {
				t.Fatalf("resubmitted job %q, was %q", second.JobID, first.JobID)
			}
			if row, ok := e.row(rec); !ok || row.Status != "pending" {
				t.Fatalf("row while the job runs again = %+v, %t", row, ok)
			}
			audio, _ := e.s3.Object(*rec.S3Key)
			if got := second.Input("recording.ogg"); !bytes.Equal(got, audio) {
				t.Fatalf("input after the restart = %q", got)
			}
			finish(t, second, 2)
			if info := e.waitStatus(room, rec, api.TranscriptCompleted); info.Speakers == nil || *info.Speakers != 2 {
				t.Fatalf("transcript = %+v", info)
			}
		})
	}
}

// A job can wait far longer than any URL could last for its owner's laptop
// to wake: the URLs are made when a machine takes an attempt, and last for
// the attempt's time limit plus 15 minutes.
func TestURLsAreMintedWhenAMachineTakesTheJob(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.pair("alice")
	machine.Approve(e.bundle)
	rec := e.record(room, time.Now())
	e.submitted(rec)

	// Ten days: longer than S3 lets any URL last.
	e.clock.Advance(10 * 24 * time.Hour)
	machine.Connect()
	a := machine.NextAttempt()
	audio, _ := e.s3.Object(*rec.S3Key)
	if got := a.Input("recording.ogg"); !bytes.Equal(got, audio) {
		t.Fatalf("input = %q", got)
	}

	e.clock.Advance(a.Timeout + 15*time.Minute - time.Second)
	a.Output("transcript.txt", []byte("still in time"))
	e.clock.Advance(2 * time.Second)
	if status := statusOf(t, http.MethodGet, a.Inputs["recording.ogg"].URL, nil); status != http.StatusForbidden {
		t.Fatalf("input URL after the attempt's time: %d", status)
	}
	if status := statusOf(t, http.MethodPut, a.Outputs["transcript.vtt"].URL, []byte("too late")); status != http.StatusForbidden {
		t.Fatalf("output URL after the attempt's time: %d", status)
	}

	// The next attempt gets URLs of its own, and starts from scratch.
	a.Progress(0.4, "finding speakers")
	e.waitTranscript(room, rec, "40% done", func(info *api.TranscriptInfo) bool {
		return info != nil && info.Progress != nil && *info.Progress == 0.4
	})
	a.Fail(moil.CodeInputDownload, "the URL expired")
	retry := machine.NextAttempt()
	e.waitTranscript(room, rec, "starting again", func(info *api.TranscriptInfo) bool {
		return info != nil && info.Status == api.TranscriptRunning && info.Message == "Starting" && info.Progress == nil
	})
	if got := retry.Input("recording.ogg"); !bytes.Equal(got, audio) {
		t.Fatalf("input of the retry = %q", got)
	}
	finish(t, retry, 1)
	e.waitStatus(room, rec, api.TranscriptCompleted)
}

// Deleting a recording stops its transcript, and leaves none of its files
// behind, even ones uploaded after the deletion.
func TestDeletingARecordingStopsItsTranscript(t *testing.T) {
	t.Run("the running job is cancelled", func(t *testing.T) {
		e := newEnv(t)
		room := e.room("alice", "Standup")
		machine := e.machine("alice")
		rec := e.record(room, time.Now())
		a := machine.NextAttempt()

		if response := e.deleteRecording(rec, session("alice")); response.Code != http.StatusNoContent {
			t.Fatalf("delete: %d %s", response.Code, response.Body)
		}
		a.WaitCancelled()
		a.WaitAcked()
		if _, ok := e.row(rec); ok {
			t.Fatal("the transcript outlived its recording")
		}
		if keys := e.s3.Keys("recordings/"); len(keys) != 0 {
			t.Fatalf("files left: %v", keys)
		}
		// The machine is free for other work.
		next := e.record(room, time.Now())
		b := machine.NextAttempt()
		if b.JobID != "recording-"+next.ID {
			t.Fatalf("next attempt = %s", b.JobID)
		}
	})

	t.Run("a job that succeeds anyway leaves nothing behind", func(t *testing.T) {
		e := newEnv(t)
		room := e.room("alice", "Standup")
		machine := e.machine("alice")
		rec := e.record(room, time.Now())
		a := machine.NextAttempt()
		a.GoSilent() // the machine won't hear the cancel in time

		if response := e.deleteRecording(rec, session("alice")); response.Code != http.StatusNoContent {
			t.Fatalf("delete: %d %s", response.Code, response.Body)
		}
		a.WaitCancelled()
		// The URLs outlive the recording: the machine uploads anyway.
		a.Output("transcript.txt", []byte("late"))
		a.Output("transcript.vtt", []byte("WEBVTT late"))
		if keys := e.s3.Keys("recordings/"); len(keys) != 2 {
			t.Fatalf("uploaded after the deletion: %v", keys)
		}
		a.Succeed(map[string]any{"speakers": 2})
		waitFor(t, "the late sidecars to be removed", func() bool { return len(e.s3.Keys("recordings/")) == 0 })
		if _, ok := e.row(rec); ok {
			t.Fatal("the transcript outlived its recording")
		}
	})

	// The reconciler's tick is an hour away: only the room's deletion
	// telling it can stop the job in time.
	t.Run("deleting its room cancels the running job at once", func(t *testing.T) {
		e := newEnv(t)
		room := e.room("alice", "Standup")
		machine := e.machine("alice")
		rec := e.record(room, time.Now())
		a := machine.NextAttempt()
		a.Progress(0.1, "finding speakers")

		if response := e.deleteRoom(room, session("alice")); response.Code != http.StatusNoContent {
			t.Fatalf("delete the room: %d %s", response.Code, response.Body)
		}
		a.WaitCancelled()
		a.WaitAcked()
		if _, ok := e.row(rec); ok {
			t.Fatal("the transcript outlived its room")
		}
		if keys := e.s3.Keys("recordings/"); len(keys) != 0 {
			t.Fatalf("files left: %v", keys)
		}
	})

	// A room deleted behind the service's back (no nudge) leaves the job
	// waiting when a machine comes for it.
	t.Run("a waiting job whose room was deleted gives a machine nothing", func(t *testing.T) {
		e := newEnv(t)
		room := e.room("alice", "Standup")
		machine := e.machine("alice")
		machine.Disconnect()
		rec := e.record(room, time.Now())
		e.submitted(rec)
		run, _ := e.moil.Run("recording-" + rec.ID)

		// What the room's deletion does, without telling the service.
		if _, err := e.db.DeleteRoom(context.Background(), room.ID, e.clock.Now().Unix()); err != nil {
			t.Fatal(err)
		}
		machine.Connect()
		if offer := machine.NextOffer(); offer.JobID != run.ID() {
			t.Fatalf("offer of %s", offer.JobID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()
		if _, err := run.Wait(ctx); err == nil || !strings.Contains(err.Error(), "the recording was deleted") {
			t.Fatalf("the job ended with %v", err)
		}
		// No attempt was made: the machine's next one is another job's.
		next := e.record(e.room("alice", "Retro"), time.Now())
		if a := machine.NextAttempt(); a.JobID != "recording-"+next.ID {
			t.Fatalf("next attempt = %s", a.JobID)
		}
	})
}

// A failed transcript says why, and requesting it again retries it.
func TestFailedTranscriptRetries(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())

	a := machine.NextAttempt()
	a.ScriptError("can't read the recording: Invalid data found when processing input", false)
	info := e.waitStatus(room, rec, api.TranscriptFailed)
	if info.Error != "Transcription failed: can't read the recording: Invalid data found when processing input." {
		t.Fatalf("error = %q", info.Error)
	}

	response := e.requestTranscript(rec, session("alice"))
	if response.Code != http.StatusAccepted {
		t.Fatalf("retry: %d %s", response.Code, response.Body)
	}
	var requested api.TranscriptInfo
	if err := json.NewDecoder(response.Body).Decode(&requested); err != nil || requested.Status != api.TranscriptWaiting {
		t.Fatalf("retry answered %+v, %v", requested, err)
	}
	retry := machine.NextAttempt()
	if retry.JobID != a.JobID {
		t.Fatalf("retry is job %s", retry.JobID)
	}
	finish(t, retry, 4)
	info = e.waitStatus(room, rec, api.TranscriptCompleted)
	if info.Speakers == nil || *info.Speakers != 4 || info.Error != "" {
		t.Fatalf("transcript after the retry = %+v", info)
	}
	if response := e.requestTranscript(rec, session("alice")); response.Code != http.StatusConflict ||
		errorMessage(t, response) != "This recording already has a transcript." {
		t.Fatalf("request of a completed transcript: %d", response.Code)
	}
}

// A machine that reports success without uploading the transcript hasn't
// made one.
func TestSuccessWithoutSidecarsFails(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	machine := e.machine("alice")
	rec := e.record(room, time.Now())

	a := machine.NextAttempt()
	a.Output("transcript.txt", []byte("only the text"))
	a.Succeed(map[string]any{"speakers": 1})
	info := e.waitStatus(room, rec, api.TranscriptFailed)
	if info.Error != "The machine finished without uploading the transcript. Try again." {
		t.Fatalf("error = %q", info.Error)
	}
	if response := e.download(rec, session("alice"), "txt"); response.Code != http.StatusConflict {
		t.Fatalf("download of a failed transcript: %d", response.Code)
	}
}

// Pairing a machine opts into transcripts from then on. A recording that
// ended before stays available until requested, and a waiting transcript
// says what it waits for.
func TestAvailableUntilRequestedAndWhatItWaitsFor(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	early := e.record(room, time.Now().Add(-time.Hour))
	if info := e.transcript(room, early); info != nil {
		t.Fatalf("transcript without a machine = %+v", info)
	}
	response := e.requestTranscript(early, session("alice"))
	if response.Code != http.StatusConflict || !strings.Contains(errorMessage(t, response), "/machines") {
		t.Fatalf("request without a machine: %d", response.Code)
	}

	machine := e.pair("alice")
	later := e.record(room, time.Now())
	waitFor(t, "the later recording's transcript to be created", func() bool { _, ok := e.row(later); return ok })
	if _, ok := e.row(early); ok {
		t.Fatal("a recording that ended before the machine was paired got a transcript unasked")
	}
	if info := e.transcript(room, early); info == nil || info.Status != api.TranscriptAvailable {
		t.Fatalf("early transcript = %+v", info)
	}

	waiting := func(message string) {
		t.Helper()
		e.waitTranscript(room, later, "waiting: "+message, func(info *api.TranscriptInfo) bool {
			return info != nil && info.Status == api.TranscriptWaiting && info.Message == message
		})
	}
	const notApproved = "No paired machine has approved the transcriber yet. Approve it in the moil app."
	waiting(notApproved)
	machine.Connect()
	machine.Sync()
	waiting(notApproved)
	machine.SetState(moil.Paused)
	machine.Approve(e.bundle)
	machine.Sync()
	waiting("The paired machines are paused. Resume one in the moil app.")
	machine.Disconnect()
	waiting("Waiting for a paired machine to come online.")
	machine.SetState(moil.Busy)
	machine.Connect()
	machine.Sync()
	waiting("Waiting for a paired machine to finish its current job.")

	machine.SetState(moil.Idle)
	a := machine.NextAttempt()
	if a.JobID != "recording-"+later.ID {
		t.Fatalf("attempt of %s", a.JobID)
	}
	a.Phase("downloading")
	e.waitTranscript(room, later, "downloading", func(info *api.TranscriptInfo) bool {
		return info != nil && info.Status == api.TranscriptRunning && info.Message == "Downloading the recording" && info.Progress == nil
	})
	a.Phase("running")
	a.Progress(0.25, "finding speakers")
	e.waitTranscript(room, later, "a quarter done", func(info *api.TranscriptInfo) bool {
		return info != nil && info.Status == api.TranscriptRunning && info.Message == "Finding speakers" &&
			info.Progress != nil && *info.Progress == 0.25
	})

	// Requested now, the early one waits for the machine to be done.
	response = e.requestTranscript(early, session("alice"))
	if response.Code != http.StatusAccepted {
		t.Fatalf("request: %d %s", response.Code, response.Body)
	}
	waitFor(t, "the early transcript to be pending", func() bool { row, ok := e.row(early); return ok && row.Status == "pending" })
	if info := e.transcript(room, early); info.Status != api.TranscriptWaiting ||
		info.Message != "Waiting for a paired machine to finish its current job." {
		t.Fatalf("early transcript while the machine is busy = %+v", info)
	}
	if response := e.requestTranscript(early, session("alice")); response.Code != http.StatusConflict ||
		errorMessage(t, response) != "This recording's transcript is already on its way." {
		t.Fatalf("second request: %d", response.Code)
	}
	finish(t, a, 2)
	b := machine.NextAttempt()
	if b.JobID != "recording-"+early.ID {
		t.Fatalf("attempt of %s", b.JobID)
	}
	finish(t, b, 3)
	e.waitStatus(room, later, api.TranscriptCompleted)
	e.waitStatus(room, early, api.TranscriptCompleted)
}

// Only the room's managers reach its transcripts. An administrator can ask
// for one, but it is still made on the room owner's machine.
func TestTranscriptRoutesNeedARoomManager(t *testing.T) {
	e := newEnv(t)
	room := e.room("alice", "Standup")
	rec := e.record(room, time.Now().Add(-time.Hour))
	alice := e.machine("alice")
	root := e.machine("root") // the administrator's own machine

	checks := []struct {
		name   string
		status int
		call   func() int
	}{
		{"request signed out", http.StatusUnauthorized, func() int { return e.requestTranscript(rec, nil).Code }},
		{"request by another host", http.StatusForbidden, func() int { return e.requestTranscript(rec, session("mallory")).Code }},
		{"download signed out", http.StatusUnauthorized, func() int { return e.download(rec, nil, "txt").Code }},
		{"download by another host", http.StatusForbidden, func() int { return e.download(rec, session("mallory"), "txt").Code }},
	}
	for _, check := range checks {
		if got := check.call(); got != check.status {
			t.Errorf("%s: %d, want %d", check.name, got, check.status)
		}
	}
	if _, ok := e.row(rec); ok {
		t.Fatal("a refused request created a transcript")
	}

	if response := e.requestTranscript(rec, admin); response.Code != http.StatusAccepted {
		t.Fatalf("administrator's request: %d %s", response.Code, response.Body)
	}
	a := alice.NextAttempt()
	root.Sync()
	if offers := root.Offers(); len(offers) != 0 {
		t.Fatalf("the administrator's machine was offered %+v", offers)
	}
	if response := e.download(rec, admin, "vtt"); response.Code != http.StatusConflict {
		t.Fatalf("download before it's done: %d", response.Code)
	}
	finish(t, a, 2)
	e.waitStatus(room, rec, api.TranscriptCompleted)

	if response := e.download(rec, session("mallory"), "vtt"); response.Code != http.StatusForbidden {
		t.Fatalf("download by another host: %d", response.Code)
	}
	if response := e.download(rec, admin, "json"); response.Code != http.StatusBadRequest {
		t.Fatalf("download as json: %d", response.Code)
	}
	if response := e.download(rec, admin, "vtt"); response.Code != http.StatusFound {
		t.Fatalf("administrator's download: %d %s", response.Code, response.Body)
	}
}

func get(t *testing.T, location string) ([]byte, http.Header) {
	t.Helper()
	response, err := http.Get(location)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %s %v", location, response.StatusCode, body, err)
	}
	return body, response.Header
}

func statusOf(t *testing.T, method, location string, body []byte) int {
	t.Helper()
	request, err := http.NewRequest(method, location, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return response.StatusCode
}

func pathBase(key string) string { return key[strings.LastIndex(key, "/")+1:] }

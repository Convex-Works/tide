package e2e

import (
	"bytes"
	"mime"
	"net/http"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"klisi/internal/api"
)

// The stub the tests publish is a bundle moil accepts: its lockfile is
// current, and the moil binary hashes it as the Go SDK does, so machines
// approve exactly what klisi offers.
func TestTheStubIsABundleMoilAccepts(t *testing.T) {
	t.Parallel()
	needSuite(t)
	m := newMachine(t, "author")
	if out := m.moil("check", "testdata/transcribe-stub"); !strings.Contains(out, "ok: the bundle is valid") {
		t.Fatalf("moil check:\n%s", out)
	}
	if hash := strings.TrimSpace(m.moil("hash", "testdata/transcribe-stub")); hash != stubBundle(t).Hash() {
		t.Fatalf("moil hashes the stub %s, the SDK %s", hash, stubBundle(t).Hash())
	}
}

// A host pairs their computer with the moil command line and approves the
// bundle klisi publishes; from then on their recordings are transcribed on
// it, from the recording's own bytes, and the transcript downloads beside
// the recording. Deleting the recording deletes its transcript, and
// unpairing the machine cuts it off, even mid-job.
func TestAHostPairsAMachineAndGetsTranscripts(t *testing.T) {
	t.Parallel()
	k := startKlisi(t)
	alice := k.signIn("alice")
	standup := alice.createRoom("Standup")
	laptop := newMachine(t, "alice-laptop")

	// Pairing: `moil pair` shows a code, alice finds her laptop under it
	// and confirms, and `moil pair` succeeds.
	laptop.Pair(alice)
	page := alice.machines()
	if len(page.Machines) != 1 || page.Machines[0].ID != laptop.id {
		t.Fatalf("alice's machines = %+v", page.Machines)
	}
	if machine := page.Machines[0]; machine.Name != "alice-laptop" || machine.State != "offline" || machine.Approved {
		t.Fatalf("paired machine = %+v, want offline and not approved", machine)
	}
	stub := k.bundle
	if want := (api.BundleInfo{Name: "transcribe-e2e", Version: stub.Version(), Hash: stub.Hash()}); page.Bundle != want {
		t.Fatalf("/machines names the bundle %+v, klisi publishes %+v", page.Bundle, want)
	}
	// The machine sees that very bundle, waiting for its owner.
	if status := laptop.Review(page.Bundle.Hash); status != "not approved" {
		t.Fatalf("moil review says the bundle is %q", status)
	}
	laptop.Approve(page.Bundle.Hash)
	laptop.Start()
	alice.waitForMachine(laptop.id, "idle and approved", func(machine api.MachineInfo) bool {
		return machine.State == "idle" && machine.Approved && machine.LastSeenAt != nil
	})

	// A meeting is recorded. A forged egress_ended changes nothing.
	rec := k.startRecording(standup, "alice")
	if status := k.webhook(rec.ended(), "not LiveKit's secret, but long enough"); status != http.StatusUnauthorized {
		t.Fatalf("forged egress_ended: %d", status)
	}
	if status := k.recordingStatus(rec); status != "recording" {
		t.Fatalf("the recording is %q after a forged egress_ended", status)
	}
	if info := alice.transcript(rec); info != nil {
		t.Fatalf("transcript of a recording in progress = %+v", info)
	}

	// LiveKit's own egress_ended sends the recording to alice's laptop,
	// which is held mid-job while the dashboard shows it running.
	laptop.Hold()
	k.endRecording(rec)
	alice.waitTranscript(rec, "running on the laptop", func(info *api.TranscriptInfo) bool {
		return info != nil && info.Status == api.TranscriptRunning && info.Message == "Waiting for the gate to open" &&
			info.Progress != nil && *info.Progress == 0.5
	})
	if attempts := laptop.Attempts(); len(attempts) != 1 || attempts[0].JobID != rec.jobID() {
		t.Fatalf("the laptop ran %+v, want one attempt of %s", attempts, rec.jobID())
	}
	laptop.Release()
	info := alice.waitStatus(rec, api.TranscriptCompleted)
	if info.Speakers == nil || *info.Speakers != 2 || info.Error != "" {
		t.Fatalf("completed transcript = %+v", info)
	}
	laptop.Logged(0, laptop.attemptKey(rec, 1)+" succeeded")

	// The transcript downloads from S3, named like the recording, and
	// shows the laptop read exactly the recording's bytes.
	name := strings.TrimSuffix(path.Base(rec.key), ".ogg")
	txt, header := alice.downloadTranscript(rec, "txt")
	if !bytes.Contains(txt, []byte(rec.transcriptLine())) || !bytes.HasPrefix(txt, []byte("[00:00:00] Speaker 1: ")) {
		t.Fatalf("the transcript of a recording with SHA-256 %s says:\n%s", rec.sum, txt)
	}
	checkDownload(t, header, name+".txt", "text/plain; charset=utf-8")
	vtt, header := alice.downloadTranscript(rec, "vtt")
	if !bytes.HasPrefix(vtt, []byte("WEBVTT\n")) || !bytes.Contains(vtt, []byte(rec.transcriptLine())) {
		t.Fatalf("the captions say:\n%s", vtt)
	}
	checkDownload(t, header, name+".vtt", "text/vtt; charset=utf-8")

	// The recording and its sidecars are all under its prefix, and
	// deleting the recording removes them all.
	prefix := path.Dir(rec.key) + "/"
	want := []string{rec.key, rec.transcriptKey("txt"), rec.transcriptKey("vtt")}
	slices.Sort(want)
	if keys := objects.keys(t, prefix); !slices.Equal(keys, want) {
		t.Fatalf("stored under %s: %q, want %q", prefix, keys, want)
	}
	alice.call(http.MethodDelete, fill(api.RecordingPath, rec.id), nil, http.StatusNoContent, nil)
	if keys := objects.keys(t, prefix); len(keys) != 0 {
		t.Fatalf("left under %s after deleting the recording: %q", prefix, keys)
	}
	if _, listed := alice.recordings(standup)[rec.id]; listed {
		t.Fatal("the deleted recording is still listed")
	}

	// Unpairing the laptop mid-job cuts it off: the job stops, the agent
	// is told it was removed and stays away, and moil says so.
	laptop.Hold()
	next := k.record(standup, "alice")
	alice.waitTranscript(next, "running on the laptop", func(info *api.TranscriptInfo) bool {
		return info != nil && info.Status == api.TranscriptRunning && info.Message == "Waiting for the gate to open"
	})
	attempts := laptop.Attempts()
	if len(attempts) != 2 || attempts[1].JobID != next.jobID() {
		t.Fatalf("the laptop ran %+v, want %s last", attempts, next.jobID())
	}
	running := attempts[1]
	unpaired := laptop.agentLog.len()
	alice.call(http.MethodDelete, fill(api.MachinePath, laptop.id), nil, http.StatusNoContent, nil)
	laptop.Logged(unpaired, "klisi removed this machine; pair it again")
	awaitGone(t, "the unpaired laptop's job", running.PID)
	if machines := alice.machines().Machines; len(machines) != 0 {
		t.Fatalf("alice's machines after unpairing = %+v", machines)
	}
	if info := alice.transcript(next); info == nil || info.Status != api.TranscriptWaiting ||
		info.Message != "No machine is paired to transcribe it." {
		t.Fatalf("transcript after unpairing its machine = %+v", info)
	}
	if _, stderr, err := laptop.try("review", laptop.serviceID); err == nil ||
		!strings.Contains(stderr, "the service no longer accepts this machine; pair it again") {
		t.Fatalf("moil review after unpairing: %v\n%s", err, stderr)
	}
	// Given the time to reconnect, and a job it would take, it doesn't.
	laptop.Release()
	time.Sleep(reconnectWindow)
	if line, ok := laptop.agentLog.find(unpaired, "connected to klisi"); ok {
		t.Fatalf("the unpaired laptop connected again: %s", line)
	}
	if attempts := laptop.Attempts(); len(attempts) != 2 {
		t.Fatalf("the unpaired laptop started %+v", attempts[2:])
	}
	if keys := objects.keys(t, path.Dir(next.key)+"/"); !slices.Equal(keys, []string{next.key}) {
		t.Fatalf("stored for the recording after unpairing: %q", keys)
	}
	if info := alice.transcript(next); info == nil || info.Status != api.TranscriptWaiting {
		t.Fatalf("transcript after unpairing its machine = %+v", info)
	}
}

// reconnectWindow is long enough for an agent to try to reconnect after
// losing its channel: it tries again within a second (moil spec §6.1).
const reconnectWindow = 2 * time.Second

// A restart loses moil's jobs, not the transcripts. klisi stops as main
// does while a machine is on a transcript; the new klisi, on the same
// database and address, submits it again, the machine drops the attempt
// the old one gave it and runs the new one, and the transcript completes.
// Its row stays pending throughout.
func TestTranscriptsSurviveAKlisiRestart(t *testing.T) {
	t.Parallel()
	k := startKlisi(t)
	alice := k.signIn("alice")
	retro := alice.createRoom("Retro")
	laptop := lentMachine(t, alice, "alice-laptop")

	laptop.Hold()
	rec := k.startRecording(retro, "alice")
	statuses := watchTranscriptRow(t, k, rec)
	k.endRecording(rec)
	alice.waitTranscript(rec, "running on the laptop", func(info *api.TranscriptInfo) bool {
		return info != nil && info.Status == api.TranscriptRunning && info.Message == "Waiting for the gate to open"
	})
	attempts := laptop.Attempts()
	if len(attempts) != 1 || attempts[0].JobID != rec.jobID() {
		t.Fatalf("the laptop ran %+v, want one attempt of %s", attempts, rec.jobID())
	}
	first := attempts[0]

	restarted := laptop.agentLog.len()
	k.Restart()
	laptop.Logged(restarted, "no longer expects "+laptop.attemptKey(rec, 1)+"; abandoning it")
	awaitGone(t, "the attempt the old klisi gave", first.PID)
	laptop.Logged(restarted, "connected to klisi")
	alice.waitTranscript(rec, "running on the laptop again", func(info *api.TranscriptInfo) bool {
		return info != nil && info.Status == api.TranscriptRunning && info.Message == "Waiting for the gate to open"
	})
	attempts = laptop.Attempts()
	if len(attempts) != 2 || attempts[1].JobID != rec.jobID() || attempts[1].PID == first.PID {
		t.Fatalf("the laptop ran %+v, want %s again", attempts, rec.jobID())
	}

	laptop.Release()
	info := alice.waitStatus(rec, api.TranscriptCompleted)
	if info.Speakers == nil || *info.Speakers != 2 {
		t.Fatalf("completed transcript = %+v", info)
	}
	if txt, _ := alice.downloadTranscript(rec, "txt"); !bytes.Contains(txt, []byte(rec.transcriptLine())) {
		t.Fatalf("the transcript of a recording with SHA-256 %s says:\n%s", rec.sum, txt)
	}
	if seen := statuses(); !slices.Equal(seen, []string{"pending", "completed"}) {
		t.Fatalf("the transcript row was %q", seen)
	}
}

// A transcript runs only on the room owner's machines. Bob's machine is
// paired, approved and idle while alice's recording waits for her laptop
// to come online, and never gets it; it transcribes bob's own recordings.
func TestOnlyTheOwnersMachinesGetTheJob(t *testing.T) {
	t.Parallel()
	k := startKlisi(t)
	alice, bob := k.signIn("alice"), k.signIn("bob")
	standup := alice.createRoom("Standup")
	// klisi learns what a machine approved when it connects, so alice's
	// laptop was online once since she approved the bundle. It isn't now.
	aliceLaptop := lentMachine(t, alice, "alice-laptop")
	aliceLaptop.Stop()
	alice.waitForMachine(aliceLaptop.id, "offline", func(machine api.MachineInfo) bool {
		return machine.State == "offline" && machine.Approved
	})
	bobDesktop := lentMachine(t, bob, "bob-desktop")

	rec := k.record(standup, "alice")
	const offline = "Waiting for a paired machine to come online."
	alice.waitTranscript(rec, "waiting for alice's laptop", func(info *api.TranscriptInfo) bool {
		return info != nil && info.Status == api.TranscriptWaiting && info.Message == offline
	})
	// Bob's machine would bid at once if it were offered the job, and the
	// job waits well past the time moil gives machines to bid.
	time.Sleep(bidWindow + time.Second)
	if info := alice.transcript(rec); info == nil || info.Status != api.TranscriptWaiting || info.Message != offline {
		t.Fatalf("alice's transcript while her laptop is offline = %+v", info)
	}
	if machine := bob.machine(bobDesktop.id); machine.State != "idle" || !machine.Approved {
		t.Fatalf("bob's desktop = %+v, want idle and approved", machine)
	}

	aliceLaptop.Start()
	alice.waitStatus(rec, api.TranscriptCompleted)
	if txt, _ := alice.downloadTranscript(rec, "txt"); !bytes.Contains(txt, []byte(rec.transcriptLine())) {
		t.Fatalf("the transcript of a recording with SHA-256 %s says:\n%s", rec.sum, txt)
	}
	if attempts := aliceLaptop.Attempts(); len(attempts) != 1 || attempts[0].JobID != rec.jobID() {
		t.Fatalf("alice's laptop ran %+v, want %s", attempts, rec.jobID())
	}
	if attempts := bobDesktop.Attempts(); len(attempts) != 0 {
		t.Fatalf("bob's desktop ran %+v", attempts)
	}
	if line, ok := bobDesktop.agentLog.find(0, "starting "); ok {
		t.Fatalf("bob's desktop started a job: %s", line)
	}

	// Bob's desktop was able all along: it transcribes bob's recording, and
	// only it does.
	review := bob.createRoom("Design review")
	bobs := k.record(review, "bob")
	bob.waitStatus(bobs, api.TranscriptCompleted)
	if attempts := bobDesktop.Attempts(); len(attempts) != 1 || attempts[0].JobID != bobs.jobID() {
		t.Fatalf("bob's desktop ran %+v, want %s", attempts, bobs.jobID())
	}
	if attempts := aliceLaptop.Attempts(); len(attempts) != 1 {
		t.Fatalf("alice's laptop ran %+v, want only alice's recording", attempts)
	}
}

// bidWindow is how long moil gives machines to answer an offer: its
// default, which klisi keeps.
const bidWindow = 2 * time.Second

// checkDownload checks a download's headers: a browser saves it as
// filename, served as contentType.
func checkDownload(t *testing.T, header http.Header, filename, contentType string) {
	t.Helper()
	if got := header.Get("Content-Type"); got != contentType {
		t.Errorf("Content-Type = %q, want %q", got, contentType)
	}
	_, params, err := mime.ParseMediaType(header.Get("Content-Disposition"))
	if err != nil || params["filename"] != filename {
		t.Errorf("Content-Disposition = %q, want the file %q", header.Get("Content-Disposition"), filename)
	}
}

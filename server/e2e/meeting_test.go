package e2e

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"klisi/internal/api"
	"klisi/internal/transcripts"
)

// transcriptLine is a line of the real bundle's plain-text transcript:
// "[00:01:02] Speaker 2: Shall we start?".
var transcriptLine = regexp.MustCompile(`^\[\d\d:\d\d:\d\d\] Speaker \d+: \S`)

// TestARealMeetingIsTranscribed runs the transcribe bundle klisi publishes,
// with its real models, on a real meeting recording, from the recording's
// end to the transcript's download. It runs only when KLISI_E2E_MEETING
// names an audio file. The machine's first transcript downloads 2.9 GB of
// models; KLISI_E2E_MOIL_CACHE can name a moil cache that already holds
// them, such as ~/Library/Caches/moil, where `moil run` keeps its own.
// KLISI_E2E_MEETING_SPEAKERS, if set, is how many speakers it must find.
func TestARealMeetingIsTranscribed(t *testing.T) {
	meeting := os.Getenv("KLISI_E2E_MEETING")
	if meeting == "" {
		t.Skip("set KLISI_E2E_MEETING to a meeting recording to transcribe it with the real bundle")
	}
	audio, err := os.ReadFile(meeting)
	if err != nil {
		t.Fatal(err)
	}
	transcribe, err := transcripts.Bundle()
	if err != nil {
		t.Fatal(err)
	}
	k := startKlisiWith(t, transcribe)
	alice := k.signIn("alice")
	studio := newMachine(t, "alice-studio")
	if cache := os.Getenv("KLISI_E2E_MOIL_CACHE"); cache != "" {
		studio.shareCache(cache)
	}
	studio.Pair(alice)
	studio.Approve(transcribe.Hash())
	studio.Start()
	alice.waitForMachine(studio.id, "idle and approved", func(info api.MachineInfo) bool {
		return info.State == api.MachineIdle && info.Approved
	})

	room := alice.createRoom("Design review")
	started := time.Now()
	r := k.startRecordingOf(room, "alice", audio)
	k.endRecording(r)
	info := alice.waitTranscriptFor(r, 20*time.Minute, func(info *api.TranscriptInfo) bool {
		if info != nil && info.Status == api.TranscriptFailed {
			t.Fatalf("the transcript failed: %s", info.Error)
		}
		return info != nil && info.Status == api.TranscriptCompleted
	})
	t.Logf("transcribed %s in %s, finding %d speakers", filepath.Base(meeting), time.Since(started).Round(time.Second), *info.Speakers)
	if want := os.Getenv("KLISI_E2E_MEETING_SPEAKERS"); want != "" {
		if n, _ := strconv.Atoi(want); *info.Speakers != n {
			t.Errorf("found %d speakers, want %d", *info.Speakers, n)
		}
	}

	text, _ := alice.downloadTranscript(r, api.TranscriptFormatText)
	lines := strings.Split(strings.TrimSpace(string(text)), "\n")
	for i, line := range lines {
		if !transcriptLine.MatchString(line) {
			t.Fatalf("transcript line %d isn't a timed, labelled utterance: %q", i+1, line)
		}
	}
	t.Logf("%d utterances; it begins:\n%s", len(lines), strings.Join(lines[:min(len(lines), 8)], "\n"))
	captions, _ := alice.downloadTranscript(r, api.TranscriptFormatVTT)
	if !bytes.HasPrefix(captions, []byte("WEBVTT\n")) || !bytes.Contains(captions, []byte(" --> ")) {
		t.Fatalf("the captions aren't WebVTT cues:\n%.300s", captions)
	}
}

// waitTranscriptFor is waitTranscript with a deadline of its own, for a
// transcript that takes real work.
func (h *host) waitTranscriptFor(r *recording, deadline time.Duration, ok func(*api.TranscriptInfo) bool) *api.TranscriptInfo {
	h.k.t.Helper()
	until := time.Now().Add(deadline)
	for {
		info := h.transcript(r)
		if ok(info) {
			return info
		}
		if time.Now().After(until) {
			h.k.t.Fatalf("gave up after %s waiting for the transcript of %s; it is %+v", deadline, r.id, info)
		}
		time.Sleep(time.Second)
	}
}

// shareCache has the machine keep models and Python environments in the
// moil cache at dir rather than its own, so it needn't download them.
func (m *machine) shareCache(dir string) {
	m.t.Helper()
	cache := filepath.Join(m.home, "cache")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		m.t.Fatal(err)
	}
	for _, name := range []string{"assets", "environments"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			m.t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, name), filepath.Join(cache, name)); err != nil {
			m.t.Fatal(err)
		}
	}
}

package transcripts

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	"klisi/internal/api"
	"klisi/internal/store"
)

// maxMessage bounds, in characters, the messages from machines klisi shows.
const maxMessage = 300

// The errors of a job that succeeded with files klisi won't keep.
const (
	missingOutputs = "The machine finished without uploading the transcript. Try again."
	tooLarge       = "The transcript the machine uploaded is larger than 16 MiB, more than klisi keeps. Try again."
)

// Transcripts implements recording.TranscriptSource: each recording's
// transcript, from its row and, while pending, its job. It reads the room's
// transcripts and its owner's machines once, however many recordings there
// are.
func (s *Service) Transcripts(ctx context.Context, room store.Room, recordings []store.Recording) (map[string]*api.TranscriptInfo, error) {
	if !slices.ContainsFunc(recordings, transcribable) {
		return nil, nil
	}
	rows, err := s.cfg.Store.TranscriptsByRoom(ctx, room.ID)
	if err != nil {
		return nil, err
	}
	machines, err := s.cfg.Moil.Machines(ctx, room.OwnerSub)
	if err != nil {
		return nil, err
	}
	infos := make(map[string]*api.TranscriptInfo)
	for _, recording := range recordings {
		if !transcribable(recording) {
			continue
		}
		row, exists := rows[recording.ID]
		if info := s.info(row, exists, machines); info != nil {
			infos[recording.ID] = info
		}
	}
	return infos, nil
}

// info is a transcribable recording's transcript: its row, and while that's
// pending, whether a machine is on it. machines are the room owner's. It is
// nil when there is no row and the owner has no machine to make one.
func (s *Service) info(row store.Transcript, exists bool, machines []moil.Machine) *api.TranscriptInfo {
	switch {
	case !exists && len(machines) == 0:
		return nil
	case !exists:
		return &api.TranscriptInfo{Status: api.TranscriptAvailable}
	case row.Status == "completed":
		return &api.TranscriptInfo{Status: api.TranscriptCompleted, Speakers: row.Speakers}
	case row.Status == "failed":
		return &api.TranscriptInfo{Status: api.TranscriptFailed, Error: row.Error}
	}
	if running := s.running(row.RecordingID); running != nil {
		return running
	}
	return &api.TranscriptInfo{Status: api.TranscriptWaiting, Message: waitingMessage(machines, s.cfg.Bundle.Hash())}
}

// running is a pending transcript's status while a machine is on its job,
// or nil while none is.
func (s *Service) running(recordingID string) *api.TranscriptInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[recordingID]
	if j == nil || j.run.State() == moil.Queued {
		return nil
	}
	info := &api.TranscriptInfo{Status: api.TranscriptRunning, Message: j.message}
	if j.progress != nil {
		progress := *j.progress
		info.Progress = &progress
	}
	return info
}

// waitingMessage says why no machine is on a pending transcript, from the
// room owner's machines: which approved the bundle, and what those are
// doing.
func waitingMessage(machines []moil.Machine, hash string) string {
	approved, online, paused, idle := 0, 0, 0, false
	for _, machine := range machines {
		if !machine.HasApproved(hash) {
			continue
		}
		approved++
		if !machine.Online() {
			continue
		}
		online++
		switch machine.State {
		case moil.Idle:
			idle = true
		case moil.Paused:
			paused++
		}
	}
	switch {
	case len(machines) == 0:
		return "No machine is paired to transcribe it."
	case approved == 0:
		return "No paired machine has approved the transcriber yet. Approve it in the moil app."
	case online == 0:
		return "Waiting for a paired machine to come online."
	case idle:
		return "Waiting for a machine to start it."
	case paused == online:
		return "The paired machines are paused. Resume one in the moil app."
	default:
		return "Waiting for a paired machine to finish its current job."
	}
}

// phaseMessage describes what the machine's runtime is doing.
func phaseMessage(phase string) string {
	switch phase {
	case "preparing":
		return "Setting up the transcriber"
	case "downloading":
		return "Downloading the recording"
	case "running":
		return "Transcribing"
	case "uploading":
		return "Uploading the transcript"
	}
	// A phase from a newer machine, shown as it is.
	return capitalize(truncate(phase, maxMessage))
}

// failure says why a job failed, for its row's error.
func failure(err error) string {
	var attempt *moil.JobError
	switch {
	case errors.As(err, &attempt):
		return attemptFailure(attempt)
	case errors.Is(err, moil.ErrTooMuchData):
		return "The machine sent more than klisi keeps for a transcript. Try again."
	case errors.Is(err, moil.ErrBundleRemoved):
		return "klisi's transcriber changed before a machine could run it. Try again."
	default:
		return "klisi couldn't hand the recording to a machine. Try again."
	}
}

// attemptFailure says why a job's last attempt failed.
func attemptFailure(err *moil.JobError) string {
	switch err.Code {
	case moil.CodeScriptError:
		// The bundle's errors are sentences for the service: "can't read
		// the recording: …", "ran out of memory: …".
		return sentence("Transcription failed: " + truncate(err.Message, maxMessage))
	case moil.CodeTimeout:
		return "Transcription took longer than its time limit. Try again."
	case moil.CodeEnvironment:
		return "The machine couldn't set up the transcriber. Check its disk space and network, then try again."
	case moil.CodeAssetDownload, moil.CodeAssetMismatch:
		return "The machine couldn't download the speech models. Check its network, then try again."
	case moil.CodeInputDownload:
		return "The machine couldn't download the recording. Try again."
	case moil.CodeOutputUpload:
		return "The machine couldn't upload the transcript. Try again."
	case moil.CodeInterrupted:
		return "Transcription was stopped on the machine. Try again."
	case moil.CodeLeaseExpired, moil.CodeLost:
		return "The machine stopped responding during transcription. Try again."
	case moil.CodeBusy, moil.CodeNotApproved:
		return "The paired machines kept turning the transcript down. Check that one approved the transcriber, then try again."
	default:
		return "The transcriber stopped unexpectedly. Try again."
	}
}

// sentence ends s with a full stop, unless it has one.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?") {
		return s
	}
	return s + "."
}

// capitalize upper-cases the first letter of s: the bundle's progress
// messages are lower-case, for services to fit in their own sentences.
func capitalize(s string) string {
	first, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return s
	}
	return string(unicode.ToUpper(first)) + s[size:]
}

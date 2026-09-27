package transcripts

import (
	"database/sql"
	"errors"
	"net/http"
	"path"
	"time"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/httpx"
	"klisi/internal/store"
)

// downloadExpiry is how long a transcript's download URL lasts
// (ARCHITECTURE.md §15).
const downloadExpiry = 5 * time.Minute

// Request serves POST api.RecordingTranscriptPath: requests a transcript of
// an available recording, or retries a failed one, and returns its
// api.TranscriptInfo. A transcript already on its way answers with its
// status as it is.
func (s *Service) Request(w http.ResponseWriter, r *http.Request) {
	recording, room, session, ok := s.requireManager(w, r)
	if !ok {
		return
	}
	switch {
	case recording.Status == "starting" || recording.Status == "recording" || recording.Status == "finalizing":
		httpx.WriteError(w, http.StatusConflict, "This recording hasn't finished yet. Request its transcript once it has.")
		return
	case !transcribable(recording):
		httpx.WriteError(w, http.StatusConflict, "This recording has no file, so it can't be transcribed.")
		return
	}
	row, exists, ok := s.loadRow(w, r, recording.ID)
	if !ok {
		return
	}
	if exists && row.Status == "completed" {
		httpx.WriteError(w, http.StatusConflict, "This recording already has a transcript.")
		return
	}
	// The job can only run on the room owner's machines, whoever asks.
	machines, err := s.cfg.Moil.Machines(r.Context(), room.OwnerSub)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not load the room owner's machines. Try again.")
		return
	}
	if exists && row.Status == "pending" {
		httpx.WriteJSON(w, http.StatusAccepted, s.info(recording, row, true, machines))
		return
	}
	if len(machines) == 0 {
		message := "The room's owner has no paired machine to transcribe it. Ask them to pair one on the Machines page."
		if session.Sub == room.OwnerSub {
			message = "Pair a machine on the Machines page to transcribe recordings."
		}
		httpx.WriteError(w, http.StatusConflict, message)
		return
	}
	now := s.cfg.Now().Unix()
	var changed bool
	if exists {
		changed, err = s.cfg.Store.RetryTranscript(r.Context(), recording.ID, now)
	} else {
		changed, err = s.cfg.Store.RequestTranscript(r.Context(), recording.ID, now)
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not request the transcript. Try again.")
		return
	}
	if !changed {
		// Requested meanwhile, by the reconciler or someone else: answer
		// with what it is now.
		if row, exists, ok = s.loadRow(w, r, recording.ID); !ok {
			return
		}
		if !exists || row.Status != "pending" {
			httpx.WriteError(w, http.StatusConflict, "The transcript just changed. Reload to see it.")
			return
		}
		httpx.WriteJSON(w, http.StatusAccepted, s.info(recording, row, true, machines))
		return
	}
	s.Nudge()
	httpx.WriteJSON(w, http.StatusAccepted, api.TranscriptInfo{
		Status: api.TranscriptWaiting, Message: waitingMessage(machines, s.cfg.Bundle.Hash()),
	})
}

// loadRow loads a recording's transcripts row, if it has one. If it can't,
// it answers the request and returns false.
func (s *Service) loadRow(w http.ResponseWriter, r *http.Request, recordingID string) (store.Transcript, bool, bool) {
	row, err := s.cfg.Store.Transcript(r.Context(), recordingID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return store.Transcript{}, false, true
	case err != nil:
		httpx.WriteError(w, http.StatusInternalServerError, "Could not load the transcript. Try again.")
		return store.Transcript{}, false, false
	}
	return row, true, true
}

// Download serves GET api.RecordingTranscriptDownloadPath?format=txt|vtt: a
// redirect to a short-lived presigned URL of a completed transcript.
func (s *Service) Download(w http.ResponseWriter, r *http.Request) {
	recording, _, _, ok := s.requireManager(w, r)
	if !ok {
		return
	}
	format, ok := store.TranscriptFormatByExtension(r.URL.Query().Get("format"))
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "Choose a transcript format: txt or vtt.")
		return
	}
	row, err := s.cfg.Store.Transcript(r.Context(), recording.ID)
	switch {
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		httpx.WriteError(w, http.StatusInternalServerError, "Could not load the transcript. Try again.")
		return
	case err != nil || row.Status != "completed" || !transcribable(recording):
		httpx.WriteError(w, http.StatusConflict, "The transcript isn't ready to download yet.")
		return
	}
	// The file is named like the recording: "2026-09-27 14-00 - Standup.vtt".
	key := recording.TranscriptKey(format)
	location, err := s.cfg.Objects.PresignedDownload(r.Context(), key, downloadExpiry, path.Base(key), format.ContentType)
	if err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "Could not prepare the download. Try again.")
		return
	}
	http.Redirect(w, r, location, http.StatusFound)
}

// requireManager loads the recording a transcript route names, and checks
// the session may manage its room: its owner, or an administrator.
func (s *Service) requireManager(w http.ResponseWriter, r *http.Request) (store.Recording, store.Room, auth.Session, bool) {
	return httpx.RequireRecordingManager(w, r, s.cfg.Store, r.PathValue("id"), "Only a room administrator can manage transcripts.")
}

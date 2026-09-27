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
// api.TranscriptInfo.
func (s *Service) Request(w http.ResponseWriter, r *http.Request) {
	recording, room, session, ok := s.requireManager(w, r)
	if !ok {
		return
	}
	if !transcribable(recording) {
		httpx.WriteError(w, http.StatusConflict, "Only a completed recording can be transcribed.")
		return
	}
	row, err := s.cfg.Store.Transcript(r.Context(), recording.ID)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not load the transcript. Try again.")
		return
	}
	if exists && row.Status != "failed" {
		writeRequested(w, row.Status)
		return
	}
	// The job can only run on the room owner's machines, whoever asks.
	machines, err := s.cfg.Moil.Machines(r.Context(), room.OwnerSub)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not load the room owner's machines. Try again.")
		return
	}
	if len(machines) == 0 {
		message := "The room's owner has no paired machine to transcribe it."
		if session.Sub == room.OwnerSub {
			message = "Pair a machine at /machines to transcribe recordings."
		}
		httpx.WriteError(w, http.StatusConflict, message)
		return
	}
	now := time.Now().Unix()
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
		// Someone else requested it meanwhile, or deleted the recording.
		httpx.WriteError(w, http.StatusConflict, "The transcript just changed. Reload to see it.")
		return
	}
	s.Nudge()
	httpx.WriteJSON(w, http.StatusAccepted, api.TranscriptInfo{
		Status: api.TranscriptWaiting, Message: waitingMessage(machines, s.cfg.Bundle.Hash()),
	})
}

// writeRequested answers a request for a transcript that is already
// pending or completed.
func writeRequested(w http.ResponseWriter, status string) {
	message := "This recording's transcript is already on its way."
	if status == "completed" {
		message = "This recording already has a transcript."
	}
	httpx.WriteError(w, http.StatusConflict, message)
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
		httpx.WriteError(w, http.StatusConflict, "The transcript is not ready to download.")
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

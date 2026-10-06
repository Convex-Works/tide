package recording

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode"

	protocol "github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/proto"

	"tide/internal/api"
	"tide/internal/auth"
	"tide/internal/httpx"
	"tide/internal/store"
)

const activeRecordingMessage = "This room already has an active recording."

type recordingStore interface {
	RoomBySlug(context.Context, string) (store.Room, error)
	RoomByID(context.Context, string) (store.Room, error)
	ActiveRecordingByRoomID(context.Context, string) (store.Recording, error)
	InsertRecording(context.Context, store.Recording) error
	UpdateRecordingByEgress(context.Context, string, store.RecordingUpdate) error
	RecordingsByRoomSlug(context.Context, string) ([]store.Recording, error)
	RecordingByID(context.Context, string) (store.Recording, error)
	RecordingByEgressID(context.Context, string) (store.Recording, error)
	DeleteRecording(ctx context.Context, id string, now int64) ([]string, error)
	ListActiveRecordings(context.Context) ([]store.Recording, error)
	RemovalQueue
}

type EgressClient interface {
	StartRoomCompositeEgress(context.Context, *protocol.RoomCompositeEgressRequest) (*protocol.EgressInfo, error)
	StopEgress(context.Context, *protocol.StopEgressRequest) (*protocol.EgressInfo, error)
	ListEgress(context.Context, *protocol.ListEgressRequest) (*protocol.ListEgressResponse, error)
}

type RoomServiceClient interface {
	UpdateRoomMetadata(context.Context, *protocol.UpdateRoomMetadataRequest) (*protocol.Room, error)
}

type ObjectStore interface {
	Remove(context.Context, string) error
	PresignedGet(context.Context, string, time.Duration) (string, error)
}

// TranscriptSource adds transcript state to listed recordings
// (ARCHITECTURE.md §8.1). The map is keyed by recording ID; a recording
// missing from it has no transcript and can't get one.
type TranscriptSource interface {
	Transcripts(ctx context.Context, room store.Room, recordings []store.Recording) (map[string]*api.TranscriptInfo, error)
}

type Handler struct {
	store       recordingStore
	egress      EgressClient
	rooms       RoomServiceClient
	objects     ObjectStore
	templateURL string
	s3Output    *protocol.S3Upload
	now         func() time.Time
	newID       func() (string, error)
	receiver    WebhookReceiver
	// onParticipantJoined lets the webhook fan participant_joined events out
	// to moderation (kick-ban enforcement) without a package dependency.
	onParticipantJoined func(ctx context.Context, room, identity string)
	// transcripts, if set, fills RecordingInfo.Transcript on the list path.
	transcripts TranscriptSource
	// onRecordingsChanged is called after a recording ends or is deleted, so
	// that the transcripts reconciler can act without waiting for its tick.
	onRecordingsChanged func()
	// removeDueTimeout bounds each reconciler pass's removals.
	removeDueTimeout time.Duration
}

// SetTranscripts makes the list path report each recording's transcript.
func (h *Handler) SetTranscripts(source TranscriptSource) {
	h.transcripts = source
}

// SetRecordingsChangedHook registers a callback invoked after a recording
// ends (by webhook or reconciliation) or is deleted. It must not block.
func (h *Handler) SetRecordingsChangedHook(hook func()) {
	h.onRecordingsChanged = hook
}

func (h *Handler) recordingsChanged() {
	if h.onRecordingsChanged != nil {
		h.onRecordingsChanged()
	}
}

// SetClock replaces the clock the handler stamps recordings and due
// removals with, for tests that move time on.
func (h *Handler) SetClock(now func() time.Time) {
	h.now = now
}

// SetParticipantJoinedHook registers a callback invoked for every verified
// participant_joined webhook event.
func (h *Handler) SetParticipantJoinedHook(hook func(ctx context.Context, room, identity string)) {
	h.onParticipantJoined = hook
}

func NewHandler(
	recordings recordingStore,
	egress EgressClient,
	rooms RoomServiceClient,
	objects ObjectStore,
	templateURL string,
	receiver WebhookReceiver,
) *Handler {
	return &Handler{
		store: recordings, egress: egress, rooms: rooms, objects: objects,
		templateURL: templateURL, now: time.Now, newID: randomID, receiver: receiver,
		removeDueTimeout: removeDueTimeout,
	}
}

func (h *Handler) Start(w http.ResponseWriter, r *http.Request) {
	room, session, ok := h.requireManagerBySlug(w, r, "Only a room administrator can start recording.")
	if !ok {
		return
	}
	// The body is optional: no body means the defaults (audio-only).
	var request api.RecordingStartRequest
	if err := httpx.DecodeJSON(w, r, &request); err != nil && !errors.Is(err, io.EOF) {
		httpx.WriteError(w, http.StatusBadRequest, "Invalid recording options.")
		return
	}
	audioOnly := !request.Video
	if _, err := h.store.ActiveRecordingByRoomID(r.Context(), room.ID); err == nil {
		httpx.WriteError(w, http.StatusConflict, activeRecordingMessage)
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not check the recording state. Try again.")
		return
	}

	started := h.now().UTC()
	startedAt := started.Unix()
	id, err := h.newID()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not create the recording. Try again.")
		return
	}
	fileType := protocol.EncodedFileType_MP4
	extension := ".mp4"
	mode := "video"
	if audioOnly {
		fileType = protocol.EncodedFileType_OGG
		extension = ".ogg"
		mode = "audio"
	}
	filename := recordingFilename(started, room.Name, extension)
	// Keep the requested human-readable basename while using the recording ID
	// as a directory so two recordings started in the same minute cannot
	// overwrite each other.
	key := path.Join("recordings", room.Slug, id, filename)
	metadata := map[string]string{
		"tide-filename":       filename,
		"tide-meeting-id":     room.ID,
		"tide-meeting-name":   room.Name,
		"tide-meeting-slug":   room.Slug,
		"tide-recording-id":   id,
		"tide-recording-mode": mode,
		"tide-started-at":     started.Format(time.RFC3339),
		"tide-started-by":     session.Sub,
	}
	info, err := h.egress.StartRoomCompositeEgress(r.Context(), &protocol.RoomCompositeEgressRequest{
		RoomName:      room.Slug,
		Layout:        "grid",
		CustomBaseUrl: h.templateURL,
		AudioOnly:     audioOnly,
		FileOutputs: []*protocol.EncodedFileOutput{{
			FileType: fileType,
			Filepath: key,
			Output: s3EncodedOutput(
				h.s3Output, metadata,
				mime.FormatMediaType("attachment", map[string]string{"filename": filename}),
			),
		}},
	})
	if err != nil || info == nil || info.EgressId == "" {
		httpx.WriteError(w, http.StatusBadGateway, "LiveKit could not start recording. Try again.")
		return
	}
	recording := store.Recording{
		ID: id, RoomID: room.ID, RoomSlug: room.Slug, EgressID: info.EgressId,
		Status: "starting", StartedBy: session.Sub, StartedAt: startedAt,
		AudioOnly: audioOnly,
	}
	if err := h.store.InsertRecording(r.Context(), recording); err != nil {
		_, _ = h.egress.StopEgress(r.Context(), &protocol.StopEgressRequest{EgressId: info.EgressId})
		if store.IsActiveRecordingConflict(err) {
			httpx.WriteError(w, http.StatusConflict, activeRecordingMessage)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Could not save the recording. Try again.")
		return
	}
	if err := h.setRecordingMetadata(r.Context(), room.Slug, true); err != nil {
		_, _ = h.egress.StopEgress(r.Context(), &protocol.StopEgressRequest{EgressId: info.EgressId})
		_ = h.store.UpdateRecordingByEgress(r.Context(), info.EgressId, store.RecordingUpdate{Status: "failed"})
		httpx.WriteError(w, http.StatusBadGateway, "The recording started, but its room state could not be updated. Try again.")
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, recordingInfo(recording))
}

func (h *Handler) Stop(w http.ResponseWriter, r *http.Request) {
	room, _, ok := h.requireManagerBySlug(w, r, "Only a room administrator can stop recording.")
	if !ok {
		return
	}
	recording, err := h.store.ActiveRecordingByRoomID(r.Context(), room.ID)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, http.StatusConflict, "This room does not have an active recording.")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not load the recording. Try again.")
		return
	}
	// Persist the stopping state BEFORE the RPC: the monotonic status guard
	// in the store means a racing egress_ended can complete the row during
	// StopEgress without this handler regressing it afterwards. A row already
	// in "finalizing" falls through and re-issues the stop — that retries a
	// stop whose RPC previously failed.
	if err := h.store.UpdateRecordingByEgress(r.Context(), recording.EgressID, store.RecordingUpdate{Status: "finalizing"}); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not save the recording state. Try again.")
		return
	}
	if _, err := h.egress.StopEgress(r.Context(), &protocol.StopEgressRequest{EgressId: recording.EgressID}); err != nil {
		// The egress may have ended on its own while the stop was in flight;
		// a terminal row means the stop already succeeded.
		if current, lookupErr := h.store.RecordingByEgressID(r.Context(), recording.EgressID); lookupErr == nil &&
			(current.Status == "completed" || current.Status == "failed") {
			_ = h.setRecordingMetadata(r.Context(), room.Slug, false)
			httpx.WriteJSON(w, http.StatusOK, recordingInfo(current))
			return
		}
		httpx.WriteError(w, http.StatusBadGateway, "LiveKit could not stop recording. Try again.")
		return
	}
	recording.Status = "finalizing"
	if err := h.setRecordingMetadata(r.Context(), room.Slug, false); err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "Recording stopped, but its room state could not be updated.")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, recordingInfo(recording))
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	room, _, ok := h.requireManagerBySlug(w, r, "Only a room administrator can view recordings.")
	if !ok {
		return
	}
	recordings, err := h.store.RecordingsByRoomSlug(r.Context(), room.Slug)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not load recordings. Try again.")
		return
	}
	// Transcripts are best effort: without them the recordings are still
	// there to play, download and delete.
	var transcripts map[string]*api.TranscriptInfo
	if h.transcripts != nil {
		transcripts, err = h.transcripts.Transcripts(r.Context(), room, recordings)
		if err != nil {
			log.Printf("recordings: room %s: list without transcripts: %v", room.ID, err)
			transcripts = nil
		}
	}
	response := make([]api.RecordingInfo, 0, len(recordings))
	for _, recording := range recordings {
		info := recordingInfo(recording)
		info.Transcript = transcripts[recording.ID]
		response = append(response, info)
	}
	httpx.WriteJSON(w, http.StatusOK, response)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	recording, ok := h.requireManagerByRecording(w, r, "Only a room administrator can delete recordings.")
	if !ok {
		return
	}
	if recording.Status == "starting" || recording.Status == "recording" || recording.Status == "finalizing" {
		httpx.WriteError(w, http.StatusConflict, "Stop the recording before deleting it.")
		return
	}
	// The row goes, and its files and transcript sidecars are queued for
	// removal, at once (ARCHITECTURE.md §8.1): from then on the files are
	// removed even if storage is down now.
	now := h.now().Unix()
	keys, err := h.store.DeleteRecording(r.Context(), recording.ID, now)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "Recording not found.")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not delete the recording. Try again.")
		return
	}
	h.recordingsChanged()
	RemoveDeleted(r.Context(), h.objects, h.store, keys, now)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	recording, ok := h.requireManagerByRecording(w, r, "Only a room administrator can download recordings.")
	if !ok {
		return
	}
	if recording.Status != "completed" || !recording.HasFile() {
		httpx.WriteError(w, http.StatusConflict, "The recording is not ready to download.")
		return
	}
	location, err := h.objects.PresignedGet(r.Context(), *recording.S3Key, 5*time.Minute)
	if err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "Could not prepare the download. Try again.")
		return
	}
	http.Redirect(w, r, location, http.StatusFound)
}

func (h *Handler) requireManagerBySlug(w http.ResponseWriter, r *http.Request, forbidden string) (store.Room, auth.Session, bool) {
	return httpx.RequireRoomManager(w, r, h.store, r.PathValue("slug"), forbidden)
}

func (h *Handler) requireManagerByRecording(w http.ResponseWriter, r *http.Request, forbidden string) (store.Recording, bool) {
	recording, _, _, ok := httpx.RequireRecordingManager(w, r, h.store, r.PathValue("id"), forbidden)
	return recording, ok
}

func (h *Handler) setRecordingMetadata(ctx context.Context, roomSlug string, active bool) error {
	metadata := `{"recording":false}`
	if active {
		metadata = `{"recording":true}`
	}
	_, err := h.rooms.UpdateRoomMetadata(ctx, &protocol.UpdateRoomMetadataRequest{
		Room: roomSlug, Metadata: metadata,
	})
	return err
}

func recordingInfo(recording store.Recording) api.RecordingInfo {
	return api.RecordingInfo{
		ID: recording.ID, RoomSlug: recording.RoomSlug, EgressID: recording.EgressID,
		Status: recording.Status, StartedBy: recording.StartedBy, StartedAt: recording.StartedAt,
		AudioOnly: recording.AudioOnly, EndedAt: recording.EndedAt, DurationS: recording.DurationS,
		S3Key: recording.S3Key, SizeBytes: recording.SizeBytes,
	}
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func recordingFilename(started time.Time, meetingName, extension string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case strings.ContainsRune(`/\\<>:"|?*`, r):
			return '-'
		case unicode.IsControl(r):
			return ' '
		default:
			return r
		}
	}, meetingName)
	name = strings.Trim(strings.Join(strings.Fields(name), " "), ". ")
	if name == "" {
		name = "meeting"
	}
	return fmt.Sprintf("%s - %s%s", started.UTC().Format("2006-01-02 15-04"), name, extension)
}

// s3EncodedOutput wraps the configured S3 destination for an egress request;
// nil (tests) leaves the output empty so egress falls back to local staging.
// Clone the configured protobuf before adding per-recording values: the
// handler is shared by concurrent requests and must never mutate its template.
func s3EncodedOutput(
	s3 *protocol.S3Upload,
	metadata map[string]string,
	contentDisposition string,
) *protocol.EncodedFileOutput_S3 {
	if s3 == nil {
		return nil
	}
	destination := proto.Clone(s3).(*protocol.S3Upload)
	if destination.Metadata == nil {
		destination.Metadata = make(map[string]string, len(metadata))
	}
	for key, value := range metadata {
		destination.Metadata[key] = value
	}
	destination.ContentDisposition = contentDisposition
	return &protocol.EncodedFileOutput_S3{S3: destination}
}

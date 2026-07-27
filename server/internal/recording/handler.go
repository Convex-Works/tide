package recording

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	protocol "github.com/livekit/protocol/livekit"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/httpx"
	"klisi/internal/store"
)

const activeRecordingMessage = "This room already has an active recording."

type recordingStore interface {
	RoomBySlug(context.Context, string) (store.Room, error)
	ActiveRecordingByRoomID(context.Context, string) (store.Recording, error)
	InsertRecording(context.Context, store.Recording) error
	UpdateRecordingByEgress(context.Context, string, store.RecordingUpdate) error
	RecordingsByRoomSlug(context.Context, string) ([]store.Recording, error)
	RecordingByID(context.Context, string) (store.Recording, error)
	RecordingByEgressID(context.Context, string) (store.Recording, error)
	DeleteRecording(context.Context, string) error
	ListActiveRecordings(context.Context) ([]store.Recording, error)
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

	startedAt := h.now().Unix()
	// Audio-only stays MP4 (AAC): it plays everywhere, including iOS Safari,
	// and egress normalizes filepath extensions to the file type anyway.
	key := fmt.Sprintf("recordings/%s/%d.mp4", room.Slug, startedAt)
	info, err := h.egress.StartRoomCompositeEgress(r.Context(), &protocol.RoomCompositeEgressRequest{
		RoomName:      room.Slug,
		Layout:        "grid",
		CustomBaseUrl: h.templateURL,
		AudioOnly:     audioOnly,
		FileOutputs: []*protocol.EncodedFileOutput{{
			FileType: protocol.EncodedFileType_MP4,
			Filepath: key,
			Output:   s3EncodedOutput(h.s3Output),
		}},
	})
	if err != nil || info == nil || info.EgressId == "" {
		httpx.WriteError(w, http.StatusBadGateway, "LiveKit could not start recording. Try again.")
		return
	}
	id, err := h.newID()
	if err != nil {
		_, _ = h.egress.StopEgress(r.Context(), &protocol.StopEgressRequest{EgressId: info.EgressId})
		httpx.WriteError(w, http.StatusInternalServerError, "Could not create the recording. Try again.")
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
	response := make([]api.RecordingInfo, 0, len(recordings))
	for _, recording := range recordings {
		response = append(response, recordingInfo(recording))
	}
	httpx.WriteJSON(w, http.StatusOK, response)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	recording, ok := h.requireManagerByRecording(w, r)
	if !ok {
		return
	}
	if recording.Status == "starting" || recording.Status == "recording" || recording.Status == "finalizing" {
		httpx.WriteError(w, http.StatusConflict, "Stop the recording before deleting it.")
		return
	}
	if recording.S3Key != nil && *recording.S3Key != "" {
		if err := h.objects.Remove(r.Context(), *recording.S3Key); err != nil {
			httpx.WriteError(w, http.StatusBadGateway, "Could not delete the recording file. Try again.")
			return
		}
	}
	if err := h.store.DeleteRecording(r.Context(), recording.ID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not delete the recording. Try again.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	recording, ok := h.requireManagerByRecording(w, r)
	if !ok {
		return
	}
	if recording.Status != "completed" || recording.S3Key == nil || *recording.S3Key == "" {
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

func (h *Handler) requireManagerByRecording(w http.ResponseWriter, r *http.Request) (store.Recording, bool) {
	_, ok := auth.SessionFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Authentication required.")
		return store.Recording{}, false
	}
	recording, err := h.store.RecordingByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "Recording not found.")
		return store.Recording{}, false
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not load the recording. Try again.")
		return store.Recording{}, false
	}
	if _, _, ok := httpx.RequireRoomManager(
		w, r, h.store, recording.RoomSlug, "Only a room administrator can manage recordings.",
	); !ok {
		return store.Recording{}, false
	}
	return recording, true
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

// s3EncodedOutput wraps the configured S3 destination for an egress request;
// nil (tests) leaves the output empty so egress falls back to local staging.
func s3EncodedOutput(s3 *protocol.S3Upload) *protocol.EncodedFileOutput_S3 {
	if s3 == nil {
		return nil
	}
	return &protocol.EncodedFileOutput_S3{S3: s3}
}

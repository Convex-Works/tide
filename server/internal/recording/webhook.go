package recording

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	protocol "github.com/livekit/protocol/livekit"

	"tide/internal/httpx"
	"tide/internal/store"
)

// maxWebhookBody bounds the request body read by the LiveKit receiver, which
// reads the whole body BEFORE verifying the signature. Real events are small
// JSON; anything larger is hostile.
const maxWebhookBody = 1 << 20

// speakersPutTimeout bounds storing a speaker timeline: storage being slow
// mustn't hold back the end of a recording for long.
const speakersPutTimeout = 10 * time.Second

func (h *Handler) Webhook(w http.ResponseWriter, r *http.Request) {
	if h.receiver == nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Webhook receiver is not configured.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBody)
	event, err := h.receiver.Receive(r)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "Webhook body is too large.")
			return
		}
		httpx.WriteError(w, http.StatusUnauthorized, "Webhook signature is invalid.")
		return
	}
	if err := h.HandleWebhookEvent(r, event); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not update recording state.")
		return
	}
	w.WriteHeader(http.StatusOK)
}

// HandleWebhookEvent is separated from signature verification so unit tests
// can exercise synthetic LiveKit events. Production only reaches it through
// Webhook, whose receiver verifies the signed request first.
func (h *Handler) HandleWebhookEvent(r *http.Request, event *protocol.WebhookEvent) error {
	if event == nil {
		return nil
	}
	if event.Event == "participant_joined" {
		if h.onParticipantJoined != nil && event.Room != nil && event.Participant != nil {
			h.onParticipantJoined(r.Context(), event.Room.Name, event.Participant.Identity)
		}
		return nil
	}
	if event.EgressInfo == nil {
		return nil
	}
	info := event.EgressInfo
	switch event.Event {
	case "egress_started":
		// egress_started means the job was accepted, not that capture began —
		// the info status is still EGRESS_STARTING until the template signals.
		return h.updateWebhookStatus(r.Context(), info, statusFromEgress(info.Status))
	case "egress_updated":
		return h.updateWebhookStatus(r.Context(), info, statusFromEgress(info.Status))
	case "egress_ended":
		err := h.finishRecording(r.Context(), info)
		// An egress with no row of its own (a Start that lost the race for
		// the room's one active recording) never turned the indicator on.
		if recording, lookupErr := h.store.RecordingByEgressID(r.Context(), info.EgressId); lookupErr == nil {
			h.recordingEnded(r.Context(), recording)
		}
		return err
	default:
		return nil
	}
}

func (h *Handler) updateWebhookStatus(ctx context.Context, info *protocol.EgressInfo, status string) error {
	if status == "" {
		return nil
	}
	return h.store.UpdateRecordingByEgress(ctx, info.EgressId, store.RecordingUpdate{Status: status})
}

func (h *Handler) finishRecording(ctx context.Context, info *protocol.EgressInfo) error {
	status := "completed"
	if info.Status != protocol.EgressStatus_EGRESS_COMPLETE || info.Error != "" {
		status = "failed"
	}
	result := recordingFileResult(info)
	endedAt := epochSeconds(info.EndedAt)
	if result != nil && result.EndedAt != 0 {
		endedAt = epochSeconds(result.EndedAt)
	}
	update := store.RecordingUpdate{Status: status}
	if endedAt > 0 {
		update.EndedAt = &endedAt
	}
	if result != nil {
		duration := result.Duration / int64(time.Second)
		if duration < 0 {
			duration = 0
		}
		update.DurationS = &duration
		size := result.Size
		update.SizeBytes = &size
		if key := objectKey(result); key != "" {
			update.S3Key = &key
		}
	}
	h.finishing.Lock()
	defer h.finishing.Unlock()
	h.saveSpeakers(ctx, info, status, result)
	if err := h.store.UpdateRecordingByEgress(ctx, info.EgressId, update); err != nil {
		return err
	}
	h.recordingsChanged()
	return nil
}

// saveSpeakers stops the observer of the recording info ended and, if it
// completed, stores what the observer saw beside its file. This comes before
// the row says completed, because that's when a transcript job can start,
// and the job takes the file only if it's there by then (ARCHITECTURE.md §8.1).
// It's best effort: a recording without the file is a transcript without
// names, so nothing here stops the recording from completing.
func (h *Handler) saveSpeakers(ctx context.Context, info *protocol.EgressInfo, status string, file *protocol.FileInfo) {
	if h.speakers == nil {
		return
	}
	recording, err := h.store.RecordingByEgressID(ctx, info.EgressId)
	if errors.Is(err, sql.ErrNoRows) {
		return // a Start that lost the race, which stopped its own observer
	}
	if err != nil {
		log.Printf("recording: egress %s: stop its speaker observer: %v", info.EgressId, err)
		return
	}
	activity := h.speakers.Stop(recording.ID)
	if activity == nil || status != "completed" || file == nil {
		return
	}
	key := objectKey(file)
	length := time.Duration(file.Duration)
	if length <= 0 {
		length = time.Duration(file.EndedAt - file.StartedAt)
	}
	if key == "" || file.StartedAt <= 0 || length <= 0 {
		log.Printf("recording %s: no speaker timeline: egress didn't say when its file starts and ends", recording.ID)
		return
	}
	timeline := activity.Timeline(time.Unix(0, file.StartedAt), length)
	if activity.Dropped > 0 {
		log.Printf("recording %s: the speaker timeline lost %d spans to its limit", recording.ID, activity.Dropped)
	}
	if len(timeline.Participants) == 0 {
		return
	}
	body, err := timeline.JSON()
	if err == nil {
		putCtx, cancel := context.WithTimeout(ctx, speakersPutTimeout)
		defer cancel()
		err = h.objects.Put(putCtx, store.Recording{S3Key: &key}.SpeakersKey(), body, "application/json")
	}
	if err != nil {
		log.Printf("recording %s: its transcript won't name speakers: store the timeline: %v", recording.ID, err)
	}
}

func recordingFileResult(info *protocol.EgressInfo) *protocol.FileInfo {
	if len(info.FileResults) > 0 {
		return info.FileResults[0]
	}
	return info.GetFile()
}

func objectKey(file *protocol.FileInfo) string {
	if file.Filename != "" {
		return strings.TrimPrefix(file.Filename, "/")
	}
	if file.Location == "" {
		return ""
	}
	parsed, err := url.Parse(file.Location)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(parsed.Path, "/")
}

// epochSeconds converts a protocol timestamp (always UnixNano) to seconds.
func epochSeconds(value int64) int64 {
	if value <= 0 {
		return 0
	}
	return value / int64(time.Second)
}

func statusFromEgress(status protocol.EgressStatus) string {
	switch status {
	case protocol.EgressStatus_EGRESS_STARTING:
		return "starting"
	case protocol.EgressStatus_EGRESS_ACTIVE:
		return "recording"
	case protocol.EgressStatus_EGRESS_ENDING:
		return "finalizing"
	case protocol.EgressStatus_EGRESS_COMPLETE:
		return "completed"
	case protocol.EgressStatus_EGRESS_FAILED,
		protocol.EgressStatus_EGRESS_ABORTED,
		protocol.EgressStatus_EGRESS_LIMIT_REACHED:
		return "failed"
	default:
		return ""
	}
}

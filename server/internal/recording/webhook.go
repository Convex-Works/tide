package recording

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	protocol "github.com/livekit/protocol/livekit"

	"klisi/internal/store"
)

func (h *Handler) Webhook(w http.ResponseWriter, r *http.Request) {
	if h.receiver == nil {
		writeError(w, http.StatusInternalServerError, "Webhook receiver is not configured.")
		return
	}
	event, err := h.receiver.Receive(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Webhook signature is invalid.")
		return
	}
	if err := h.HandleWebhookEvent(r, event); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update recording state.")
		return
	}
	w.WriteHeader(http.StatusOK)
}

// HandleWebhookEvent is separated from signature verification so unit tests
// can exercise synthetic LiveKit events. Production only reaches it through
// Webhook, whose receiver verifies the signed request first.
func (h *Handler) HandleWebhookEvent(r *http.Request, event *protocol.WebhookEvent) error {
	if event == nil || event.EgressInfo == nil {
		return nil
	}
	info := event.EgressInfo
	switch event.Event {
	case "egress_started":
		// egress_started means the job was accepted, not that capture began —
		// the info status is still EGRESS_STARTING until the template signals.
		return h.updateWebhookStatus(r, info, statusFromEgress(info.Status))
	case "egress_updated":
		return h.updateWebhookStatus(r, info, statusFromEgress(info.Status))
	case "egress_ended":
		err := h.finishRecording(r, info)
		roomSlug := info.RoomName
		if recording, lookupErr := h.store.RecordingByEgressID(r.Context(), info.EgressId); lookupErr == nil {
			roomSlug = recording.RoomSlug
		}
		if roomSlug != "" {
			// A room may already have closed by the time Egress ends.
			_ = h.setRecordingMetadata(r.Context(), roomSlug, false)
		}
		return err
	default:
		return nil
	}
}

func (h *Handler) updateWebhookStatus(r *http.Request, info *protocol.EgressInfo, status string) error {
	if status == "" {
		return nil
	}
	err := h.store.UpdateRecordingByEgress(r.Context(), info.EgressId, store.RecordingUpdate{Status: status})
	return err
}

func (h *Handler) finishRecording(r *http.Request, info *protocol.EgressInfo) error {
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
	err := h.store.UpdateRecordingByEgress(r.Context(), info.EgressId, update)
	return err
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

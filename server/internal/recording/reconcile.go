package recording

import (
	"context"
	"log"
	"time"

	protocol "github.com/livekit/protocol/livekit"

	"klisi/internal/store"
)

// reconcileGrace spares young or freshly-stopped rows so the normal webhook
// flow gets to win before the reconciler declares an egress lost.
const reconcileGrace = 2 * time.Minute

// RunReconciler repairs recording rows whose webhooks were lost. LiveKit does
// not guarantee webhook delivery, so a missed egress_ended would otherwise
// leave a row active forever — blocking every future recording for that room
// (partial unique index) and its deletion. Active rows are compared against
// LiveKit's actual egress state: finished egresses finalize the row,
// still-running egresses stuck in "finalizing" get their stop re-issued, and
// egresses LiveKit no longer knows are failed after a grace period. Runs once
// immediately, then every interval; blocks until ctx is done.
func (h *Handler) RunReconciler(ctx context.Context, interval time.Duration) {
	h.reconcile(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.reconcile(ctx)
		}
	}
}

func (h *Handler) reconcile(ctx context.Context) {
	active, err := h.store.ListActiveRecordings(ctx)
	if err != nil {
		log.Printf("recording reconciler: list active recordings: %v", err)
		return
	}
	for _, recording := range active {
		if err := h.reconcileRecording(ctx, recording); err != nil {
			log.Printf("recording reconciler: %s (egress %s): %v", recording.ID, recording.EgressID, err)
		}
	}
}

func (h *Handler) reconcileRecording(ctx context.Context, recording store.Recording) error {
	response, err := h.egress.ListEgress(ctx, &protocol.ListEgressRequest{EgressId: recording.EgressID})
	if err != nil {
		return err // LiveKit unreachable — retry next tick.
	}
	var info *protocol.EgressInfo
	for _, item := range response.GetItems() {
		if item.EgressId == recording.EgressID {
			info = item
			break
		}
	}
	if info == nil {
		// LiveKit no longer knows this egress (crash, restart, state loss).
		// Grace period covers a start RPC that has not registered yet.
		if h.now().Sub(time.Unix(recording.StartedAt, 0)) < reconcileGrace {
			return nil
		}
		if err := h.store.UpdateRecordingByEgress(ctx, recording.EgressID, store.RecordingUpdate{Status: "failed"}); err != nil {
			return err
		}
		_ = h.setRecordingMetadata(ctx, recording.RoomSlug, false)
		return nil
	}
	switch info.Status {
	case protocol.EgressStatus_EGRESS_COMPLETE,
		protocol.EgressStatus_EGRESS_FAILED,
		protocol.EgressStatus_EGRESS_ABORTED,
		protocol.EgressStatus_EGRESS_LIMIT_REACHED:
		// The egress finished but the row never heard: replay the ending.
		if err := h.finishRecording(ctx, info); err != nil {
			return err
		}
		_ = h.setRecordingMetadata(ctx, recording.RoomSlug, false)
		return nil
	case protocol.EgressStatus_EGRESS_STARTING, protocol.EgressStatus_EGRESS_ACTIVE:
		// A row in "finalizing" means a stop was requested; if the egress is
		// still running the stop RPC was lost — re-issue it.
		if recording.Status == "finalizing" {
			_, err := h.egress.StopEgress(ctx, &protocol.StopEgressRequest{EgressId: recording.EgressID})
			return err
		}
		// Repair a missed egress_updated; the monotonic store guard makes a
		// stale-downgrade attempt a no-op.
		return h.updateWebhookStatus(ctx, info, statusFromEgress(info.Status))
	default:
		return nil
	}
}

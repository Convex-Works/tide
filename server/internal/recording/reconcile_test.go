package recording

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	protocol "github.com/livekit/protocol/livekit"

	"tide/internal/auth"
	"tide/internal/store"
)

func insertRecording(t *testing.T, handler *Handler, room store.Room, id, egressID, status string, startedAt int64) store.Recording {
	t.Helper()
	recording := store.Recording{
		ID: id, RoomID: room.ID, RoomSlug: room.Slug, EgressID: egressID,
		Status: status, StartedBy: "owner", StartedAt: startedAt,
	}
	if err := handler.store.InsertRecording(context.Background(), recording); err != nil {
		t.Fatal(err)
	}
	return recording
}

func recordingStatus(t *testing.T, handler *Handler, id string) string {
	t.Helper()
	got, err := handler.store.RecordingByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got.Status
}

// Stop must persist "finalizing" before the StopEgress RPC so a racing
// egress_ended can never be regressed afterwards (review finding #3).
func TestStopPersistsFinalizingBeforeRPC(t *testing.T) {
	handler, egress, _, room := recordingTestHandler(t)
	insertRecording(t, handler, room, "rec-race", "egress-race", "recording", 100)
	egress.stopErr = errors.New("rpc lost")

	response := httptest.NewRecorder()
	handler.Stop(response, stopRequest(room.Slug))

	if response.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 on RPC failure, got %d", response.Code)
	}
	if got := recordingStatus(t, handler, "rec-race"); got != "finalizing" {
		t.Fatalf("status must be persisted before the RPC, got %q", got)
	}

	// Retrying the stop re-issues the RPC instead of returning a conflict.
	egress.stopErr = nil
	response = httptest.NewRecorder()
	handler.Stop(response, stopRequest(room.Slug))
	if response.Code != http.StatusOK {
		t.Fatalf("retry of a finalizing stop should succeed, got %d", response.Code)
	}
	if len(egress.stops) != 2 {
		t.Fatalf("expected 2 StopEgress calls, got %d", len(egress.stops))
	}
}

func TestStopReportsTerminalRowWhenRPCFails(t *testing.T) {
	handler, egress, _, room := recordingTestHandler(t)
	insertRecording(t, handler, room, "rec-done", "egress-done", "recording", 100)
	// Simulate egress_ended landing while the stop RPC is in flight.
	egress.stopErr = errors.New("egress already ended")
	egress.onStop = func() {
		if err := handler.store.UpdateRecordingByEgress(context.Background(), "egress-done",
			store.RecordingUpdate{Status: "completed"}); err != nil {
			t.Fatal(err)
		}
	}

	response := httptest.NewRecorder()
	handler.Stop(response, stopRequest(room.Slug))

	if response.Code != http.StatusOK {
		t.Fatalf("terminal row should turn the failed RPC into success, got %d", response.Code)
	}
	if got := recordingStatus(t, handler, "rec-done"); got != "completed" {
		t.Fatalf("status = %q, want completed", got)
	}
}

func TestReconcileCompletesRowFromEgressState(t *testing.T) {
	handler, egress, rooms, room := recordingTestHandler(t)
	insertRecording(t, handler, room, "rec-1", "egress-1", "recording", 100)
	egress.egresses = []*protocol.EgressInfo{{
		EgressId: "egress-1", RoomName: room.Slug,
		Status:  protocol.EgressStatus_EGRESS_COMPLETE,
		EndedAt: time.Unix(130, 0).UnixNano(),
		FileResults: []*protocol.FileInfo{{
			Filename: "recordings/calm-otter-412/100.mp4",
			Duration: 30 * int64(time.Second), Size: 1_000,
		}},
	}}

	handler.reconcile(context.Background())

	got, err := handler.store.RecordingByID(context.Background(), "rec-1")
	if err != nil || got.Status != "completed" || got.S3Key == nil {
		t.Fatalf("reconciled recording = %#v, %v", got, err)
	}
	if len(rooms.updates) != 1 || rooms.updates[0].Metadata != `{"recording":false}` {
		t.Fatalf("metadata updates = %#v", rooms.updates)
	}
}

func TestReconcileFailsLostEgressAfterGrace(t *testing.T) {
	handler, _, _, room := recordingTestHandler(t)
	now := handler.now()
	// Old row: LiveKit no longer knows the egress → failed.
	insertRecording(t, handler, room, "rec-old", "egress-old", "recording", now.Add(-10*time.Minute).Unix())
	handler.reconcile(context.Background())
	if got := recordingStatus(t, handler, "rec-old"); got != "failed" {
		t.Fatalf("lost egress should fail after grace, got %q", got)
	}
}

func TestReconcileSparesYoungRows(t *testing.T) {
	handler, _, _, room := recordingTestHandler(t)
	now := handler.now()
	insertRecording(t, handler, room, "rec-new", "egress-new", "starting", now.Add(-10*time.Second).Unix())
	handler.reconcile(context.Background())
	if got := recordingStatus(t, handler, "rec-new"); got != "starting" {
		t.Fatalf("young row must be left for the webhook flow, got %q", got)
	}
}

func TestReconcileReissuesLostStop(t *testing.T) {
	handler, egress, _, room := recordingTestHandler(t)
	insertRecording(t, handler, room, "rec-stuck", "egress-stuck", "finalizing", 100)
	egress.egresses = []*protocol.EgressInfo{{
		EgressId: "egress-stuck", RoomName: room.Slug,
		Status: protocol.EgressStatus_EGRESS_ACTIVE,
	}}

	handler.reconcile(context.Background())

	if len(egress.stops) != 1 || egress.stops[0].EgressId != "egress-stuck" {
		t.Fatalf("expected StopEgress re-issue, got %#v", egress.stops)
	}
	if got := recordingStatus(t, handler, "rec-stuck"); got != "finalizing" {
		t.Fatalf("status should stay finalizing until egress ends, got %q", got)
	}
}

func stopRequest(slug string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/rooms/"+slug+"/recordings/stop", nil)
	request.SetPathValue("slug", slug)
	return request.WithContext(auth.WithSession(request.Context(), auth.Session{Sub: "owner"}))
}

package recording

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	protocol "github.com/livekit/protocol/livekit"

	"tide/internal/store"
)

func TestWebhookEgressStateTransitions(t *testing.T) {
	handler, _, rooms, room := recordingTestHandler(t)
	db := handler.store.(*store.Store)
	recording := store.Recording{
		ID: "webhook-recording", RoomID: room.ID, RoomSlug: room.Slug,
		EgressID: "egress-webhook", Status: "starting", StartedBy: "owner", StartedAt: 100,
	}
	if err := db.InsertRecording(context.Background(), recording); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/webhook", nil)

	// Synthetic events intentionally bypass receiver signature verification;
	// Webhook itself is wired separately to the verified receiver seam.
	for _, transition := range []struct {
		event  string
		status protocol.EgressStatus
		want   string
	}{
		{"egress_started", protocol.EgressStatus_EGRESS_STARTING, "starting"},
		{"egress_updated", protocol.EgressStatus_EGRESS_ACTIVE, "recording"},
		{"egress_updated", protocol.EgressStatus_EGRESS_ENDING, "finalizing"},
	} {
		err := handler.HandleWebhookEvent(request, &protocol.WebhookEvent{
			Event:      transition.event,
			EgressInfo: &protocol.EgressInfo{EgressId: recording.EgressID, RoomName: room.Slug, Status: transition.status},
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := db.RecordingByID(context.Background(), recording.ID)
		if err != nil || got.Status != transition.want {
			t.Fatalf("after %s recording = %#v, %v", transition.event, got, err)
		}
	}

	endedAt := time.Unix(130, 0).UnixNano()
	err := handler.HandleWebhookEvent(request, &protocol.WebhookEvent{
		Event: "egress_ended",
		EgressInfo: &protocol.EgressInfo{
			EgressId: recording.EgressID, RoomName: room.Slug,
			Status: protocol.EgressStatus_EGRESS_COMPLETE, EndedAt: endedAt,
			FileResults: []*protocol.FileInfo{{
				Filename: "recordings/calm-otter-412/100.mp4",
				Duration: 30 * int64(time.Second), Size: 1_234_567,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.RecordingByID(context.Background(), recording.ID)
	if err != nil || got.Status != "completed" || got.EndedAt == nil || *got.EndedAt != 130 ||
		got.DurationS == nil || *got.DurationS != 30 || got.SizeBytes == nil || *got.SizeBytes != 1_234_567 ||
		got.S3Key == nil || *got.S3Key != "recordings/calm-otter-412/100.mp4" {
		t.Fatalf("completed recording = %#v, %v", got, err)
	}
	if len(rooms.updates) != 1 || rooms.updates[0].Metadata != `{"recording":false}` {
		t.Fatalf("ended metadata updates = %#v", rooms.updates)
	}
}

func TestWebhookFailureAndUnknownEvent(t *testing.T) {
	handler, _, rooms, room := recordingTestHandler(t)
	db := handler.store.(*store.Store)
	recording := store.Recording{
		ID: "failed-recording", RoomID: room.ID, RoomSlug: room.Slug,
		EgressID: "egress-failed", Status: "recording", StartedBy: "owner", StartedAt: 100,
	}
	if err := db.InsertRecording(context.Background(), recording); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/webhook", nil)
	if err := handler.HandleWebhookEvent(request, &protocol.WebhookEvent{Event: "room_started"}); err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleWebhookEvent(request, &protocol.WebhookEvent{
		Event: "egress_ended",
		EgressInfo: &protocol.EgressInfo{
			EgressId: recording.EgressID, RoomName: room.Slug,
			Status: protocol.EgressStatus_EGRESS_FAILED, Error: "encoder failed",
		},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := db.RecordingByID(context.Background(), recording.ID)
	if err != nil || got.Status != "failed" {
		t.Fatalf("failed recording = %#v, %v", got, err)
	}
	if len(rooms.updates) != 1 || rooms.updates[0].Metadata != `{"recording":false}` {
		t.Fatalf("metadata updates = %#v", rooms.updates)
	}
}

func TestWebhookParticipantJoinedHook(t *testing.T) {
	handler, _, _, room := recordingTestHandler(t)
	var gotRoom, gotIdentity string
	handler.SetParticipantJoinedHook(func(_ context.Context, roomName, identity string) {
		gotRoom, gotIdentity = roomName, identity
	})
	request := httptest.NewRequest("POST", "/webhook", nil)
	if err := handler.HandleWebhookEvent(request, &protocol.WebhookEvent{
		Event:       "participant_joined",
		Room:        &protocol.Room{Name: room.Slug},
		Participant: &protocol.ParticipantInfo{Identity: "guest:1234"},
	}); err != nil {
		t.Fatal(err)
	}
	if gotRoom != room.Slug || gotIdentity != "guest:1234" {
		t.Fatalf("hook got (%q, %q)", gotRoom, gotIdentity)
	}
	// Events without room/participant payloads must not panic or fire the hook.
	gotIdentity = ""
	if err := handler.HandleWebhookEvent(request, &protocol.WebhookEvent{Event: "participant_joined"}); err != nil {
		t.Fatal(err)
	}
	if gotIdentity != "" {
		t.Fatal("hook must not fire without a participant payload")
	}
}

// readingReceiver mimics the pinned LiveKit receiver, which reads the whole
// body before verifying anything.
type readingReceiver struct{}

func (readingReceiver) Receive(r *http.Request) (*protocol.WebhookEvent, error) {
	if _, err := io.ReadAll(r.Body); err != nil {
		return nil, err
	}
	return &protocol.WebhookEvent{Event: "room_started"}, nil
}

func TestWebhookRejectsOversizedBody(t *testing.T) {
	handler, _, _, _ := recordingTestHandler(t)
	handler.receiver = readingReceiver{}

	response := httptest.NewRecorder()
	handler.Webhook(response, httptest.NewRequest("POST", "/webhook", bytes.NewReader(make([]byte, maxWebhookBody+1))))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for an oversized body, got %d", response.Code)
	}

	response = httptest.NewRecorder()
	handler.Webhook(response, httptest.NewRequest("POST", "/webhook", bytes.NewReader([]byte(`{}`))))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200 for a small body, got %d", response.Code)
	}
}

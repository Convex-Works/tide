package recording

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	protocol "github.com/livekit/protocol/livekit"

	"klisi/internal/auth"
	"klisi/internal/store"
)

type fakeEgressClient struct {
	starts   []*protocol.RoomCompositeEgressRequest
	stops    []*protocol.StopEgressRequest
	stopErr  error
	onStop   func()
	listErr  error
	egresses []*protocol.EgressInfo
}

func (f *fakeEgressClient) ListEgress(_ context.Context, request *protocol.ListEgressRequest) (*protocol.ListEgressResponse, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	items := make([]*protocol.EgressInfo, 0)
	for _, info := range f.egresses {
		if request.EgressId == "" || info.EgressId == request.EgressId {
			items = append(items, info)
		}
	}
	return &protocol.ListEgressResponse{Items: items}, nil
}

func (f *fakeEgressClient) StartRoomCompositeEgress(_ context.Context, request *protocol.RoomCompositeEgressRequest) (*protocol.EgressInfo, error) {
	f.starts = append(f.starts, request)
	return &protocol.EgressInfo{EgressId: "egress-" + string(rune('0'+len(f.starts)))}, nil
}

func (f *fakeEgressClient) StopEgress(_ context.Context, request *protocol.StopEgressRequest) (*protocol.EgressInfo, error) {
	f.stops = append(f.stops, request)
	if f.onStop != nil {
		f.onStop()
	}
	if f.stopErr != nil {
		return nil, f.stopErr
	}
	return &protocol.EgressInfo{EgressId: request.EgressId}, nil
}

type fakeRoomService struct {
	updates []*protocol.UpdateRoomMetadataRequest
}

func (f *fakeRoomService) UpdateRoomMetadata(_ context.Context, request *protocol.UpdateRoomMetadataRequest) (*protocol.Room, error) {
	f.updates = append(f.updates, request)
	return &protocol.Room{Name: request.Room, Metadata: request.Metadata}, nil
}

type fakeObjectStore struct{}

func (fakeObjectStore) Remove(context.Context, string) error { return nil }
func (fakeObjectStore) PresignedGet(context.Context, string, time.Duration) (string, error) {
	return "http://minio.example/download", nil
}

func recordingTestHandler(t *testing.T) (*Handler, *fakeEgressClient, *fakeRoomService, store.Room) {
	t.Helper()
	db, err := store.Open("file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	room := store.Room{
		ID: "room-1", Slug: "calm-otter-412", Name: "Weekly",
		OwnerSub: "owner", LobbyEnabled: true, CreatedAt: 1,
	}
	if err := db.CreateRoom(context.Background(), room); err != nil {
		t.Fatal(err)
	}
	egress := &fakeEgressClient{}
	rooms := &fakeRoomService{}
	handler := NewHandler(db, egress, rooms, fakeObjectStore{}, "http://egress-template.example", nil)
	handler.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	handler.newID = func() (string, error) { return "recording-1", nil }
	return handler, egress, rooms, room
}

func ownerRequest(method string, room store.Room, sub string) *http.Request {
	request := httptest.NewRequest(method, "/recording", nil)
	request.SetPathValue("slug", room.Slug)
	if sub != "" {
		request = request.WithContext(auth.WithSession(request.Context(), auth.Session{Sub: sub}))
	}
	return request
}

func TestStartStopAuthorizationAndDoubleStart(t *testing.T) {
	t.Run("start requires authentication and ownership", func(t *testing.T) {
		for _, test := range []struct {
			name string
			sub  string
			want int
		}{{"signed out", "", http.StatusUnauthorized}, {"non-owner", "other", http.StatusForbidden}} {
			t.Run(test.name, func(t *testing.T) {
				handler, egress, _, room := recordingTestHandler(t)
				response := httptest.NewRecorder()
				handler.Start(response, ownerRequest(http.MethodPost, room, test.sub))
				if response.Code != test.want || len(egress.starts) != 0 {
					t.Fatalf("status = %d, starts = %d, body = %s", response.Code, len(egress.starts), response.Body.String())
				}
			})
		}
	})

	t.Run("owner starts once and stops", func(t *testing.T) {
		handler, egress, rooms, room := recordingTestHandler(t)
		first := httptest.NewRecorder()
		handler.Start(first, ownerRequest(http.MethodPost, room, "owner"))
		if first.Code != http.StatusCreated || len(egress.starts) != 1 {
			t.Fatalf("first start status = %d, body = %s", first.Code, first.Body.String())
		}
		start := egress.starts[0]
		if start.RoomName != room.Slug || start.CustomBaseUrl != "http://egress-template.example" ||
			len(start.FileOutputs) != 1 || start.FileOutputs[0].Filepath != "recordings/calm-otter-412/1700000000.mp4" {
			t.Fatalf("start request = %#v", start)
		}

		second := httptest.NewRecorder()
		handler.Start(second, ownerRequest(http.MethodPost, room, "owner"))
		if second.Code != http.StatusConflict || len(egress.starts) != 1 {
			t.Fatalf("second start status = %d, starts = %d", second.Code, len(egress.starts))
		}

		for _, sub := range []string{"", "other"} {
			response := httptest.NewRecorder()
			handler.Stop(response, ownerRequest(http.MethodPost, room, sub))
			want := http.StatusUnauthorized
			if sub != "" {
				want = http.StatusForbidden
			}
			if response.Code != want || len(egress.stops) != 0 {
				t.Fatalf("stop sub %q status = %d, stops = %d", sub, response.Code, len(egress.stops))
			}
		}

		stopped := httptest.NewRecorder()
		handler.Stop(stopped, ownerRequest(http.MethodPost, room, "owner"))
		if stopped.Code != http.StatusOK || len(egress.stops) != 1 {
			t.Fatalf("owner stop status = %d, body = %s", stopped.Code, stopped.Body.String())
		}
		if len(rooms.updates) != 2 || rooms.updates[0].Metadata != `{"recording":true}` || rooms.updates[1].Metadata != `{"recording":false}` {
			t.Fatalf("metadata updates = %#v", rooms.updates)
		}
	})
}

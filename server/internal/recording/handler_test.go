package recording

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	protocol "github.com/livekit/protocol/livekit"

	"tide/internal/api"
	"tide/internal/auth"
	"tide/internal/store"
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

// fakeObjectStore removes objects, or fails to while down is set.
type fakeObjectStore struct {
	removed []string
	down    bool
}

func (f *fakeObjectStore) Remove(_ context.Context, key string) error {
	if f.down {
		return errors.New("storage is down")
	}
	f.removed = append(f.removed, key)
	return nil
}

func (*fakeObjectStore) PresignedGet(context.Context, string, time.Duration) (string, error) {
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
	handler := NewHandler(db, egress, rooms, &fakeObjectStore{}, "http://egress-template.example", nil)
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
			len(start.FileOutputs) != 1 || start.FileOutputs[0].FileType != protocol.EncodedFileType_OGG ||
			start.FileOutputs[0].Filepath != "recordings/calm-otter-412/recording-1/2023-11-14 22-13 - Weekly.ogg" {
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

func TestStartRecordingModes(t *testing.T) {
	startWithBody := func(t *testing.T, body string) (*httptest.ResponseRecorder, *fakeEgressClient) {
		t.Helper()
		handler, egress, _, room := recordingTestHandler(t)
		request := httptest.NewRequest(http.MethodPost, "/recording", strings.NewReader(body))
		if body == "" {
			request = httptest.NewRequest(http.MethodPost, "/recording", nil)
		}
		request.SetPathValue("slug", room.Slug)
		request = request.WithContext(auth.WithSession(request.Context(), auth.Session{Sub: "owner"}))
		response := httptest.NewRecorder()
		handler.Start(response, request)
		return response, egress
	}

	decodeInfo := func(t *testing.T, response *httptest.ResponseRecorder) api.RecordingInfo {
		t.Helper()
		var info api.RecordingInfo
		if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
			t.Fatal(err)
		}
		return info
	}

	t.Run("empty body defaults to audio-only", func(t *testing.T) {
		response, egress := startWithBody(t, "")
		if response.Code != http.StatusCreated || len(egress.starts) != 1 {
			t.Fatalf("status = %d, starts = %d, body = %s", response.Code, len(egress.starts), response.Body.String())
		}
		if !egress.starts[0].AudioOnly {
			t.Fatalf("egress request AudioOnly = false, want true")
		}
		if info := decodeInfo(t, response); !info.AudioOnly {
			t.Fatalf("response audio_only = false, want true")
		}
	})

	t.Run("explicit video opts out of audio-only", func(t *testing.T) {
		response, egress := startWithBody(t, `{"video":true}`)
		if response.Code != http.StatusCreated || len(egress.starts) != 1 {
			t.Fatalf("status = %d, starts = %d, body = %s", response.Code, len(egress.starts), response.Body.String())
		}
		if egress.starts[0].AudioOnly {
			t.Fatalf("egress request AudioOnly = true, want false")
		}
		if info := decodeInfo(t, response); info.AudioOnly {
			t.Fatalf("response audio_only = true, want false")
		}
	})

	t.Run("explicit false stays audio-only", func(t *testing.T) {
		response, egress := startWithBody(t, `{"video":false}`)
		if response.Code != http.StatusCreated || len(egress.starts) != 1 || !egress.starts[0].AudioOnly {
			t.Fatalf("status = %d, request = %#v", response.Code, egress.starts)
		}
	})

	t.Run("invalid body rejected", func(t *testing.T) {
		response, egress := startWithBody(t, `{"video":`)
		if response.Code != http.StatusBadRequest || len(egress.starts) != 0 {
			t.Fatalf("status = %d, starts = %d", response.Code, len(egress.starts))
		}
	})
}

func TestStartRecordingFileOutput(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		fileType  protocol.EncodedFileType
		extension string
		mode      string
	}{
		{name: "audio", body: "", fileType: protocol.EncodedFileType_OGG, extension: ".ogg", mode: "audio"},
		{name: "video", body: `{"video":true}`, fileType: protocol.EncodedFileType_MP4, extension: ".mp4", mode: "video"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, egress, _, room := recordingTestHandler(t)
			handler.s3Output = &protocol.S3Upload{
				Bucket: "recordings",
				Metadata: map[string]string{
					"deployment": "production",
				},
				ContentDisposition: "attachment; filename=old",
			}
			request := ownerRequest(http.MethodPost, room, "owner")
			if test.body != "" {
				request = httptest.NewRequest(http.MethodPost, "/recording", strings.NewReader(test.body))
				request.SetPathValue("slug", room.Slug)
				request = request.WithContext(auth.WithSession(request.Context(), auth.Session{Sub: "owner"}))
			}
			response := httptest.NewRecorder()
			handler.Start(response, request)
			if response.Code != http.StatusCreated || len(egress.starts) != 1 {
				t.Fatalf("status = %d, starts = %d, body = %s", response.Code, len(egress.starts), response.Body.String())
			}

			output := egress.starts[0].FileOutputs[0]
			filename := "2023-11-14 22-13 - Weekly" + test.extension
			wantKey := "recordings/calm-otter-412/recording-1/" + filename
			if output.FileType != test.fileType || output.Filepath != wantKey {
				t.Fatalf("file output = %#v, want type %s and path %q", output, test.fileType, wantKey)
			}
			s3 := output.GetS3()
			if s3 == nil {
				t.Fatal("S3 output is nil")
			}
			wantMetadata := map[string]string{
				"deployment":          "production",
				"tide-filename":       filename,
				"tide-meeting-id":     room.ID,
				"tide-meeting-name":   room.Name,
				"tide-meeting-slug":   room.Slug,
				"tide-recording-id":   "recording-1",
				"tide-recording-mode": test.mode,
				"tide-started-at":     "2023-11-14T22:13:20Z",
				"tide-started-by":     "owner",
			}
			if !reflect.DeepEqual(s3.Metadata, wantMetadata) {
				t.Fatalf("metadata = %#v, want %#v", s3.Metadata, wantMetadata)
			}
			if want := `attachment; filename="` + filename + `"`; s3.ContentDisposition != want {
				t.Fatalf("content disposition = %q, want %q", s3.ContentDisposition, want)
			}
			if !reflect.DeepEqual(handler.s3Output.Metadata, map[string]string{"deployment": "production"}) ||
				handler.s3Output.ContentDisposition != "attachment; filename=old" {
				t.Fatalf("configured S3 destination was mutated: %#v", handler.s3Output)
			}
		})
	}
}

// Deleting a recording removes its file and its transcript sidecars, which
// sit beside it, and tells the transcripts reconciler.
func TestDeleteRemovesRecordingAndSidecars(t *testing.T) {
	handler, _, _, room := recordingTestHandler(t)
	objects := handler.objects.(*fakeObjectStore)
	changed := 0
	handler.SetRecordingsChangedHook(func() { changed++ })
	key := "recordings/calm-otter-412/rec-1/2023-11-14 22-13 - Weekly.ogg"
	endedAt := int64(1_700_000_600)
	if err := handler.store.InsertRecording(context.Background(), store.Recording{
		ID: "rec-1", RoomID: room.ID, RoomSlug: room.Slug, EgressID: "egress-1", Status: "completed",
		StartedBy: "owner", StartedAt: 1_700_000_000, AudioOnly: true, EndedAt: &endedAt, S3Key: &key,
	}); err != nil {
		t.Fatal(err)
	}
	remove := func(sub string) int {
		request := httptest.NewRequest(http.MethodDelete, "/api/recordings/rec-1", nil)
		request.SetPathValue("id", "rec-1")
		request = request.WithContext(auth.WithSession(request.Context(), auth.Session{Sub: sub}))
		response := httptest.NewRecorder()
		handler.Delete(response, request)
		return response.Code
	}

	if status := remove("other"); status != http.StatusForbidden || len(objects.removed) != 0 || changed != 0 {
		t.Fatalf("delete by another host: %d, removed %v", status, objects.removed)
	}
	if status := remove("owner"); status != http.StatusNoContent {
		t.Fatalf("delete: %d", status)
	}
	want := []string{
		key,
		"recordings/calm-otter-412/rec-1/2023-11-14 22-13 - Weekly.txt",
		"recordings/calm-otter-412/rec-1/2023-11-14 22-13 - Weekly.vtt",
	}
	if !reflect.DeepEqual(objects.removed, want) {
		t.Fatalf("removed %q, want %q", objects.removed, want)
	}
	if _, err := handler.store.RecordingByID(context.Background(), "rec-1"); err == nil {
		t.Fatal("the recording is still there")
	}
	if changed != 1 {
		t.Fatalf("the reconciler heard of %d changes", changed)
	}
	if queued := dueRemovals(t, handler); len(queued) != 0 {
		t.Fatalf("still queued for removal: %q", queued)
	}
}

// Deleting a recording succeeds while storage is down: its files stay
// queued, and the reconciler removes them once storage is back.
func TestDeleteSucceedsWhileStorageIsDown(t *testing.T) {
	handler, _, _, room := recordingTestHandler(t)
	objects := handler.objects.(*fakeObjectStore)
	key := "recordings/calm-otter-412/rec-1/2023-11-14 22-13 - Weekly.ogg"
	if err := handler.store.InsertRecording(context.Background(), store.Recording{
		ID: "rec-1", RoomID: room.ID, RoomSlug: room.Slug, EgressID: "egress-1", Status: "completed",
		StartedBy: "owner", StartedAt: 1_700_000_000, AudioOnly: true, S3Key: &key,
	}); err != nil {
		t.Fatal(err)
	}
	files := []string{
		key,
		"recordings/calm-otter-412/rec-1/2023-11-14 22-13 - Weekly.txt",
		"recordings/calm-otter-412/rec-1/2023-11-14 22-13 - Weekly.vtt",
	}

	objects.down = true
	request := httptest.NewRequest(http.MethodDelete, "/api/recordings/rec-1", nil)
	request.SetPathValue("id", "rec-1")
	request = request.WithContext(auth.WithSession(request.Context(), auth.Session{Sub: "owner"}))
	response := httptest.NewRecorder()
	handler.Delete(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete while storage is down: %d %s", response.Code, response.Body)
	}
	if _, err := handler.store.RecordingByID(context.Background(), "rec-1"); err == nil {
		t.Fatal("the recording is still there")
	}
	if queued := dueRemovals(t, handler); !reflect.DeepEqual(queued, files) {
		t.Fatalf("queued %q, want %q", queued, files)
	}

	// A pass while storage is still down keeps them queued.
	handler.reconcile(context.Background())
	if queued := dueRemovals(t, handler); len(queued) != len(files) || len(objects.removed) != 0 {
		t.Fatalf("after a pass with storage down: queued %q, removed %q", queued, objects.removed)
	}
	objects.down = false
	handler.reconcile(context.Background())
	slices.Sort(objects.removed)
	if !reflect.DeepEqual(objects.removed, files) {
		t.Fatalf("removed %q, want %q", objects.removed, files)
	}
	if queued := dueRemovals(t, handler); len(queued) != 0 {
		t.Fatalf("still queued: %q", queued)
	}
}

func dueRemovals(t *testing.T, handler *Handler) []string {
	t.Helper()
	keys, err := handler.store.DueRemovals(context.Background(), 1<<40, 100)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func TestRecordingFilename(t *testing.T) {
	started := time.Date(2026, time.August, 2, 12, 34, 0, 0, time.FixedZone("EEST", 3*60*60))
	for _, test := range []struct {
		name string
		want string
	}{
		{name: "  Product / planning\\notes: Q3?\n", want: "2026-08-02 09-34 - Product - planning-notes- Q3-.ogg"},
		{name: "...", want: "2026-08-02 09-34 - meeting.ogg"},
		{name: "Συνάντηση", want: "2026-08-02 09-34 - Συνάντηση.ogg"},
	} {
		if got := recordingFilename(started, test.name, ".ogg"); got != test.want {
			t.Errorf("recordingFilename(%q) = %q, want %q", test.name, got, test.want)
		}
	}
}

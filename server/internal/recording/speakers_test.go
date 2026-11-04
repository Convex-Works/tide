package recording

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	protocol "github.com/livekit/protocol/livekit"

	"tide/internal/api"
	"tide/internal/config"
	"tide/internal/speakers"
	"tide/internal/store"
)

// fileStart is when egress says the recording's file begins, some time after
// tide started the recording.
var fileStart = time.Unix(1_700_000_010, 0)

const speakersFileKey = "recordings/calm-otter-412/rec-1/speakers.json"

// fakeObservers hands out activity of its own making, and says what it was
// told to do.
type fakeObservers struct {
	mu       sync.Mutex
	started  []string
	stopped  []string
	closed   int
	activity map[string]*speakers.Activity
}

func (f *fakeObservers) Start(recordingID, room string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, recordingID+" in "+room)
}

func (f *fakeObservers) Stop(recordingID string) *speakers.Activity {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, recordingID)
	activity := f.activity[recordingID]
	delete(f.activity, recordingID)
	return activity
}

func (f *fakeObservers) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
}

// adaSpoke is an activity with Ada speaking from 2.5 s into the file to 4 s.
func adaSpoke() *speakers.Activity {
	activity := speakers.NewActivity()
	activity.Update(fileStart.Add(2500*time.Millisecond), []speakers.Speaker{{Identity: "guest:1", Name: "Ada"}})
	activity.Update(fileStart.Add(4*time.Second), nil)
	return activity
}

func withObservers(handler *Handler, activity map[string]*speakers.Activity) *fakeObservers {
	observers := &fakeObservers{activity: activity}
	handler.SetSpeakerObservers(observers)
	return observers
}

func endedEgress(status protocol.EgressStatus) *protocol.EgressInfo {
	return &protocol.EgressInfo{
		EgressId: "egress-1", RoomName: "calm-otter-412", Status: status,
		EndedAt: fileStart.Add(30 * time.Second).UnixNano(),
		FileResults: []*protocol.FileInfo{{
			Filename:  "recordings/calm-otter-412/rec-1/2026-09-27 14-00 - Weekly.ogg",
			StartedAt: fileStart.UnixNano(), EndedAt: fileStart.Add(30 * time.Second).UnixNano(),
			Duration: int64(30 * time.Second), Size: 1_000,
		}},
	}
}

// The two ways a recording ends: the webhook, and the reconciler healing a
// webhook that was lost.
var endings = []struct {
	name string
	end  func(*testing.T, *Handler, *fakeEgressClient, *protocol.EgressInfo)
}{
	{"webhook", func(t *testing.T, handler *Handler, _ *fakeEgressClient, info *protocol.EgressInfo) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, api.LiveKitWebhookPath, nil)
		if err := handler.HandleWebhookEvent(request, &protocol.WebhookEvent{Event: "egress_ended", EgressInfo: info}); err != nil {
			t.Fatal(err)
		}
	}},
	{"reconciler", func(_ *testing.T, handler *Handler, egress *fakeEgressClient, info *protocol.EgressInfo) {
		egress.egresses = []*protocol.EgressInfo{info}
		handler.reconcile(context.Background())
	}},
}

// A transcript job can start as soon as the row says completed, and it takes
// the timeline only if it finds it then.
func TestTheTimelineIsStoredBeforeTheRecordingCompletes(t *testing.T) {
	for _, ending := range endings {
		t.Run(ending.name, func(t *testing.T) {
			handler, egress, _, room := recordingTestHandler(t)
			insertRecording(t, handler, room, "rec-1", "egress-1", "recording", 100)
			observers := withObservers(handler, map[string]*speakers.Activity{"rec-1": adaSpoke()})
			objects := handler.objects.(*fakeObjectStore)
			var statusAtPut string
			objects.onPut = func() { statusAtPut = recordingStatus(t, handler, "rec-1") }

			ending.end(t, handler, egress, endedEgress(protocol.EgressStatus_EGRESS_COMPLETE))

			if statusAtPut != "recording" {
				t.Fatalf("the row said %q as the timeline was stored", statusAtPut)
			}
			if got := recordingStatus(t, handler, "rec-1"); got != "completed" {
				t.Fatalf("status = %q", got)
			}
			if len(objects.puts) != 1 {
				t.Fatalf("stored %d objects", len(objects.puts))
			}
			put := objects.puts[0]
			if put.key != speakersFileKey || put.contentType != "application/json" {
				t.Fatalf("stored %q as %q", put.key, put.contentType)
			}
			if got := string(put.body); got != `{"v":1,"participants":[{"name":"Ada","spans":[[2.5,4]]}]}` {
				t.Fatalf("timeline = %s", got)
			}
			if !reflect.DeepEqual(observers.stopped, []string{"rec-1"}) {
				t.Fatalf("stopped = %v", observers.stopped)
			}
		})
	}
}

// The webhook and the reconciler can finish a recording at once. The one that
// finds the observer already stopped must wait: it would otherwise complete
// the row with the timeline still on its way to storage.
func TestARacingEndWaitsForTheTimeline(t *testing.T) {
	handler, egress, _, room := recordingTestHandler(t)
	insertRecording(t, handler, room, "rec-1", "egress-1", "recording", 100)
	withObservers(handler, map[string]*speakers.Activity{"rec-1": adaSpoke()})
	info := endedEgress(protocol.EgressStatus_EGRESS_COMPLETE)
	egress.egresses = []*protocol.EgressInfo{info}
	objects := handler.objects.(*fakeObjectStore)
	storing, release := make(chan struct{}), make(chan struct{})
	objects.onPut = func() {
		close(storing)
		<-release
	}
	first := make(chan struct{})
	go func() {
		defer close(first)
		endings[0].end(t, handler, egress, info)
	}()
	<-storing

	second := make(chan struct{})
	go func() {
		defer close(second)
		handler.reconcile(context.Background())
	}()
	select {
	case <-second:
		t.Fatal("the reconciler finished the recording while the timeline was being stored")
	case <-time.After(100 * time.Millisecond):
	}
	if got := recordingStatus(t, handler, "rec-1"); got != "recording" {
		t.Fatalf("status = %q with the timeline still being stored", got)
	}
	close(release)
	<-first
	<-second
	if got := recordingStatus(t, handler, "rec-1"); got != "completed" {
		t.Fatalf("status = %q", got)
	}
}

// Storage refusing the timeline costs the names, not the recording.
func TestAFailedPutDoesntStopTheRecordingCompleting(t *testing.T) {
	for _, ending := range endings {
		t.Run(ending.name, func(t *testing.T) {
			handler, egress, _, room := recordingTestHandler(t)
			insertRecording(t, handler, room, "rec-1", "egress-1", "recording", 100)
			withObservers(handler, map[string]*speakers.Activity{"rec-1": adaSpoke()})
			handler.objects.(*fakeObjectStore).down = true

			ending.end(t, handler, egress, endedEgress(protocol.EgressStatus_EGRESS_COMPLETE))

			if got := recordingStatus(t, handler, "rec-1"); got != "completed" {
				t.Fatalf("status = %q", got)
			}
		})
	}
}

// What leaves nothing to store still stops the observer, and completes or
// fails as ever.
func TestNoTimelineIsStored(t *testing.T) {
	timed := func(change func(*protocol.FileInfo)) *protocol.EgressInfo {
		info := endedEgress(protocol.EgressStatus_EGRESS_COMPLETE)
		change(info.FileResults[0])
		return info
	}
	failed := endedEgress(protocol.EgressStatus_EGRESS_FAILED)
	tests := []struct {
		name     string
		info     *protocol.EgressInfo
		activity *speakers.Activity
		status   string
	}{
		{"the recording failed", failed, adaSpoke(), "failed"},
		{"the observer never connected", endedEgress(protocol.EgressStatus_EGRESS_COMPLETE), speakers.NewActivity(), "completed"},
		{"there is no observer, as after a restart", endedEgress(protocol.EgressStatus_EGRESS_COMPLETE), nil, "completed"},
		{"egress doesn't say when the file began", timed(func(f *protocol.FileInfo) { f.StartedAt = 0 }), adaSpoke(), "completed"},
		{"nothing happened inside the file", timed(func(f *protocol.FileInfo) { f.Duration = int64(time.Second) }), adaSpoke(), "completed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, egress, _, room := recordingTestHandler(t)
			insertRecording(t, handler, room, "rec-1", "egress-1", "recording", 100)
			observers := withObservers(handler, map[string]*speakers.Activity{"rec-1": test.activity})
			endings[0].end(t, handler, egress, test.info)
			if got := recordingStatus(t, handler, "rec-1"); got != test.status {
				t.Fatalf("status = %q, want %q", got, test.status)
			}
			if puts := handler.objects.(*fakeObjectStore).puts; len(puts) != 0 {
				t.Fatalf("stored %v", puts)
			}
			if len(observers.stopped) != 1 {
				t.Fatalf("stopped = %v", observers.stopped)
			}
		})
	}
}

// Egress may report only when its file ended, not its length.
func TestALengthIsWorkedOutFromTheFilesEnd(t *testing.T) {
	handler, egress, _, room := recordingTestHandler(t)
	insertRecording(t, handler, room, "rec-1", "egress-1", "recording", 100)
	withObservers(handler, map[string]*speakers.Activity{"rec-1": adaSpoke()})
	info := endedEgress(protocol.EgressStatus_EGRESS_COMPLETE)
	info.FileResults[0].Duration = 0
	info.FileResults[0].EndedAt = fileStart.Add(3 * time.Second).UnixNano()
	endings[0].end(t, handler, egress, info)
	var timeline speakers.Timeline
	puts := handler.objects.(*fakeObjectStore).puts
	if len(puts) != 1 {
		t.Fatalf("stored %d objects", len(puts))
	}
	if err := json.Unmarshal(puts[0].body, &timeline); err != nil {
		t.Fatal(err)
	}
	if got := timeline.Participants[0].Spans; !reflect.DeepEqual(got, [][2]float64{{2.5, 3}}) {
		t.Fatalf("spans = %v", got)
	}
}

// A recording the reconciler gives up on stops its observer too, whose
// activity goes with it.
func TestTheReconcilerFailingARecordingStopsItsObserver(t *testing.T) {
	handler, _, _, room := recordingTestHandler(t)
	insertRecording(t, handler, room, "rec-1", "egress-1", "recording", handler.now().Add(-10*time.Minute).Unix())
	observers := withObservers(handler, map[string]*speakers.Activity{"rec-1": adaSpoke()})
	handler.reconcile(context.Background())
	if got := recordingStatus(t, handler, "rec-1"); got != "failed" {
		t.Fatalf("status = %q", got)
	}
	if !reflect.DeepEqual(observers.stopped, []string{"rec-1"}) {
		t.Fatalf("stopped = %v", observers.stopped)
	}
	if puts := handler.objects.(*fakeObjectStore).puts; len(puts) != 0 {
		t.Fatalf("stored %v", puts)
	}
}

// A recording the reconciler leaves running keeps its observer.
func TestARunningRecordingKeepsItsObserver(t *testing.T) {
	handler, egress, _, room := recordingTestHandler(t)
	insertRecording(t, handler, room, "rec-1", "egress-1", "recording", 100)
	observers := withObservers(handler, nil)
	egress.egresses = []*protocol.EgressInfo{{EgressId: "egress-1", Status: protocol.EgressStatus_EGRESS_ACTIVE}}
	handler.reconcile(context.Background())
	if len(observers.stopped) != 0 {
		t.Fatalf("stopped = %v", observers.stopped)
	}
}

func TestStartStartsAnObserverOnceEgressHasStarted(t *testing.T) {
	t.Run("started", func(t *testing.T) {
		handler, egress, _, room := recordingTestHandler(t)
		observers := withObservers(handler, nil)
		response := httptest.NewRecorder()
		handler.Start(response, ownerRequest(http.MethodPost, room, room.OwnerSub))
		if response.Code != http.StatusCreated {
			t.Fatalf("Start = %d: %s", response.Code, response.Body)
		}
		if !reflect.DeepEqual(observers.started, []string{"recording-1 in " + room.Slug}) || len(egress.starts) != 1 {
			t.Fatalf("started = %v, egress starts = %d", observers.started, len(egress.starts))
		}
		if len(observers.stopped) != 0 {
			t.Fatalf("stopped = %v", observers.stopped)
		}
	})
	t.Run("egress refused", func(t *testing.T) {
		handler, egress, _, room := recordingTestHandler(t)
		observers := withObservers(handler, nil)
		egress.startErr = errors.New("no recorder")
		response := httptest.NewRecorder()
		handler.Start(response, ownerRequest(http.MethodPost, room, room.OwnerSub))
		if response.Code != http.StatusBadGateway {
			t.Fatalf("Start = %d", response.Code)
		}
		if len(observers.started) != 0 {
			t.Fatalf("started = %v", observers.started)
		}
	})
	t.Run("the room's state can't be updated", func(t *testing.T) {
		handler, _, rooms, room := recordingTestHandler(t)
		observers := withObservers(handler, nil)
		rooms.err = errors.New("room gone")
		response := httptest.NewRecorder()
		handler.Start(response, ownerRequest(http.MethodPost, room, room.OwnerSub))
		if response.Code != http.StatusBadGateway {
			t.Fatalf("Start = %d", response.Code)
		}
		if !reflect.DeepEqual(observers.stopped, []string{"recording-1"}) {
			t.Fatalf("an observer outlived its Start: stopped = %v", observers.stopped)
		}
	})
}

// Only transcripts read the timeline, so observers need recording and moil
// (ARCHITECTURE.md §8).
func TestObserversNeedRecordingAndMoil(t *testing.T) {
	if handler := New(config.Config{Moil: true}, nil, nil); handler.speakers != nil {
		t.Fatal("there are observers with recording off")
	}
	storage := config.Config{S3Endpoint: "http://s3.example"}
	if handler := New(storage, nil, NewMinIOStore(storage)); handler.speakers != nil {
		t.Fatal("there are observers with moil off")
	}
	storage.Moil = true
	handler := New(storage, nil, NewMinIOStore(storage))
	if handler.speakers == nil {
		t.Fatal("there are no observers with recording and moil on")
	}
	handler.CloseObservers()
}

func TestMinIOStorePutsThroughTheServerEndpoint(t *testing.T) {
	var key, contentType, body string
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := new(strings.Builder)
		buffer := make([]byte, 1024)
		for {
			n, err := r.Body.Read(buffer)
			data.Write(buffer[:n])
			if err != nil {
				break
			}
		}
		key, contentType, body = r.URL.Path, r.Header.Get("Content-Type"), data.String()
		w.Header().Set("ETag", `"0123abcd"`)
	}))
	defer s3.Close()
	objects := NewMinIOStore(config.Config{
		S3Endpoint: s3.URL, S3PublicEndpoint: "https://s3.public.example", S3Bucket: "tide",
		S3AccessKey: "key", S3SecretKey: "secret", S3Region: "us-east-1",
	})
	if err := objects.Put(context.Background(), speakersFileKey, []byte(`{"v":1}`), "application/json"); err != nil {
		t.Fatal(err)
	}
	if key != "/tide/"+speakersFileKey || contentType != "application/json" || !strings.Contains(body, `{"v":1}`) {
		t.Fatalf("stored %q as %q: %q", key, contentType, body)
	}
}

var _ SpeakerObservers = (*speakers.Observers)(nil)

func TestSpeakersKey(t *testing.T) {
	key := "recordings/calm-otter-412/rec-1/2026-09-27 14-00 - Weekly.ogg"
	if got := (store.Recording{S3Key: &key}).SpeakersKey(); got != speakersFileKey {
		t.Fatalf("SpeakersKey() = %q", got)
	}
	if got := (store.Recording{}).SpeakersKey(); got != "" {
		t.Fatalf("SpeakersKey() of a recording with no file = %q", got)
	}
}

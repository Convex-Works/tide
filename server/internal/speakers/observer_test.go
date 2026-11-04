package speakers_test

import (
	"context"
	"encoding/json"
	"net"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"

	"tide/internal/config"
	tidelivekit "tide/internal/livekit"
	"tide/internal/media"
	"tide/internal/speakers"
)

const (
	testKey    = "tide-test-key"
	testSecret = "tide-test-secret-0123456789-0123456789"
	room       = "calm-otter-412"
	patience   = 30 * time.Second
)

// A meeting is a real embedded media server, with tide's own settings: a
// room whose participants left closes after 5 seconds (dev's departure
// timeout), so these tests needn't wait the 20 production gives them.
type meeting struct {
	t      *testing.T
	server *media.Server
	rooms  *lksdk.RoomServiceClient
	minter *tidelivekit.Minter
}

func startMeeting(t *testing.T) *meeting {
	t.Helper()
	ports := func() (tcp [2]int, udp int) {
		var listeners []net.Listener
		for i := range tcp {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			listeners = append(listeners, l)
			tcp[i] = l.Addr().(*net.TCPAddr).Port
		}
		c, err := net.ListenPacket("udp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		udp = c.LocalAddr().(*net.UDPAddr).Port
		_ = c.Close()
		for _, l := range listeners {
			_ = l.Close()
		}
		return tcp, udp
	}
	tcp, udp := ports()
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()
	server, err := media.Start(ctx, media.Options{
		APIKey: testKey, APISecret: testSecret, Loopback: true, Dev: true,
		InternalPort: tcp[0], TCPPort: tcp[1], UDPPort: udp,
	})
	if err != nil {
		t.Fatalf("start the media server: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("close the media server: %v", err)
		}
	})
	return &meeting{
		t: t, server: server,
		rooms: lksdk.NewRoomServiceClient(server.URL(), testKey, testSecret),
		minter: tidelivekit.NewMinter(config.Config{
			LiveKitAPIKey: testKey, LiveKitAPISecret: testSecret,
		}),
	}
}

func (m *meeting) observers() *speakers.Observers {
	observers := speakers.New(m.server.URL(), m.minter)
	m.t.Cleanup(observers.Close)
	return observers
}

// A person is a participant with a tide guest token and, once they publish,
// a microphone they can speak into.
type person struct {
	m        *meeting
	room     *lksdk.Room
	setLevel func(uint8)
	stop     chan struct{}
	done     chan struct{}
}

func (m *meeting) join(identity, name string, callback *lksdk.RoomCallback) *person {
	m.t.Helper()
	token, err := m.minter.MintToken(identity, name, room, tidelivekit.RoleGuest, time.Hour)
	if err != nil {
		m.t.Fatal(err)
	}
	if callback == nil {
		callback = lksdk.NewRoomCallback()
	}
	connected, err := lksdk.ConnectToRoomWithToken(m.server.URL(), token, callback, lksdk.WithAutoSubscribe(false))
	if err != nil {
		m.t.Fatalf("%s joins: %v", identity, err)
	}
	p := &person{m: m, room: connected}
	m.t.Cleanup(p.leave)
	return p
}

// silent is the audio level, in -dBov, of a microphone nobody speaks into,
// and loud of one somebody does.
const (
	silent = 127
	loud   = 20
)

func (p *person) publish() {
	p.m.t.Helper()
	track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
	})
	if err != nil {
		p.m.t.Fatal(err)
	}
	if _, err := p.room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name: "mic", Source: livekit.TrackSource_MICROPHONE,
	}); err != nil {
		p.m.t.Fatalf("publish: %v", err)
	}
	var level atomic.Uint32
	level.Store(silent)
	p.setLevel = func(l uint8) { level.Store(uint32(l)) }
	p.stop, p.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		// An Opus frame of silence: LiveKit goes by the level the sender states.
		frame := []byte{0xf8, 0xff, 0xfe}
		for {
			select {
			case <-p.stop:
				return
			case <-ticker.C:
				now := uint8(level.Load())
				_ = track.WriteSample(pionmedia.Sample{Data: frame, Duration: 20 * time.Millisecond},
					&lksdk.SampleWriteOptions{AudioLevel: &now})
			}
		}
	}()
}

func (p *person) leave() {
	if p.stop != nil {
		close(p.stop)
		<-p.done
		p.stop = nil
	}
	p.room.Disconnect()
}

// goroutines are the stacks of every goroutine now.
func goroutines() string {
	stacks := make([]byte, 1<<24)
	return string(stacks[:runtime.Stack(stacks, true)])
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", patience, what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// spokeFor is how long an activity says name was active.
func spokeFor(activity *speakers.Activity, name string) time.Duration {
	var total time.Duration
	for _, participant := range activity.Timeline(time.Now().Add(-time.Hour), 2*time.Hour).Participants {
		if participant.Name == name {
			for _, span := range participant.Spans {
				total += time.Duration((span[1] - span[0]) * float64(time.Second))
			}
		}
	}
	return total
}

// observerIn says whether the recording's observer is in the room.
func (m *meeting) observerIn(recordingID string) bool {
	list, err := m.rooms.ListParticipants(context.Background(), &livekit.ListParticipantsRequest{Room: room})
	if err != nil {
		return false
	}
	return slices.ContainsFunc(list.Participants, func(p *livekit.ParticipantInfo) bool {
		return p.Identity == "observer:"+recordingID
	})
}

func (m *meeting) liveRoom() *livekit.Room {
	m.t.Helper()
	list, err := m.rooms.ListRooms(context.Background(), &livekit.ListRoomsRequest{Names: []string{room}})
	if err != nil {
		m.t.Fatal(err)
	}
	if len(list.Rooms) == 0 {
		return nil
	}
	return list.Rooms[0]
}

// speak has p speak long enough for LiveKit to smooth it into an update,
// and be silent again long enough for the span to end.
func (p *person) speak() {
	p.setLevel(loud)
	time.Sleep(1500 * time.Millisecond)
	p.setLevel(silent)
	time.Sleep(1500 * time.Millisecond)
}

// The observer hears a microphone that was there before it joined and one
// published after, and the rejoiner counts as the name they kept.
func TestObserverHearsWhoSpeaks(t *testing.T) {
	m := startMeeting(t)
	ada := m.join("guest:ada-1", "Ada", nil)
	ada.publish()

	observers := m.observers()
	observers.Start("rec", room)
	eventually(t, "the observer to join", func() bool { return m.observerIn("rec") })

	grace := m.join("guest:grace", "Grace", nil)
	grace.publish()
	time.Sleep(time.Second) // for the observer to subscribe
	ada.speak()
	grace.speak()

	ada.leave()
	back := m.join("guest:ada-2", "Ada", nil)
	back.publish()
	time.Sleep(time.Second)
	before := time.Now()
	back.speak()

	activity := observers.Stop("rec")
	if spoke := spokeFor(activity, "Grace"); spoke < 500*time.Millisecond {
		t.Errorf("Grace spoke for %s, by the timeline", spoke)
	}
	timeline := activity.Timeline(before.Add(-time.Hour), 2*time.Hour)
	var names []string
	var adaSpans [][2]float64
	for _, participant := range timeline.Participants {
		names = append(names, participant.Name)
		if participant.Name == "Ada" {
			adaSpans = participant.Spans
		}
	}
	if !reflect.DeepEqual(names, []string{"Ada", "Grace"}) {
		t.Fatalf("names = %v, from %+v", names, timeline)
	}
	if len(adaSpans) != 2 {
		t.Fatalf("Ada's spans across two connections = %v", adaSpans)
	}
	if adaSpans[1][0] < 3600-5 {
		t.Fatalf("Ada's second span %v starts well before she rejoined, at 3600", adaSpans[1])
	}
	eventually(t, "the observer to leave", func() bool { return !m.observerIn("rec") })
}

// Someone who leaves mid-sentence gets no update saying they stopped, since
// updates cover only participants the observer subscribes to: their span
// must end when they leave anyway, not whenever someone next speaks.
func TestSomeoneWhoLeavesMidSentenceStopsSpeaking(t *testing.T) {
	m := startMeeting(t)
	m.join("guest:stays", "Stays", nil)
	ada := m.join("guest:ada", "Ada", nil)
	ada.publish()
	observers := m.observers()
	observers.Start("rec", room)
	eventually(t, "the observer to join", func() bool { return m.observerIn("rec") })
	time.Sleep(time.Second)

	ada.setLevel(loud)
	time.Sleep(1500 * time.Millisecond)
	ada.leave()
	time.Sleep(3 * time.Second)

	activity := observers.Stop("rec")
	if spoke := spokeFor(activity, "Ada"); spoke == 0 || spoke > 2500*time.Millisecond {
		t.Fatalf("Ada spoke for %s of the 1.5 s before she left", spoke)
	}
}

// Rooms are held open only by participants who aren't dependent, and egress
// ends only once a room closes: if the observer held it open, a recording
// nobody stopped would never end.
func TestTheRoomClosesWithTheObserverInIt(t *testing.T) {
	m := startMeeting(t)
	ada := m.join("guest:ada", "Ada", nil)
	ada.publish()
	observers := m.observers()
	observers.Start("rec", room)
	eventually(t, "the observer to join", func() bool { return m.observerIn("rec") })

	eventually(t, "the room to count Ada", func() bool { return m.liveRoom().GetNumParticipants() == 1 })
	time.Sleep(time.Second)
	live := m.liveRoom()
	if live.GetActiveRecording() {
		t.Fatal("with the observer in it the room reports that it's recording")
	}
	if live.GetNumParticipants() != 1 {
		t.Fatalf("the room counts %d participants with the observer in it", live.GetNumParticipants())
	}

	ada.leave()
	eventually(t, "the room to close", func() bool { return m.liveRoom() == nil })

	// Closing the room ended the observer's connection, not what it saw.
	if activity := observers.Stop("rec"); activity == nil {
		t.Fatal("the observer's activity went with the room")
	}
}

// Nobody in the room can see the observer, nor tell it's there.
func TestTheObserverIsHidden(t *testing.T) {
	m := startMeeting(t)
	joined := make(chan string, 8)
	callback := lksdk.NewRoomCallback()
	callback.OnParticipantConnected = func(p *lksdk.RemoteParticipant) { joined <- p.Identity() }
	ada := m.join("guest:ada", "Ada", callback)
	observers := m.observers()
	observers.Start("rec", room)
	eventually(t, "the observer to join", func() bool { return m.observerIn("rec") })
	late := m.join("guest:grace", "Grace", nil)

	time.Sleep(time.Second)
	for _, p := range []*person{ada, late} {
		for _, other := range p.room.GetRemoteParticipants() {
			if strings.HasPrefix(other.Identity(), "observer:") {
				t.Fatalf("a participant sees %s", other.Identity())
			}
		}
	}
	for len(joined) > 0 {
		if identity := <-joined; strings.HasPrefix(identity, "observer:") {
			t.Fatalf("a participant saw %s join", identity)
		}
	}
}

// The grants hold where it counts: on the media server itself.
func TestTheObserverCantPublish(t *testing.T) {
	m := startMeeting(t)
	m.join("guest:ada", "Ada", nil)
	observers := m.observers()
	observers.Start("rec", room)
	eventually(t, "the observer to join", func() bool { return m.observerIn("rec") })
	list, err := m.rooms.ListParticipants(context.Background(), &livekit.ListParticipantsRequest{Room: room})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list.Participants {
		if p.Identity != "observer:rec" {
			continue
		}
		if p.Kind != livekit.ParticipantInfo_AGENT || p.Permission.CanPublish || p.Permission.CanPublishData ||
			!p.Permission.CanSubscribe || !p.Permission.Hidden || p.Permission.Recorder {
			description, _ := json.Marshal(p.Permission)
			t.Fatalf("observer is kind %s with %s", p.Kind, description)
		}
	}
}

// An observer that can't connect costs nothing: nobody waits for it, and
// stopping it gives an activity with nothing in it.
func TestAnObserverThatCantConnect(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + l.Addr().String()
	_ = l.Close()
	observers := speakers.New(url, tidelivekit.NewMinter(config.Config{LiveKitAPIKey: testKey, LiveKitAPISecret: testSecret}))
	began := time.Now()
	observers.Start("rec", room)
	activity := observers.Stop("rec")
	if took := time.Since(began); took > time.Second {
		t.Fatalf("Start and Stop took %s", took)
	}
	if activity == nil || len(activity.Timeline(time.Now(), time.Hour).Participants) != 0 {
		t.Fatalf("activity = %+v", activity)
	}
	if got := observers.Stop("rec"); got != nil {
		t.Fatalf("a second Stop found %+v", got)
	}
	observers.Close()
}

// Stopping tide takes every observer out of its room while the media server
// is up, and leaves none of their goroutines behind.
func TestCloseLeavesNothingRunning(t *testing.T) {
	m := startMeeting(t)
	ada := m.join("guest:ada", "Ada", nil)
	ada.publish()
	observers := speakers.New(m.server.URL(), m.minter)
	observers.Start("rec", room)
	eventually(t, "the observer to join", func() bool { return m.observerIn("rec") })
	eventually(t, "the observer to drain the microphone", func() bool {
		return strings.Contains(goroutines(), "speakers.drain")
	})

	observers.Close()
	// The media server runs in this process, so counting goroutines would
	// count its own: these are the stacks of ours.
	if stacks := goroutines(); strings.Contains(stacks, "internal/speakers/observer.go") {
		t.Fatalf("an observer's goroutine outlived Close:\n%s", stacks)
	}
	eventually(t, "the observer to leave", func() bool { return !m.observerIn("rec") })
	observers.Start("late", room)
	if time.Sleep(200 * time.Millisecond); m.observerIn("late") {
		t.Fatal("an observer started after Close")
	}
	if got := observers.Stop("rec"); got != nil {
		t.Fatalf("Stop after Close found %+v", got)
	}
}

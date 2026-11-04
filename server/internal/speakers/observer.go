// Package speakers records who is active during a recording, so that its
// transcript can name its speakers (ARCHITECTURE.md §8).
//
// Each recording gets one observer: tide itself in the room, hidden, hearing
// the microphones and publishing nothing. All of it is best effort: an
// observer that fails costs the recording's transcript its names, and
// nothing else.
package speakers

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"

	tidelivekit "tide/internal/livekit"
)

const (
	// tokenTTL is how long an observer's token admits it. LiveKit refreshes
	// the token of a connected participant, so it only has to outlast the
	// connecting.
	tokenTTL = time.Hour
	// closeWait bounds how long a stopping tide waits for its observers to
	// leave their rooms.
	closeWait = 5 * time.Second
)

// A Minter makes the observer's token.
type Minter interface {
	MintToken(identity, name, room string, role tidelivekit.Role, ttl time.Duration) (string, error)
}

// Observers are the observers of the recordings running now, by recording
// ID. Every method is safe to call at any time.
type Observers struct {
	url   string
	mint  Minter
	tasks sync.WaitGroup

	mu      sync.Mutex
	running map[string]*observer
	closed  bool
}

// New makes Observers that connect to the media server at url, which is
// tide's own loopback address for it when it's embedded.
func New(url string, mint Minter) *Observers {
	return &Observers{url: url, mint: mint, running: map[string]*observer{}}
}

// Start starts observing a recording of room. It returns at once: the
// observer connects on its own, and logs if it can't.
func (o *Observers) Start(recordingID, room string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, running := o.running[recordingID]; running || o.closed {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	observer := &observer{recordingID: recordingID, cancel: cancel, activity: NewActivity()}
	o.running[recordingID] = observer
	o.tasks.Go(func() {
		defer cancel()
		observer.run(ctx, o.url, room, o.mint)
	})
}

// Stop ends a recording's observer, without waiting for it to leave the
// room, and returns what it saw: nil if it has no observer, as after a
// restart, and an empty Activity if it never connected.
func (o *Observers) Stop(recordingID string) *Activity {
	o.mu.Lock()
	observer := o.running[recordingID]
	delete(o.running, recordingID)
	o.mu.Unlock()
	if observer == nil {
		return nil
	}
	return observer.stop()
}

// Close ends every observer and waits for them to leave their rooms, within
// reason. What they saw is lost: tide is stopping, so nothing could finish
// their recordings now. Start does nothing afterwards.
func (o *Observers) Close() {
	o.mu.Lock()
	o.closed = true
	running := o.running
	o.running = map[string]*observer{}
	o.mu.Unlock()
	for _, observer := range running {
		observer.stop()
	}
	left := make(chan struct{})
	go func() {
		o.tasks.Wait()
		close(left)
	}()
	select {
	case <-left:
	case <-time.After(closeWait):
		log.Printf("speakers: observers still leaving after %s", closeWait)
	}
}

type observer struct {
	recordingID string
	cancel      context.CancelFunc
	// room is set before the observer joins, so every callback sees it.
	room *lksdk.Room
	// work is the audio being drained and the updates being read.
	work sync.WaitGroup

	mu       sync.Mutex
	activity *Activity
	stopped  bool
}

func (o *observer) stop() *Activity {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.stopped {
		o.activity.Update(time.Now(), nil)
		o.stopped = true
	}
	o.cancel()
	return o.activity
}

// run is the observer's life in the room: until ctx ends, whatever becomes
// of the connection. A connection the server ended stays ended: joining again
// could bring back a room that closed.
func (o *observer) run(ctx context.Context, url, room string, mint Minter) {
	token, err := mint.MintToken("observer:"+o.recordingID, "tide", room, tidelivekit.RoleObserver, tokenTTL)
	if err != nil {
		log.Printf("speakers: recording %s: mint a token: %v", o.recordingID, err)
		return
	}
	o.room = lksdk.NewRoom(o.callback())
	if err := o.room.JoinWithContextAndToken(ctx, url, token, lksdk.WithAutoSubscribe(false)); err != nil {
		if ctx.Err() == nil {
			log.Printf("speakers: recording %s: join room %s: %v", o.recordingID, room, err)
		}
		return
	}
	// A publication the callback was too early for, during the join.
	for _, participant := range o.room.GetRemoteParticipants() {
		for _, publication := range participant.TrackPublications() {
			if remote, ok := publication.(*lksdk.RemoteTrackPublication); ok {
				o.subscribe(remote)
			}
		}
	}
	<-ctx.Done()
	o.room.Disconnect()
	o.work.Wait()
}

func (o *observer) callback() *lksdk.RoomCallback {
	callback := lksdk.NewRoomCallback()
	callback.ParticipantCallback.OnTrackPublished = func(publication *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
		o.subscribe(publication)
	}
	callback.ParticipantCallback.OnTrackSubscribed = func(track *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
		o.background(func() { drain(track) })
	}
	// The SDK calls this one holding the room's lock.
	callback.ParticipantCallback.OnTrackUnpublished = func(*lksdk.RemoteTrackPublication, *lksdk.RemoteParticipant) {
		o.background(o.refresh)
	}
	callback.OnActiveSpeakersChanged = func([]lksdk.Participant) { o.refresh() }
	callback.OnParticipantDisconnected = func(*lksdk.RemoteParticipant) { o.refresh() }
	callback.OnDisconnected = func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		if !o.stopped {
			o.activity.Update(time.Now(), nil)
		}
	}
	return callback
}

// subscribe subscribes to a microphone, and to no other track: LiveKit reports
// speakers only among the participants you subscribe to.
func (o *observer) subscribe(publication *lksdk.RemoteTrackPublication) {
	if publication.Source() != livekit.TrackSource_MICROPHONE {
		return
	}
	if err := publication.SetSubscribed(true); err != nil {
		log.Printf("speakers: recording %s: subscribe to %s: %v", o.recordingID, publication.SID(), err)
	}
}

// background runs f, unless the observer has stopped, and has it finish
// before the observer is done.
func (o *observer) background(f func()) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.stopped {
		o.work.Go(f)
	}
}

// drain reads a track's RTP and discards it until the track ends, so that
// nothing backs up.
func drain(track *webrtc.TrackRemote) {
	buffer := make([]byte, 1500)
	for {
		if _, _, err := track.Read(buffer); err != nil {
			return
		}
	}
}

// refresh records who is active now. It asks the room, not the callback's
// argument, and under the lock: the SDK calls each update on a goroutine of
// its own, so they can arrive out of order, and the room always has the
// latest. The room's list isn't quite the truth, though. It keeps someone who
// left or stopped publishing mid-sentence: the update saying they stopped
// never comes, since it covers only subscribed participants.
func (o *observer) refresh() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.stopped {
		return
	}
	var active []Speaker
	for _, participant := range o.room.ActiveSpeakers() {
		if o.room.GetParticipantBySID(participant.SID()) != nil && participant.IsMicrophoneEnabled() {
			active = append(active, Speaker{Identity: participant.Identity(), Name: participant.Name()})
		}
	}
	o.activity.Update(time.Now(), active)
}

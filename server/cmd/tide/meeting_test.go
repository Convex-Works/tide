package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"

	"tide/internal/api"
)

// The one-binary acceptance test (ARCHITECTURE.md §16, Phase 6): tide with
// nothing configured but its ports is an anonymous deployment that holds a
// meeting by itself. Two browsers' worth of clients take the SPA's API path:
// the one that creates the room is its host, the other comes through the
// lobby, and audio flows between them through tide's /rtc and the media
// server inside the process. No other process runs.
func TestOneBinaryHoldsAMeeting(t *testing.T) {
	tcp, udp := freePorts(t, 3)
	base := fmt.Sprintf("http://127.0.0.1:%d", tcp[0])
	k := startMainWith(t,
		fmt.Sprintf("TIDE_ADDR=127.0.0.1:%d", tcp[0]), "TIDE_BASE_URL="+base,
		fmt.Sprintf("TIDE_MEDIA_API_PORT=%d", tcp[1]),
		fmt.Sprintf("TIDE_MEDIA_TCP_PORT=%d", tcp[2]),
		fmt.Sprintf("TIDE_MEDIA_UDP_PORT=%d", udp[0]))
	if !k.logged("tide: anonymous, media server embedded") {
		t.Fatalf("tide didn't log an anonymous mode with its media server embedded; it logged %q", k.history)
	}

	// The owner's browser gets a session from /api/me and creates a room.
	owner := k.anonymousSession()
	response, body := k.call(http.MethodPost, api.RoomsPath, `{"name":"Standup"}`, owner)
	var room api.RoomInfo
	if err := json.Unmarshal([]byte(body), &room); response.StatusCode != http.StatusCreated || err != nil {
		t.Fatalf("create = %d %s", response.StatusCode, body)
	}
	hosting := k.join(room.Slug, "Ada", owner)
	if hosting.Status != "admitted" || hosting.Token == "" {
		t.Fatalf("the owner's join = %+v, want admitted at once", hosting)
	}
	// Browsers signal through tide's own origin.
	if want := "ws://" + strings.TrimPrefix(base, "http://"); hosting.WSURL != want {
		t.Fatalf("ws_url = %q, want tide's origin %q", hosting.WSURL, want)
	}

	// Another browser is a guest there, and waits in the lobby.
	guest := k.anonymousSession()
	waiting := k.join(room.Slug, "Grace", guest)
	if waiting.RequestID == "" || waiting.Token != "" {
		t.Fatalf("the guest's join = %+v, want a lobby request and no token", waiting)
	}
	admitted := k.waitInLobby(waiting.RequestID)
	if response, body := k.call(http.MethodPost, "/api/lobby/"+waiting.RequestID+"/approve", "", owner); response.StatusCode >= 300 {
		t.Fatalf("approve = %d %s", response.StatusCode, body)
	}
	var admission api.LobbyAdmittedSSE
	select {
	case admission = <-admitted:
	case <-time.After(10 * time.Second):
		t.Fatal("the guest was never admitted")
	}

	// The host publishes; the guest hears it.
	ada, err := lksdk.ConnectToRoomWithToken(hosting.WSURL, hosting.Token, lksdk.NewRoomCallback())
	if err != nil {
		t.Fatalf("the host connects through tide: %v", err)
	}
	t.Cleanup(ada.Disconnect)
	var packets atomic.Int64
	var hostSeen atomic.Bool
	disconnected := make(chan struct{})
	listener := lksdk.NewRoomCallback()
	listener.OnTrackSubscribed = func(track *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, from *lksdk.RemoteParticipant) {
		if strings.HasPrefix(from.Identity(), "host:anon:") && strings.Contains(from.Metadata(), `"role":"host"`) {
			hostSeen.Store(true)
		}
		go func() {
			for {
				if _, _, err := track.ReadRTP(); err != nil {
					return
				}
				packets.Add(1)
			}
		}()
	}
	listener.OnDisconnected = func() { close(disconnected) }
	reconnecting := make(chan struct{})
	var lost sync.Once
	listener.OnReconnecting = func() { lost.Do(func() { close(reconnecting) }) }
	grace, err := lksdk.ConnectToRoomWithToken(admission.WSURL, admission.Token, listener)
	if err != nil {
		t.Fatalf("the admitted guest connects through tide: %v", err)
	}
	t.Cleanup(grace.Disconnect)
	speak(t, ada)
	waitFor(t, 20*time.Second, "the guest to hear the host", func() bool { return packets.Load() >= 25 })
	if !hostSeen.Load() {
		t.Fatal("the guest heard someone who isn't the room's anonymous host")
	}

	// tide sees the meeting through its media server's API, and the media
	// server's webhooks reach tide: the room is live and was joined.
	waitFor(t, 10*time.Second, "the dashboard to show the live room", func() bool {
		response, body := k.call(http.MethodGet, api.RoomsPath, "", owner)
		var rooms []api.RoomInfo
		if response.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &rooms) != nil || len(rooms) != 1 {
			return false
		}
		return rooms[0].Active && rooms[0].NumParticipants == 2 && rooms[0].LastActiveAt != nil
	})

	// Stopping tide ends the meeting with it, by stopping the media server
	// before it exits, as its log says; a process that merely died wouldn't.
	// The guest loses the meeting either way the media server's teardown
	// races: told it is shutting down, or finding its transport gone first and
	// retrying a server that is no longer there.
	k.stop()
	k.drainLogs()
	if !k.logged("tide: media server stopped") {
		t.Fatalf("tide exited without stopping its media server; it logged %q", k.history)
	}
	select {
	case <-disconnected:
	case <-reconnecting:
	case <-time.After(15 * time.Second):
		t.Fatal("the guest stayed connected after tide stopped")
	}
}

// A signal while the media server is still starting (STUN discovery can
// take a minute) stops tide as the operator asked: at once, and not as a
// failure.
func TestASignalWhileTheMediaServerStartsStopsTide(t *testing.T) {
	k := launchMain(t, "TIDE_TEST_MEDIA_STARTS_SLOWLY=1")
	k.waitForLog("test: the media server is starting")
	k.signal(syscall.SIGTERM)
	if state := k.wait(10 * time.Second); !state.Success() {
		t.Fatalf("tide stopped while starting with %v; it logged %q", state, k.history)
	}
	k.waitForLog("tide: stopped while starting")
}

// The embedded mode stops in order and lets go of everything: with
// recording on, a SIGTERM stops tide cleanly within its grace, and the
// media ports, the media server's API port and the recorder's Redis
// endpoint are all free again.
func TestEmbeddedTideReleasesItsPortsWhenItStops(t *testing.T) {
	tcp, udp := freePorts(t, 4)
	k := startMainWith(t,
		"TIDE_OIDC_ISSUER=http://127.0.0.1:1/dex", "TIDE_OIDC_CLIENT_SECRET="+strings.Repeat("o", 20),
		"TIDE_SESSION_SECRET="+testSessionSecret, "TIDE_DB_PATH="+filepath.Join(t.TempDir(), "tide.db"),
		"TIDE_S3_ENDPOINT=http://127.0.0.1:1", "TIDE_S3_ACCESS_KEY=test-access",
		"TIDE_S3_SECRET_KEY="+strings.Repeat("k", 20),
		"TIDE_MEDIA_API_KEY=test-key", "TIDE_MEDIA_API_SECRET="+strings.Repeat("m", 40),
		"TIDE_RECORDER_REDIS_PASSWORD="+strings.Repeat("r", 40),
		fmt.Sprintf("TIDE_RECORDER_REDIS_ADDR=127.0.0.1:%d", tcp[0]),
		fmt.Sprintf("TIDE_MEDIA_API_PORT=%d", tcp[1]),
		fmt.Sprintf("TIDE_MEDIA_TCP_PORT=%d", tcp[2]),
		fmt.Sprintf("TIDE_MEDIA_UDP_PORT=%d", udp[0]))
	if !k.logged("media server embedded") || !k.logged("recording on") {
		t.Fatalf("tide didn't start signed in with recording and its media server embedded: %q", k.history)
	}
	// While tide runs, they are taken.
	if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", tcp[0])); err == nil {
		_ = l.Close()
		t.Fatal("the recorder's Redis endpoint isn't listening")
	}
	k.stop()
	for _, port := range tcp[:3] {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("TCP port %d is still taken after tide stopped: %v", port, err)
		}
		_ = l.Close()
	}
	c, err := net.ListenPacket("udp", fmt.Sprintf(":%d", udp[0]))
	if err != nil {
		t.Fatalf("UDP port %d is still taken after tide stopped: %v", udp[0], err)
	}
	_ = c.Close()
}

// drainLogs keeps the lines tide logged that nothing waited for. Call it once
// tide has exited, when every line is buffered.
func (k *tideProcess) drainLogs() {
	for {
		select {
		case line := <-k.logs:
			k.history = append(k.history, line)
		default:
			return
		}
	}
}

// anonymousSession is a new browser's first visit: /api/me hands it a
// session.
func (k *tideProcess) anonymousSession() *http.Cookie {
	k.t.Helper()
	response, body := k.call(http.MethodGet, api.MePath, "", nil)
	var me api.Me
	if err := json.Unmarshal([]byte(body), &me); response.StatusCode != http.StatusOK || err != nil || !me.Anonymous {
		k.t.Fatalf("/api/me = %d %s", response.StatusCode, body)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == "tide_session" {
			return cookie
		}
	}
	k.t.Fatalf("/api/me set no session: %v", response.Header["Set-Cookie"])
	return nil
}

func (k *tideProcess) join(slug, name string, session *http.Cookie) api.JoinResponse {
	k.t.Helper()
	response, body := k.call(http.MethodPost, "/api/rooms/"+slug+"/join", `{"name":"`+name+`"}`, session)
	var joined api.JoinResponse
	if err := json.Unmarshal([]byte(body), &joined); response.StatusCode != http.StatusOK || err != nil {
		k.t.Fatalf("join as %s = %d %s", name, response.StatusCode, body)
	}
	return joined
}

// waitInLobby opens the guest's lobby stream and delivers its admission.
func (k *tideProcess) waitInLobby(id string) <-chan api.LobbyAdmittedSSE {
	k.t.Helper()
	response, err := http.Get("http://" + k.addr + "/api/lobby/" + id + "/wait")
	if err != nil || response.StatusCode != http.StatusOK {
		k.t.Fatalf("lobby wait: %v %v", response, err)
	}
	k.t.Cleanup(func() { _ = response.Body.Close() })
	admitted := make(chan api.LobbyAdmittedSSE, 1)
	go func() {
		scanner := bufio.NewScanner(response.Body)
		event := ""
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: ") && event == "admitted":
				var admission api.LobbyAdmittedSSE
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &admission) == nil {
					admitted <- admission
				}
				return
			}
		}
	}()
	return admitted
}

// speak publishes an Opus track of silence from room until the test ends.
func speak(t *testing.T, room *lksdk.Room) {
	t.Helper()
	track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: "mic"}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = track.WriteSample(pionmedia.Sample{Data: []byte{0xf8, 0xff, 0xfe}, Duration: 20 * time.Millisecond}, nil)
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-done })
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// freePorts finds n free TCP ports on loopback and one free UDP port per n.
func freePorts(t *testing.T, n int) (tcp, udp []int) {
	t.Helper()
	for range n {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		tcp = append(tcp, l.Addr().(*net.TCPAddr).Port)
		c, err := net.ListenPacket("udp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		udp = append(udp, c.LocalAddr().(*net.UDPAddr).Port)
	}
	return tcp, udp
}

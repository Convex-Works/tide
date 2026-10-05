package media

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/webhook"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	pionmedia "github.com/pion/webrtc/v4/pkg/media"
)

const (
	testKey    = "tide-test-key"
	testSecret = "tide-test-secret-0123456789-0123456789"
)

// freePorts reserves n TCP ports and n UDP ports at once, so that none of
// them is handed out twice, and releases them for the test to use.
func freePorts(t *testing.T, n int) (tcp, udp []int) {
	t.Helper()
	var listeners []net.Listener
	var conns []net.PacketConn
	for range n {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, l)
		tcp = append(tcp, l.Addr().(*net.TCPAddr).Port)
		c, err := net.ListenPacket("udp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		udp = append(udp, c.LocalAddr().(*net.UDPAddr).Port)
	}
	for _, l := range listeners {
		_ = l.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
	return tcp, udp
}

// testOptions are options for a loopback media server on free ports.
func testOptions(t *testing.T) Options {
	t.Helper()
	tcp, udp := freePorts(t, 2)
	return Options{
		APIKey: testKey, APISecret: testSecret, Loopback: true,
		InternalPort: tcp[0], TCPPort: tcp[1], UDPPort: udp[0],
	}
}

func startServer(t *testing.T, opts Options) *Server {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := Start(ctx, opts)
	if err != nil {
		t.Fatalf("start the media server: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close the media server: %v", err)
		}
	})
	return s
}

// eventually polls cond until it holds or timeout passes.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestStartServesTheAPI(t *testing.T) {
	opts := testOptions(t)
	s := startServer(t, opts)
	if want := "http://127.0.0.1:" + strconv.Itoa(opts.InternalPort); s.URL() != want {
		t.Fatalf("URL() = %q, want %q", s.URL(), want)
	}
	rooms := lksdk.NewRoomServiceClient(s.URL(), testKey, testSecret)
	ctx := context.Background()
	if _, err := rooms.CreateRoom(ctx, &livekit.CreateRoomRequest{Name: "abc-defg-hij"}); err != nil {
		t.Fatalf("create room: %v", err)
	}
	list, err := rooms.ListRooms(ctx, &livekit.ListRoomsRequest{})
	if err != nil {
		t.Fatalf("list rooms: %v", err)
	}
	if len(list.Rooms) != 1 || list.Rooms[0].Name != "abc-defg-hij" {
		t.Fatalf("rooms = %v, want just abc-defg-hij", list.Rooms)
	}
	// A key the server wasn't given is refused: the API is authenticated.
	stranger := lksdk.NewRoomServiceClient(s.URL(), "stranger", testSecret)
	if _, err := stranger.ListRooms(ctx, &livekit.ListRoomsRequest{}); err == nil {
		t.Fatal("ListRooms with an unknown API key succeeded")
	}
}

func TestMediaFlowsThroughSignalHandler(t *testing.T) {
	t.Run("in memory", func(t *testing.T) {
		mediaFlows(t, testOptions(t))
	})
	t.Run("over Redis", func(t *testing.T) {
		mediaFlows(t, withRedis(testOptions(t), startRedis(t)))
	})
}

// mediaFlows connects two participants to a media server through
// SignalHandler, behind an HTTP server with tide's kind of short deadlines,
// and checks that one hears the other, and that signaling outlives the
// deadlines.
func mediaFlows(t *testing.T, opts Options) {
	s := startServer(t, opts)
	mux := http.NewServeMux()
	mux.Handle(SignalPath, s.SignalHandler())
	mux.Handle(SignalPath+"/", s.SignalHandler())
	front := httptest.NewUnstartedServer(mux)
	const deadline = 2 * time.Second
	front.Config.ReadTimeout = deadline
	front.Config.WriteTimeout = deadline
	front.Start()
	t.Cleanup(front.Close)
	wsURL := "ws://" + strings.TrimPrefix(front.URL, "http://")

	var reconnects atomic.Int32
	var subscribed sync.Map // track name → *atomic.Int64 of RTP packets
	var receivers sync.Map  // track name → *webrtc.RTPReceiver
	listener := lksdk.NewRoomCallback()
	listener.OnReconnecting = func() { reconnects.Add(1) }
	listener.OnTrackSubscribed = func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
		count := new(atomic.Int64)
		subscribed.Store(pub.Name(), count)
		receivers.Store(pub.Name(), pub.Receiver())
		go func() {
			for {
				if _, _, err := track.ReadRTP(); err != nil {
					return
				}
				count.Add(1)
			}
		}()
	}
	bob, err := lksdk.ConnectToRoom(wsURL, lksdk.ConnectInfo{
		APIKey: testKey, APISecret: testSecret, RoomName: "flow", ParticipantIdentity: "bob",
	}, listener)
	if err != nil {
		t.Fatalf("bob connects through the signal handler: %v", err)
	}
	t.Cleanup(bob.Disconnect)

	speaker := lksdk.NewRoomCallback()
	speaker.OnReconnecting = func() { reconnects.Add(1) }
	alice, err := lksdk.ConnectToRoom(wsURL, lksdk.ConnectInfo{
		APIKey: testKey, APISecret: testSecret, RoomName: "flow", ParticipantIdentity: "alice",
	}, speaker)
	if err != nil {
		t.Fatalf("alice connects through the signal handler: %v", err)
	}
	t.Cleanup(alice.Disconnect)

	publish := func(name string) {
		track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := alice.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: name}); err != nil {
			t.Fatalf("publish %s: %v", name, err)
		}
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			// An Opus frame of silence: the SFU forwards it like any other.
			frame := []byte{0xf8, 0xff, 0xfe}
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					_ = track.WriteSample(pionmedia.Sample{Data: frame, Duration: 20 * time.Millisecond}, nil)
				}
			}
		}()
		t.Cleanup(func() { close(stop); <-done })
	}
	packets := func(name string) int64 {
		count, ok := subscribed.Load(name)
		if !ok {
			return 0
		}
		return count.(*atomic.Int64).Load()
	}

	publish("first")
	eventually(t, 20*time.Second, "bob to receive alice's first track", func() bool { return packets("first") >= 25 })
	// Media must take the UDP mux, not ICE/TCP, which would hide a mux that
	// isn't working. (Which of this machine's addresses carries it is up to
	// ICE: on one machine a client also finds the mux's LAN socket.)
	receiver, _ := receivers.Load("first")
	pair, err := receiver.(*webrtc.RTPReceiver).Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil {
		t.Fatalf("bob's selected candidate pair: %v, %v", pair, err)
	}
	if pair.Remote.Protocol != webrtc.ICEProtocolUDP || int(pair.Remote.Port) != opts.UDPPort {
		t.Fatalf("bob receives media over %s %s:%d, want UDP on the mux's port %d",
			pair.Remote.Protocol, pair.Remote.Address, pair.Remote.Port, opts.UDPPort)
	}

	// Past the front server's deadlines, signaling must still carry a new
	// publication without either client having had to reconnect.
	time.Sleep(deadline + time.Second)
	publish("second")
	eventually(t, 20*time.Second, "bob to receive alice's second track", func() bool { return packets("second") >= 25 })
	if n := reconnects.Load(); n != 0 {
		t.Fatalf("clients reconnected %d times: the signal connection didn't outlive the deadlines", n)
	}
	if before := packets("first"); before < 50 {
		t.Fatalf("the first track stopped flowing (%d packets)", before)
	}
}

func TestWebhooksReachTide(t *testing.T) {
	events := make(chan *livekit.WebhookEvent, 16)
	keys := auth.NewSimpleKeyProvider(testKey, testSecret)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		event, err := webhook.ReceiveWebhookEvent(r, keys)
		if err != nil {
			t.Errorf("webhook fails verification: %v", err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		events <- event
	}))
	t.Cleanup(hook.Close)

	opts := testOptions(t)
	opts.WebhookURL = hook.URL + "/api/webhooks/media"
	s := startServer(t, opts)
	room, err := lksdk.ConnectToRoom("ws"+strings.TrimPrefix(s.URL(), "http"), lksdk.ConnectInfo{
		APIKey: testKey, APISecret: testSecret, RoomName: "hooked", ParticipantIdentity: "carol",
	}, lksdk.NewRoomCallback())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(room.Disconnect)

	timeout := time.After(20 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Event == webhook.EventParticipantJoined {
				if got := event.GetParticipant().GetIdentity(); got != "carol" {
					t.Fatalf("participant_joined for %q, want carol", got)
				}
				if got := event.GetRoom().GetName(); got != "hooked" {
					t.Fatalf("participant_joined in room %q, want hooked", got)
				}
				return
			}
		case <-timeout:
			t.Fatal("no signed participant_joined webhook arrived")
		}
	}
}

func TestAPIIsReachableOnlyOverLoopback(t *testing.T) {
	opts := testOptions(t)
	s := startServer(t, opts)

	t.Run("not on other addresses", func(t *testing.T) {
		ip := nonLoopbackIP(t)
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(opts.InternalPort)), 2*time.Second)
		if err == nil {
			_ = conn.Close()
			t.Fatalf("the media server's API answers on %s", ip)
		}
		// The ICE/TCP port, by contrast, is open everywhere.
		conn, err = net.DialTimeout("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(opts.TCPPort)), 2*time.Second)
		if err != nil {
			t.Fatalf("the ICE/TCP port isn't reachable on %s: %v", ip, err)
		}
		_ = conn.Close()
	})

	t.Run("not through the signal handler", func(t *testing.T) {
		front := httptest.NewServer(s.SignalHandler())
		t.Cleanup(front.Close)
		// Judge each answer as given: a redirect followed back here would
		// end in this handler's 404 whatever it forwarded.
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
		for _, path := range []string{
			"/twirp/livekit.RoomService/ListRooms",
			"/rtc/../twirp/livekit.RoomService/ListRooms",
			"/rtcx",
			"/",
		} {
			request, err := http.NewRequest(http.MethodPost, front.URL+path, strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			// Keep the client from cleaning the path itself.
			request.URL.Opaque = path
			request.Header.Set("Content-Type", "application/json")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusNotFound {
				t.Errorf("POST %s through the signal handler = %d, want 404", path, response.StatusCode)
			}
		}
		// Signaling itself goes through: without a token, the media server
		// itself refuses the validation request.
		response, err := client.Get(front.URL + "/rtc/validate")
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET /rtc/validate without a token = %d, want the media server's 401", response.StatusCode)
		}
	})
}

// nonLoopbackIP is an address of this machine's other than loopback, or
// skips the test.
func nonLoopbackIP(t *testing.T) net.IP {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range addrs {
		network, ok := addr.(*net.IPNet)
		if !ok || network.IP.IsLoopback() || network.IP.To4() == nil || network.IP.IsLinkLocalUnicast() {
			continue
		}
		return network.IP
	}
	t.Skip("this machine has no non-loopback IPv4 address")
	return nil
}

// A loopback deployment advertises 127.0.0.1, so its UDP mux must listen
// there, which LiveKit's mux skips unless told.
func TestLoopbackMediaListensOnLoopback(t *testing.T) {
	opts := testOptions(t)
	startServer(t, opts)
	conn, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(opts.UDPPort))
	if err == nil {
		_ = conn.Close()
		t.Fatalf("nothing listens on 127.0.0.1:%d, the address a loopback server advertises", opts.UDPPort)
	}
}

func TestCloseReleasesThePorts(t *testing.T) {
	opts := testOptions(t)
	first := startServer(t, opts)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if second, err := Start(ctx, opts); err == nil {
		_ = second.Close()
		t.Fatal("a second media server started on ports the first one holds")
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	again, err := Start(ctx, opts)
	if err != nil {
		t.Fatalf("start again on the ports Close released: %v", err)
	}
	if err := again.Close(); err != nil {
		t.Fatalf("close again: %v", err)
	}
}

func TestFailedStartReleasesTheMediaPorts(t *testing.T) {
	opts := testOptions(t)
	// Take the API port and let the start past tide's own check of it, so it
	// fails where LiveKit listens, after the media ports opened.
	check := apiPortFree
	apiPortFree = func(int) error { return nil }
	t.Cleanup(func() { apiPortFree = check })
	blocker, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(opts.InternalPort))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if s, err := Start(ctx, opts); err == nil {
		_ = s.Close()
		t.Fatal("the media server started on a taken API port")
	} else if !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("start error %q doesn't say the port is taken", err)
	}
	_ = blocker.Close()
	s, err := Start(ctx, opts)
	if err != nil {
		t.Fatalf("start after a failed start: the media ports stayed taken: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// A start refused for a taken API port starts nothing: once started,
// LiveKit's router publishes a keepalive to Redis every 2 s, and a failed
// LiveKit Start can't stop it.
func TestFailedStartLeavesRedisAlone(t *testing.T) {
	kv := startRedis(t)
	opts := withRedis(testOptions(t), kv)
	blocker, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(opts.InternalPort))
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if s, err := Start(ctx, opts); err == nil {
		_ = s.Close()
		t.Fatal("the media server started on a taken API port")
	} else if !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("start error %q doesn't say the port is taken", err)
	}
	time.Sleep(500 * time.Millisecond)
	before := kv.CommandCount()
	time.Sleep(5 * time.Second)
	if after := kv.CommandCount(); after != before {
		t.Fatalf("%d commands reached Redis in the 5 s after a failed start", after-before)
	}
}

// A start given up on (its context ended) stops the server once it is up,
// and the ports come free.
func TestAbandonedStartReleasesThePorts(t *testing.T) {
	opts := testOptions(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s, err := Start(ctx, opts); err == nil {
		_ = s.Close()
		t.Fatal("a start with its context already ended returned a server")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("start error %v, want context.Canceled", err)
	}
	var again *Server
	eventually(t, 15*time.Second, "the abandoned server's ports to come free", func() bool {
		start, cancelStart := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelStart()
		s, err := Start(start, opts)
		if err != nil {
			return false
		}
		again = s
		return true
	})
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStartRefusesIncompleteOptions(t *testing.T) {
	ctx := context.Background()
	for name, mutate := range map[string]func(*Options){
		"no key":        func(o *Options) { o.APIKey = "" },
		"no secret":     func(o *Options) { o.APISecret = "" },
		"no UDP port":   func(o *Options) { o.UDPPort = 0 },
		"port too high": func(o *Options) { o.TCPPort = 70000 },
		"bad node IP":   func(o *Options) { o.NodeIP = "media.example.com" },
	} {
		t.Run(name, func(t *testing.T) {
			opts := testOptions(t)
			mutate(&opts)
			if s, err := Start(ctx, opts); err == nil {
				_ = s.Close()
				t.Fatal("started")
			} else if errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timed out instead of refusing: %v", err)
			}
		})
	}
}

// With Redis named, the media server keeps its rooms and its node there,
// where the recorder finds them, and releases LiveKit's room lock with its
// script as it creates a room.
func TestMediaServerKeepsItsStateInRedis(t *testing.T) {
	kv := startRedis(t)
	s := startServer(t, withRedis(testOptions(t), kv))

	ctx := context.Background()
	rooms := lksdk.NewRoomServiceClient(s.URL(), testKey, testSecret)
	if _, err := rooms.CreateRoom(ctx, &livekit.CreateRoomRequest{Name: "in-redis"}); err != nil {
		t.Fatalf("create room: %v", err)
	}
	client := redisClient(t, kv)
	if n := client.Exists(ctx, "room_lock:in-redis").Val(); n != 0 {
		t.Fatal("the room lock outlived the room's creation")
	}
	if !client.HExists(ctx, "rooms", "in-redis").Val() {
		keys, _ := client.Keys(ctx, "*").Result()
		t.Fatalf("the room isn't in Redis; keys: %v", keys)
	}
	if n := client.HLen(ctx, "nodes").Val(); n != 1 {
		t.Fatalf("%d nodes registered in Redis, want 1", n)
	}
}

// A media server without Redis keeps nothing in one: it is a single node,
// in memory.
func TestMediaServerWithoutRedisUsesNone(t *testing.T) {
	kv := startRedis(t)
	s := startServer(t, testOptions(t))
	rooms := lksdk.NewRoomServiceClient(s.URL(), testKey, testSecret)
	if _, err := rooms.CreateRoom(context.Background(), &livekit.CreateRoomRequest{Name: "in-memory"}); err != nil {
		t.Fatalf("create room: %v", err)
	}
	if n := kv.CommandCount(); n != 0 {
		t.Fatalf("a media server without Redis sent %d commands to one", n)
	}
}

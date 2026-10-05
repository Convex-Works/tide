// Package media runs the media server inside tide (ARCHITECTURE.md §2.1):
// LiveKit's SFU as a library, started from Options rather than a YAML file.
// With recording on it shares a Redis, which tide doesn't run, with the
// recorder; without, it keeps its state in memory.
//
// tide talks to the embedded server exactly as it talks to an external one:
// through the server SDK at URL(), with the same API key and secret, and
// receives its webhooks over loopback HTTP. Browsers never see URL(): they
// reach signaling through SignalHandler(), which tide mounts at /rtc on its
// own origin.
package media

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/livekit/livekit-server/pkg/routing"
	"github.com/livekit/livekit-server/pkg/service"
	"github.com/livekit/livekit-server/pkg/telemetry/prometheus"
)

// SignalPath is where tide mounts SignalHandler. livekit-client appends it
// (and /rtc/v1, /rtc/validate, /rtc/v1/validate) to the URL a token is
// returned with, so it is fixed by the client, not by tide.
const SignalPath = "/rtc"

// Options configures the embedded media server. config.Config fills it.
type Options struct {
	// APIKey and APISecret sign every token and webhook. With recording off
	// they may be generated per start; the recorder needs them fixed.
	APIKey    string
	APISecret string

	// NodeIP is the address advertised to browsers in ICE candidates, and it
	// replaces the machine's interface addresses there. Empty means
	// 127.0.0.1 when Loopback, else the public address found over STUN,
	// advertised alongside the interface addresses: a recorder in tide's
	// network namespace reaches media through those only when the address is
	// discovered (ARCHITECTURE.md §2.1).
	NodeIP   string
	Loopback bool

	// TCPPort and UDPPort carry media (ICE/TCP fallback and the single-port
	// UDP mux). They are opened on every interface.
	TCPPort int
	UDPPort int

	// InternalPort is the SFU's own HTTP port: its API and signaling, bound to
	// 127.0.0.1 only. tide's SDK calls and SignalHandler both go there.
	InternalPort int

	// WebhookURL is tide's own webhook endpoint over loopback, e.g.
	// http://127.0.0.1:8080/api/webhooks/media.
	WebhookURL string

	// RedisAddr (host:port) and RedisPassword name the Redis the SFU shares
	// with the recorder, which only speaks Redis; once the SFU uses it, its
	// signal relay goes over it too. An empty RedisAddr means single-node,
	// in-memory state, which is right whenever there is no recorder.
	RedisAddr     string
	RedisPassword string

	// Dev gives the SFU info logs and a 5 s departure timeout, as the media
	// gate needs; otherwise it logs warnings and keeps LiveKit's 20 s. The
	// log level is the first server's in a process (see setLogger).
	Dev bool
}

// Server is a running embedded media server.
type Server struct {
	lk     *service.LivekitServer
	url    string
	signal http.Handler
	flight *inFlight
	run    *run

	closeOnce sync.Once
	closeErr  error
}

// run is one call of LiveKit's blocking Start: done closes when it returns,
// and err is what it returned.
type run struct {
	done chan struct{}
	err  error
}

// startPoll is how often Start checks whether the server is up. LiveKit's
// Start marks it running about 100ms after it begins serving.
const startPoll = 10 * time.Millisecond

// Start starts the media server and returns once it accepts API calls, or
// with the error that kept it from starting (a port in use, say). ctx bounds
// only the start.
//
// Discovering the node address over STUN (no NodeIP, not Loopback) happens
// before anything listens and ignores ctx: LiveKit tries its STUN servers
// three times and Start then fails with "could not resolve external IP".
func Start(ctx context.Context, opts Options) (*Server, error) {
	conf, err := livekitConfig(opts)
	if err != nil {
		return nil, err
	}
	if err := apiPortFree(opts.InternalPort); err != nil {
		return nil, fmt.Errorf("media: the API port: %w", err)
	}
	node, err := routing.NewLocalNode(conf)
	if err != nil {
		return nil, fmt.Errorf("media: create node: %w", err)
	}
	// Init registers LiveKit's metrics once per process and ignores later
	// calls, so a second server in one process keeps the first one's node ID
	// in its metric labels. Nothing scrapes them.
	if err := prometheus.Init(string(node.NodeID()), node.NodeType()); err != nil {
		return nil, fmt.Errorf("media: metrics: %w", err)
	}
	lk, err := service.InitializeServer(conf, node)
	if err != nil {
		return nil, fmt.Errorf("media: start: %w", err)
	}
	r := &run{done: make(chan struct{})}
	go func() {
		defer close(r.done)
		r.err = lk.Start()
	}()

	ticker := time.NewTicker(startPoll)
	defer ticker.Stop()
	for {
		select {
		case <-r.done:
			releaseFailed(lk)
			return nil, fmt.Errorf("media: start: %w", r.err)
		case <-ctx.Done():
			go abandon(lk, r)
			return nil, fmt.Errorf("media: start: %w", ctx.Err())
		case <-ticker.C:
			if lk.IsRunning() {
				target := "http://127.0.0.1:" + strconv.Itoa(opts.InternalPort)
				flight := &inFlight{}
				return &Server{lk: lk, url: target, signal: newSignalHandler(target, flight), flight: flight, run: r}, nil
			}
		}
	}
}

// apiPortFree checks, by listening on it for a moment, that the API port is
// free. LiveKit's Start listens on it only after it has started its router
// and IO service, which a failed Start leaves running (see releaseFailed),
// so a taken port is refused here, before anything starts. Tests replace it
// to make a start fail where LiveKit listens.
var apiPortFree = func(port int) error {
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return listener.Close()
}

// releaseFailed cleans up after a LiveKit Start that failed: it closes the
// media sockets InitializeServer opened, which LiveKit closes only when a
// started server stops.
//
// It can't stop the rest. Before it listens, LiveKit's Start registers the
// node and starts its router (with Redis: a keepalive published every 2 s)
// and its IO service (psrpc handlers on Redis); it stops them only in a
// Stop that follows a successful start, and keeps them in private fields.
// apiPortFree leaves only a port taken between that check and LiveKit's own
// listen, and tide exits after a failed start, so they end with the process;
// only a process that goes on, a test, keeps them.
func releaseFailed(lk *service.LivekitServer) {
	lk.RoomManager().Stop()
}

// abandon stops a server whose start was given up on, once it either runs or
// fails.
func abandon(lk *service.LivekitServer, r *run) {
	ticker := time.NewTicker(startPoll)
	defer ticker.Stop()
	for {
		select {
		case <-r.done:
			releaseFailed(lk)
			return
		case <-ticker.C:
			if lk.IsRunning() {
				lk.Stop(true)
				<-r.done
				return
			}
		}
	}
}

// URL is the server's API and signaling URL for server-side SDK clients:
// http://127.0.0.1:<InternalPort>.
func (s *Server) URL() string { return s.url }

// SignalHandler forwards browsers' signaling (SignalPath and everything under
// it, WebSocket upgrades included) to the server: GET requests without a
// body, nothing else. tide's connection deadlines hold until the server
// accepts a WebSocket, when hijacking the connection clears them, since a
// signaling WebSocket lives as long as the meeting.
func (s *Server) SignalHandler() http.Handler { return s.signal }

// Close stops the server at once: every room ends, and every participant is
// told the server is shutting down. Their clients disconnect rather than
// retry, and the meeting page offers Rejoin, which asks tide for a new token
// (ARCHITECTURE.md §2.1). It returns once the media ports are released and
// the signaling connections SignalHandler forwards have ended, or after
// drainTimeout: the server's leave travels through them, and tide exiting
// before they are copied would cut it off, leaving clients to retry a server
// that is gone.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.lk.Stop(true)
		<-s.run.done
		s.closeErr = s.run.err
		if !s.flight.wait(drainTimeout) {
			log.Printf("media: signaling connections still open %s after stopping", drainTimeout)
		}
	})
	return s.closeErr
}

// drainTimeout bounds how long Close waits for forwarded signaling to end
// once the server has stopped. Clients close their side as soon as the leave
// arrives, so this is only reached by one that doesn't.
const drainTimeout = 2 * time.Second

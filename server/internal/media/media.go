// Package media runs the media server inside tide (ARCHITECTURE.md §2.1):
// LiveKit's SFU as a library, started from Options rather than a YAML file,
// and, when recording is on, the Redis-protocol endpoint the recorder and the
// SFU coordinate over.
//
// tide talks to the embedded server exactly as it talks to an external one:
// through the server SDK at URL(), with the same API key and secret, and
// receives its webhooks over loopback HTTP. Browsers never see URL(): they
// reach signaling through SignalHandler(), which tide mounts at /rtc on its
// own origin.
package media

import (
	"context"
	"errors"
	"net/http"
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

	// NodeIP is the address advertised to browsers in ICE candidates. Empty
	// means discover it: 127.0.0.1 when Loopback, else the public address
	// found over STUN.
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

	// Bus is the Redis-protocol endpoint the SFU coordinates over. Nil means
	// single-node, in-memory state, which is right whenever there is no
	// recorder.
	Bus *Bus

	// Dev raises the SFU's log level from warn to info.
	Dev bool
}

// Server is a running embedded media server.
type Server struct{}

// ErrNotImplemented marks the contract stubs; the media track replaces them.
var ErrNotImplemented = errors.New("media: not implemented")

// Start starts the media server and returns once it accepts API calls, or
// with the error that kept it from starting (a port in use, say). ctx bounds
// only the start.
func Start(ctx context.Context, opts Options) (*Server, error) {
	return nil, ErrNotImplemented
}

// URL is the server's API and signaling URL for server-side SDK clients:
// http://127.0.0.1:<InternalPort>.
func (s *Server) URL() string { return "" }

// SignalHandler forwards browsers' signaling (SignalPath and everything under
// it, WebSocket upgrades included) to the server. It clears the connection
// deadlines tide's http.Server sets, since a signaling WebSocket lives as long
// as the meeting.
func (s *Server) SignalHandler() http.Handler { return http.NotFoundHandler() }

// Close stops the server at once: every room ends and its participants are
// disconnected, as a crash would, so their clients reconnect when tide is
// back. It returns once the media ports are released.
func (s *Server) Close() error { return ErrNotImplemented }

// Bus is the Redis-protocol endpoint tide serves for the SFU and the
// recorder, in memory. It requires password on every connection and expires
// keys on the wall clock.
type Bus struct{}

// StartBus listens on addr (host:port; ":6379" means every interface) and
// serves until Close.
func StartBus(addr, password string) (*Bus, error) {
	return nil, ErrNotImplemented
}

// Addr is the address the bus listens on, with the port it was given (or
// chose, for port 0).
func (b *Bus) Addr() string { return "" }

// Password is the password every connection must AUTH with.
func (b *Bus) Password() string { return "" }

// Close stops serving and drops every key.
func (b *Bus) Close() error { return ErrNotImplemented }

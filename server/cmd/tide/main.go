package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	tide "tide"
	"tide/internal/api"
	"tide/internal/config"
	"tide/internal/httpapi"
	"tide/internal/media"
	"tide/internal/store"
	"tide/internal/transcripts"
)

// shutdownGrace bounds how long a stopping server waits for requests in
// flight. Lobby streams end as soon as it stops.
const shutdownGrace = 10 * time.Second

// mediaStartTimeout bounds how long tide waits for its media server to
// accept API calls before giving up on starting.
const mediaStartTimeout = 30 * time.Second

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		// Give the signals back to the runtime, so that a second one stops
		// tide at once, as it would any program.
		stop()
		log.Print("tide: stopping; a second signal stops it at once")
	}()
	if err := run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}

// run serves tide until ctx is done, starting and stopping its parts in the
// order ARCHITECTURE.md §2 gives: the listener, the recorder's Redis
// endpoint and the media server come up before tide serves; once
// httpapi.Serve has stopped the server and the background work, the media
// server stops, then the Redis endpoint, and the database closes last, once
// nothing uses it.
func run(ctx context.Context, cfg config.Config) error {
	if cfg.DevMode {
		log.Print("WARNING: dev mode is on — unauthenticated /api/dev/token is exposed and dev secrets are in use")
	}
	log.Printf("tide: %s", cfg.Summary())
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("close database: %v", err)
		}
	}()
	// Transcripts are off unless the operator turns them on
	// (ARCHITECTURE.md §8.1); only then does tide need the bundle.
	var transcribe *moil.Bundle
	if cfg.Transcripts {
		log.Print("tide: transcripts are on: machines can pair through moil at /moil")
		transcribe, err = transcripts.Bundle()
		if err != nil {
			return fmt.Errorf("load the transcribe bundle: %w", err)
		}
	} else {
		log.Print("tide: transcripts are off (set TIDE_TRANSCRIPTS=true to turn them on)")
	}
	// The listener comes first: the media server's webhooks need its port.
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	running, err := startMedia(ctx, &cfg, listener.Addr())
	if err != nil {
		_ = listener.Close()
		return err
	}
	defer running.close()
	apiHandler, background, err := httpapi.New(cfg, tide.WebFS(), db, transcribe, running.signal())
	if err != nil {
		_ = listener.Close()
		return err
	}
	// Read/Write timeouts bound slow-loris bodies and wedged writers on every
	// route; the lobby SSE handlers clear their own deadlines via
	// http.ResponseController when a stream starts, and hijacking a
	// connection for a machine's WebSocket clears them too, as forwarding a
	// signaling WebSocket to the media server does.
	server := &http.Server{
		Handler:           apiHandler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	log.Printf("tide listening on %s", listener.Addr())
	return httpapi.Serve(ctx, server, listener, background, shutdownGrace)
}

// embeddedMedia is the media server tide runs itself and, with recording
// on, the Redis endpoint it and the recorder coordinate over
// (ARCHITECTURE.md §2.1). With an external media server both are nil, and
// there is nothing to run.
type embeddedMedia struct {
	server *media.Server
	bus    *media.Bus
}

// startMedia starts the embedded media server, and first the recorder's
// Redis endpoint when recording is on, and points cfg's SDK URL at the
// server. addr is tide's own listener, where the server sends its webhooks.
func startMedia(ctx context.Context, cfg *config.Config, addr net.Addr) (*embeddedMedia, error) {
	running := &embeddedMedia{}
	if !cfg.MediaEmbedded {
		return running, nil
	}
	if cfg.Recording {
		bus, err := media.StartBus(cfg.RecorderRedisAddr, cfg.RecorderRedisPassword)
		if err != nil {
			return nil, fmt.Errorf("start the recorder's Redis endpoint on %s: %w", cfg.RecorderRedisAddr, err)
		}
		running.bus = bus
	}
	startCtx, cancel := context.WithTimeout(ctx, mediaStartTimeout)
	defer cancel()
	server, err := media.Start(startCtx, media.Options{
		APIKey:       cfg.LiveKitAPIKey,
		APISecret:    cfg.LiveKitAPISecret,
		NodeIP:       cfg.MediaNodeIP,
		Loopback:     cfg.MediaLoopback(),
		TCPPort:      cfg.MediaTCPPort,
		UDPPort:      cfg.MediaUDPPort,
		InternalPort: cfg.MediaInternalPort,
		WebhookURL:   webhookURL(addr),
		Bus:          running.bus,
		Dev:          cfg.DevMode,
	})
	if err != nil {
		running.close()
		// LiveKit says "could not resolve external IP" when no STUN server
		// answered; the operator's way out is to name the address.
		if cfg.MediaNodeIP == "" && strings.Contains(err.Error(), "external IP") {
			return nil, fmt.Errorf("start the media server: %w (set TIDE_MEDIA_NODE_IP to the address browsers reach this server at)", err)
		}
		return nil, fmt.Errorf("start the media server: %w", err)
	}
	running.server = server
	cfg.LiveKitURL = server.URL()
	return running, nil
}

// signal is the embedded server's signaling for tide to serve at /rtc, or
// nil with an external media server.
func (m *embeddedMedia) signal() http.Handler {
	if m.server == nil {
		return nil
	}
	return m.server.SignalHandler()
}

// close stops the media server, which ends every room, and then the Redis
// endpoint.
func (m *embeddedMedia) close() {
	if m.server != nil {
		if err := m.server.Close(); err != nil {
			log.Printf("tide: stop the media server: %v", err)
		}
	}
	if m.bus != nil {
		if err := m.bus.Close(); err != nil {
			log.Printf("tide: stop the recorder's Redis endpoint: %v", err)
		}
	}
}

// webhookURL is tide's media webhook endpoint as the embedded server reaches
// it: on loopback when tide listens on every interface, or on the one
// address it listens on.
func webhookURL(addr net.Addr) string {
	host, port := "127.0.0.1", "8080"
	if tcp, ok := addr.(*net.TCPAddr); ok {
		port = strconv.Itoa(tcp.Port)
		if tcp.IP != nil && !tcp.IP.IsUnspecified() {
			host = tcp.IP.String()
		}
	}
	return "http://" + net.JoinHostPort(host, port) + api.LiveKitWebhookPath
}

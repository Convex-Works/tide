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
	"github.com/redis/go-redis/v9"

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
// accept API calls before giving up on starting. It is long because
// discovering the node address over STUN comes first and LiveKit bounds it
// on its own: three tries of its STUN servers, each allowed about 10
// seconds, can take a minute and more before Start learns the answer.
const mediaStartTimeout = 3 * time.Minute

// startMediaServer starts the embedded media server; a variable so that
// main's process tests can hold a start open.
var startMediaServer = media.Start

// redisWait bounds how long tide waits at startup for the Redis it shares
// with the recorder to answer (ARCHITECTURE.md §2.1): beside the recorder,
// in a pod or a Compose project, Redis may come up a moment after tide. A
// variable so that main's process tests can wait less.
var redisWait = 30 * time.Second

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
		// A signal that came while tide was still starting stopped the start:
		// that is a stop the operator asked for, not a failure.
		if ctx.Err() != nil {
			log.Printf("tide: stopped while starting: %v", err)
			return
		}
		log.Fatal(err)
	}
}

// run serves tide until ctx is done, starting and stopping its parts in the
// order ARCHITECTURE.md §2 gives: the listener comes up, then, with
// recording on, Redis must answer, then the media server starts, all before
// tide serves; once httpapi.Serve has stopped the server and the background
// work, the media server stops, and the database closes last, once nothing
// uses it.
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

// embeddedMedia is the media server tide runs itself (ARCHITECTURE.md
// §2.1). With an external media server it is nil, and there is nothing to
// run.
type embeddedMedia struct {
	server *media.Server
}

// startMedia starts the embedded media server, once the Redis it shares with
// the recorder answers when recording is on, and points cfg's SDK URL at the
// server. addr is tide's own listener, where the server sends its webhooks.
func startMedia(ctx context.Context, cfg *config.Config, addr net.Addr) (*embeddedMedia, error) {
	running := &embeddedMedia{}
	if !cfg.MediaEmbedded {
		return running, nil
	}
	options := media.Options{
		APIKey:       cfg.LiveKitAPIKey,
		APISecret:    cfg.LiveKitAPISecret,
		NodeIP:       cfg.MediaNodeIP,
		Loopback:     cfg.MediaLoopback(),
		TCPPort:      cfg.MediaTCPPort,
		UDPPort:      cfg.MediaUDPPort,
		InternalPort: cfg.MediaInternalPort,
		WebhookURL:   webhookURL(addr),
		Dev:          cfg.DevMode,
	}
	if cfg.Recording {
		if err := waitForRedis(ctx, cfg.RecorderRedisAddr, cfg.RecorderRedisPassword); err != nil {
			return nil, err
		}
		options.RedisAddr = cfg.RecorderRedisAddr
		options.RedisPassword = cfg.RecorderRedisPassword
	}
	startCtx, cancel := context.WithTimeout(ctx, mediaStartTimeout)
	defer cancel()
	server, err := startMediaServer(startCtx, options)
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

// close stops the media server, which ends every room.
func (m *embeddedMedia) close() {
	if m.server != nil {
		if err := m.server.Close(); err != nil {
			log.Printf("tide: stop the media server: %v", err)
		} else {
			log.Print("tide: media server stopped; every meeting ended")
		}
	}
}

// waitForRedis waits until the Redis at addr answers an authenticated PING,
// trying about every second for at most redisWait, or until ctx ends. A
// password Redis refuses fails at once: waiting won't change it.
func waitForRedis(ctx context.Context, addr, password string) error {
	client := redis.NewClient(&redis.Options{
		Addr: addr, Password: password, MaxRetries: -1,
		DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second,
	})
	defer client.Close()
	waitCtx, cancel := context.WithTimeout(ctx, redisWait)
	defer cancel()
	logged := false
	for {
		err := client.Ping(waitCtx).Err()
		if err == nil {
			return nil
		}
		if refused := strings.ToUpper(err.Error()); strings.Contains(refused, "WRONGPASS") ||
			strings.Contains(refused, "NOAUTH") || strings.Contains(refused, "INVALID PASSWORD") {
			return fmt.Errorf("Redis at %s (TIDE_RECORDER_REDIS_ADDR) refused TIDE_RECORDER_REDIS_PASSWORD: %w", addr, err)
		}
		if !logged {
			log.Printf("tide: waiting up to %s for Redis at %s (TIDE_RECORDER_REDIS_ADDR)", redisWait, addr)
			logged = true
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("Redis at %s (TIDE_RECORDER_REDIS_ADDR) didn't answer within %s: %w", addr, redisWait, err)
		case <-time.After(time.Second):
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

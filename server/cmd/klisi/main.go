package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	klisi "klisi"
	"klisi/internal/config"
	"klisi/internal/httpapi"
	"klisi/internal/store"
	"klisi/internal/transcripts"
)

// shutdownGrace bounds how long a stopping server waits for requests in
// flight. Lobby streams end as soon as it stops.
const shutdownGrace = 10 * time.Second

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
		// klisi at once, as it would any program.
		stop()
		log.Print("klisi: stopping; a second signal stops it at once")
	}()
	if err := run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}

// run serves klisi until ctx is done, then stops it as httpapi.Serve says,
// and closes the database last, once nothing uses it.
func run(ctx context.Context, cfg config.Config) error {
	if cfg.DevMode {
		log.Print("WARNING: dev mode is on — unauthenticated /api/dev/token is exposed and dev secrets are in use")
	}
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
	// (ARCHITECTURE.md §8.1); only then does klisi need the bundle.
	var transcribe *moil.Bundle
	if cfg.Transcripts {
		log.Print("klisi: transcripts are on: machines can pair through moil at /moil")
		transcribe, err = transcripts.Bundle()
		if err != nil {
			return fmt.Errorf("load the transcribe bundle: %w", err)
		}
	} else {
		log.Print("klisi: transcripts are off (set KLISI_TRANSCRIPTS=true to turn them on)")
	}
	apiHandler, background, err := httpapi.New(cfg, klisi.WebFS(), db, transcribe)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		_ = background.Close()
		return err
	}
	// Read/Write timeouts bound slow-loris bodies and wedged writers on every
	// route; the lobby SSE handlers clear their own deadlines via
	// http.ResponseController when a stream starts, and hijacking a
	// connection for a machine's WebSocket clears them too.
	server := &http.Server{
		Handler:           apiHandler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	log.Printf("klisi listening on %s", listener.Addr())
	return httpapi.Serve(ctx, server, listener, background, shutdownGrace)
}

package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	klisi "klisi"
	"klisi/internal/config"
	"klisi/internal/httpapi"
	"klisi/internal/store"
)

// shutdownGrace bounds how long a stopping server waits for requests in
// flight; lobby SSE streams would otherwise hold it open indefinitely.
const shutdownGrace = 10 * time.Second

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	if cfg.DevMode {
		log.Print("WARNING: dev mode is on — unauthenticated /api/dev/token is exposed and dev secrets are in use")
	}
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("close database: %v", err)
		}
	}()
	apiHandler, background, err := httpapi.New(cfg, klisi.WebFS(), db)
	if err != nil {
		log.Fatal(err)
	}
	// Read/Write timeouts bound slow-loris bodies and wedged writers on every
	// route; the lobby SSE handlers clear their own deadlines via
	// http.ResponseController when a stream starts, and hijacking a
	// connection for a machine's WebSocket clears them too.
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           apiHandler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// Heal recording rows whose LiveKit webhooks were lost, and project
	// transcript rows onto moil jobs (startup + 1 min + nudges).
	go background.Run(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("shut down HTTP server: %v", err)
		}
	}()

	log.Printf("klisi listening on %s", cfg.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	// Machines' WebSockets are hijacked, so Shutdown doesn't wait for them:
	// closing moil disconnects them and saves what they last reported.
	if err := background.Close(); err != nil {
		log.Printf("close moil: %v", err)
	}
}

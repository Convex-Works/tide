package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	klisi "klisi"
	"klisi/internal/config"
	"klisi/internal/httpapi"
	"klisi/internal/store"
)

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
	apiHandler, recorder := httpapi.New(cfg, klisi.WebFS(), db)
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           apiHandler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Heal recording rows whose LiveKit webhooks were lost (startup + 1 min).
	reconcilerCtx, stopReconciler := context.WithCancel(context.Background())
	defer stopReconciler()
	go recorder.RunReconciler(reconcilerCtx, time.Minute)

	log.Printf("klisi listening on %s", cfg.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

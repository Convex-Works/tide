package main

import (
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
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.New(cfg, klisi.WebFS(), db),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("klisi listening on %s", cfg.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

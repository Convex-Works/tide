package main

import (
	"errors"
	"log"
	"net/http"
	"time"

	klisi "klisi"
	"klisi/internal/config"
	"klisi/internal/httpapi"
)

func main() {
	cfg := config.Load()
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.New(cfg, klisi.WebFS()),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("klisi listening on %s", cfg.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

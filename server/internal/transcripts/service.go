// Package transcripts makes speaker-labelled transcripts of recordings on
// machines their room's owner paired, through moil (ARCHITECTURE.md §8.1).
//
// The transcripts table is the truth: one row per recording that should have
// a transcript. The reconciler projects every pending row onto a moil job,
// and the job's end back onto the row.
package transcripts

import (
	"context"
	"net/http"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	"klisi/internal/api"
	"klisi/internal/httpx"
	"klisi/internal/store"
)

// ObjectStore is where recordings and their transcript sidecars live. URLs
// are presigned for the endpoint machines and browsers reach.
type ObjectStore interface {
	Remove(ctx context.Context, key string) error
	PresignedGet(ctx context.Context, key string, expiry time.Duration) (string, error)
	PresignedPut(ctx context.Context, key string, expiry time.Duration) (string, error)
}

type Config struct {
	// Moil is the server machines connect to; Bundle must have been added
	// to it.
	Moil    *moil.Server
	Bundle  *moil.Bundle
	Store   *store.Store
	Objects ObjectStore
}

// Service runs transcript jobs and serves the transcript routes.
type Service struct {
	cfg Config
}

func New(cfg Config) *Service {
	return &Service{cfg: cfg}
}

// Run reconciles transcript rows with moil jobs at once, then every
// interval and whenever nudged, until ctx is done.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	<-ctx.Done()
}

// Nudge asks the reconciler to run soon. It never blocks.
func (s *Service) Nudge() {}

// Transcripts implements recording.TranscriptSource.
func (s *Service) Transcripts(ctx context.Context, room store.Room, recordings []store.Recording) (map[string]*api.TranscriptInfo, error) {
	return nil, nil
}

// Request serves POST api.RecordingTranscriptPath: requests a transcript of
// an available recording, or retries a failed one, and returns its
// api.TranscriptInfo.
func (s *Service) Request(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, http.StatusNotImplemented, "Not implemented yet.")
}

// Download serves GET api.RecordingTranscriptDownloadPath?format=txt|vtt: a
// redirect to a short-lived presigned URL of a completed transcript.
func (s *Service) Download(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, http.StatusNotImplemented, "Not implemented yet.")
}

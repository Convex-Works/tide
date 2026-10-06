package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	"tide/internal/api"
	"tide/internal/config"
	"tide/internal/machines"
	"tide/internal/recording"
	"tide/internal/rooms"
	"tide/internal/store"
	"tide/internal/transcripts"
)

// transcriptsFeature is everything TIDE_TRANSCRIPTS turns on
// (ARCHITECTURE.md §8.1): the moil server machines connect to, the
// transcripts service and its reconciler, the machines and pairing routes,
// and the transcript routes. New builds one only when the switch is on; a
// nil *transcriptsFeature is tide without transcripts, and its methods do
// nothing.
//
// With it off, nothing else changes: deleting a recording or a room still
// queues and removes the recording's transcript files (store's
// Recording.ObjectKeys names them), and the recording reconciler, which
// always runs, removes every queued key when it is due, including the
// transcripts-staging/ keys Prepare queued while transcripts were on. The
// machines and transcripts rows stay. Turning it back on is not a pause
// ending (ARCHITECTURE.md §8.1): pending rows' 14 days count from their
// request, so the old ones fail on the first pass, and that pass adds rows
// for the recordings that completed while it was off.
type transcriptsFeature struct {
	moil     *moil.Server
	service  *transcripts.Service
	machines *machines.Handler
	// pairLimiter bounds machines starting a pairing, per client address.
	pairLimiter *rateLimiter
}

// newTranscriptsFeature starts the moil server and builds the transcripts
// service on it. transcribe is the bundle machines run.
func newTranscriptsFeature(cfg config.Config, db *store.Store, objects transcripts.ObjectStore, transcribe *moil.Bundle) (*transcriptsFeature, error) {
	if transcribe == nil {
		return nil, errors.New("transcripts are on, but no transcribe bundle was given")
	}
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	moilServer, err := moil.NewServer(moil.Config{
		Name:            "tide",
		VerificationURL: baseURL + "/machines",
		Store:           db,
		Logger:          slog.Default(),
		// A transcript travels as files, and tide ignores data events: a
		// machine may make a job hold 4 MiB of them at most, and a finished
		// job leaves moil's memory after 5 minutes (ARCHITECTURE.md §8.1).
		MaxDataBytes: 4 << 20,
		KeepFinished: 5 * time.Minute,
	})
	if err != nil {
		return nil, fmt.Errorf("start moil: %w", err)
	}
	moilServer.AddBundle(transcribe)
	// Machines refuse plain http storage unless tide and storage are both
	// on loopback. tide still starts (the media gate runs that way), but
	// every transcript fails at once, naming the setting.
	storageProblem := transcripts.StorageWarning(cfg.BaseURL, cfg.S3PublicEndpoint)
	if storageProblem != "" {
		log.Printf("WARNING: every transcript will fail: %s", storageProblem)
	}
	return &transcriptsFeature{
		moil: moilServer,
		service: transcripts.New(transcripts.Config{
			Moil: moilServer, Bundle: transcribe, Store: db, Objects: objects,
			StorageProblem: storageProblem,
		}),
		machines:    machines.NewHandler(moilServer, transcribe, baseURL+api.MoilBasePath),
		pairLimiter: newRateLimiter(orDefault(cfg.PairRateLimit, config.DefaultPairRateLimit), time.Minute),
	}, nil
}

// enabled reports whether transcripts are on, for /api/me.
func (f *transcriptsFeature) enabled() bool { return f != nil }

// attach has recordings report their transcripts, and nudges the
// reconciler when a recording ends or is deleted, or a room is.
func (f *transcriptsFeature) attach(recordings *recording.Handler, roomsHandler *rooms.Handler) {
	if f == nil {
		return
	}
	recordings.SetTranscripts(f.service)
	recordings.SetRecordingsChangedHook(f.service.Nudge)
	roomsHandler.SetRoomDeletedHook(f.service.Nudge)
}

// routes registers the transcript, machine, pairing and moil routes, with
// their method fallbacks. Without transcripts none exists, so each answers
// 404 like any unknown path.
func (f *transcriptsFeature) routes(mux *http.ServeMux, h *Handler, ips *clientIPResolver) {
	if f == nil {
		return
	}
	codeHostLimiter := newRateLimiter(pairingCodesPerHost, time.Minute)
	codeAddressLimiter := newRateLimiter(pairingCodesPerAddress, time.Minute)
	pairingCodes := func(next http.HandlerFunc) http.Handler {
		return h.requireAuth(withPairingCodeLimit(codeHostLimiter, codeAddressLimiter, ips, next))
	}

	mux.Handle("POST "+api.RecordingTranscriptPath, h.csrf(h.requireAuth(http.HandlerFunc(f.service.Request))))
	mux.Handle("GET "+api.RecordingTranscriptDownloadPath, h.requireAuth(http.HandlerFunc(f.service.Download)))
	mux.Handle("GET "+api.MachinesPath, h.requireAuth(http.HandlerFunc(f.machines.List)))
	mux.Handle("DELETE "+api.MachinePath, h.csrf(h.requireAuth(http.HandlerFunc(f.machines.Remove))))
	// Pairing codes are short enough to guess at, so every route that takes
	// one is rate limited per host and per client address.
	mux.Handle("GET "+api.PairingPath, pairingCodes(f.machines.Pairing))
	mux.Handle("POST "+api.PairingConfirmPath, h.csrf(pairingCodes(f.machines.Confirm)))
	mux.Handle("POST "+api.PairingDenyPath, h.csrf(pairingCodes(f.machines.Deny)))

	// The machines' side of moil. Starting a pairing is the one moil endpoint
	// that creates state without credentials: it takes only JSON, so a web
	// page can't post one without a CORS preflight, and it is rate limited
	// per client address.
	moilHandler := http.StripPrefix(api.MoilBasePath, f.moil.Handler())
	mux.Handle("POST "+api.MoilBasePath+"/v1/pair", requireMoilJSON(withMoilRateLimit(f.pairLimiter, ips, moilHandler)))
	mux.Handle(api.MoilBasePath+"/", moilHandler)

	registerMethodFallback(mux, api.RecordingTranscriptPath, http.MethodPost)
	registerMethodFallback(mux, api.RecordingTranscriptDownloadPath, http.MethodGet)
	registerMethodFallback(mux, api.MachinesPath, http.MethodGet)
	registerMethodFallback(mux, api.MachinePath, http.MethodDelete)
	registerMethodFallback(mux, api.PairingPath, http.MethodGet)
	registerMethodFallback(mux, api.PairingConfirmPath, http.MethodPost)
	registerMethodFallback(mux, api.PairingDenyPath, http.MethodPost)
}

// run runs the transcripts reconciler until ctx is done, and returns once
// it has, transcript jobs' followers included.
func (f *transcriptsFeature) run(ctx context.Context) {
	if f == nil {
		return
	}
	f.service.Run(ctx, time.Minute)
}

// close disconnects every machine, ends every transcript job with
// moil.ErrClosed, and saves what machines last reported. Jobs' rows stay
// pending until the next start resubmits them.
func (f *transcriptsFeature) close() error {
	if f == nil {
		return nil
	}
	return f.moil.Close()
}

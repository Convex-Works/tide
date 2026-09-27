package moil

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/internal/wire"
)

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/info", s.handleInfo)
	mux.HandleFunc("POST /v1/pair", s.handlePair)
	mux.HandleFunc("POST /v1/pair/token", s.handleToken)
	mux.HandleFunc("GET /v1/bundles", s.withMachine(s.handleBundles))
	mux.HandleFunc("GET /v1/bundles/{hash}", s.withMachine(s.handleBundle))
	mux.HandleFunc("GET /v1/connect", s.handleConnect)
	mux.HandleFunc("DELETE /v1/machine", s.withMachine(s.handleUnpair))
	return mux
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, wire.InfoResponse{Protocol: wire.Protocol, Name: s.cfg.Name})
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	var req wire.PairRequest
	if !readJSON(w, r, &req) {
		return
	}
	if n := wire.Chars(req.Name); n < 1 || n > 64 {
		writeError(w, http.StatusBadRequest, wire.ErrInvalidRequest, "name must be 1–64 characters")
		return
	}
	for _, field := range []string{req.OS, req.Arch, req.AppVersion} {
		if wire.Chars(field) > 64 {
			writeError(w, http.StatusBadRequest, wire.ErrInvalidRequest, "os, arch and app_version must be at most 64 characters")
			return
		}
	}
	deviceCode, p, ok := s.pairings.start(req, s.cfg.PairingTTL, s.cfg.MaxPendingPairings)
	if !ok {
		s.log.Warn("moil: too many pending pairings; refusing a new one", "limit", s.cfg.MaxPendingPairings)
		writeError(w, http.StatusServiceUnavailable, wire.ErrTemporarilyUnavailable, "Too many machines are pairing right now. Try again in a few minutes.")
		return
	}
	complete, _ := url.Parse(s.cfg.VerificationURL)
	q := complete.Query()
	q.Set("code", p.Code)
	complete.RawQuery = q.Encode()
	writeJSON(w, http.StatusOK, wire.PairResponse{
		DeviceCode:              deviceCode,
		UserCode:                p.Code,
		VerificationURI:         s.cfg.VerificationURL,
		VerificationURIComplete: complete.String(),
		ExpiresIn:               int64(s.cfg.PairingTTL / time.Second),
		Interval:                ceilSeconds(s.cfg.PairingInterval),
		Service:                 wire.ServiceInfo{Name: s.cfg.Name},
	})
}

// ceilSeconds rounds d up to whole seconds, at least 1, since machines poll
// at whole-second intervals.
func ceilSeconds(d time.Duration) int64 {
	return max(1, int64(math.Ceil(d.Seconds())))
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	var req wire.TokenRequest
	if !readJSON(w, r, &req) {
		return
	}
	machineID, token, errCode := s.pairings.poll(req.DeviceCode, s.cfg.PairingInterval)
	if errCode != "" {
		var msg string
		if errCode == wire.ErrAccessDenied {
			msg = "The user rejected this machine."
		}
		writeError(w, http.StatusBadRequest, errCode, msg)
		return
	}
	writeJSON(w, http.StatusOK, wire.TokenResponse{MachineID: machineID, Token: token, Service: wire.ServiceInfo{Name: s.cfg.Name}})
}

func (s *Server) handleBundles(w http.ResponseWriter, r *http.Request, _ MachineRecord) {
	var infos []wire.BundleInfo
	if err := s.call(func(sc *scheduler) { infos = sc.bundleInfos() }); err != nil {
		writeError(w, http.StatusServiceUnavailable, wire.ErrTemporarilyUnavailable, "The service is shutting down.")
		return
	}
	writeJSON(w, http.StatusOK, wire.BundlesResponse{Bundles: infos})
}

func (s *Server) handleBundle(w http.ResponseWriter, r *http.Request, _ MachineRecord) {
	var b *Bundle
	_ = s.call(func(sc *scheduler) { b = sc.bundleByHash[r.PathValue("hash")] })
	if b == nil {
		writeError(w, http.StatusNotFound, wire.ErrNotFound, "")
		return
	}
	writeJSON(w, http.StatusOK, b.response())
}

func (s *Server) handleUnpair(w http.ResponseWriter, r *http.Request, rec MachineRecord) {
	if err := s.RemoveMachine(r.Context(), rec.ID); err != nil && !errors.Is(err, ErrNotFound) {
		s.log.Error("moil: removing a machine that unpaired itself", "machine", rec.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// withMachine authenticates the machine calling an endpoint.
func (s *Server) withMachine(h func(http.ResponseWriter, *http.Request, MachineRecord)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rec, ok := s.authenticate(w, r); ok {
			h(w, r, rec)
		}
	}
}

// authenticate finds the machine whose token the request bears, or answers
// 401.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (MachineRecord, bool) {
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") || token == "" {
		writeError(w, http.StatusUnauthorized, wire.ErrUnauthorized, "This endpoint needs a machine token.")
		return MachineRecord{}, false
	}
	rec, err := s.lookupToken(r.Context(), token)
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusUnauthorized, wire.ErrUnauthorized, "The machine token is unknown or revoked.")
		return MachineRecord{}, false
	case err != nil:
		s.log.Error("moil: looking up a machine token", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return MachineRecord{}, false
	}
	return rec, true
}

func (s *Server) lookupToken(ctx context.Context, token string) (MachineRecord, error) {
	return s.store.MachineByToken(ctx, hashSecret(token))
}

// maxBodyBytes bounds the JSON bodies the endpoints read.
const maxBodyBytes = 64 << 10

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, wire.ErrInvalidRequest, "The body must be a JSON object.")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, wire.ErrorBody{Error: code, Message: message})
}

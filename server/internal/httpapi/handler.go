package httpapi

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/config"
	klisilivekit "klisi/internal/livekit"
	"klisi/internal/lobby"
	"klisi/internal/moderation"
	"klisi/internal/recording"
	"klisi/internal/rooms"
	"klisi/internal/store"
)

type Handler struct {
	web        fs.FS
	sessions   *auth.Sessions
	oidc       *auth.OIDC
	rooms      *rooms.Handler
	lobby      *lobby.Handler
	moderation *moderation.Handler
	recording  *recording.Handler
	minter     *klisilivekit.Minter
}

// New builds the HTTP handler. The returned recording handler is the seam for
// background work: main runs its reconciler loop (RunReconciler) so recording
// state heals when LiveKit webhooks are lost.
func New(cfg config.Config, web fs.FS, roomStore *store.Store) (http.Handler, *recording.Handler) {
	sessions := auth.NewSessions(cfg.SessionSecret, cfg.BaseURL)
	minter := klisilivekit.NewMinter(cfg)
	registry := lobby.NewRegistry(lobby.DefaultRequestTTL)
	joinLimiter := newIPRateLimiter(10, time.Minute)
	waitLimiter := newIPRateLimiter(20, time.Minute)
	loginLimiter := newIPRateLimiter(10, time.Minute)
	// Kick bans must outlive any cached admission token (see finding #2 in
	// docs/REVIEW-2026-07-19.md), so the denylist TTL is the token TTL.
	denylist := moderation.NewDenylist(lobby.TokenTTL)
	moderationHandler := moderation.NewHandler(roomStore, moderation.NewRoomService(cfg), denylist)
	recordingHandler := recording.New(cfg, roomStore)
	recordingHandler.SetParticipantJoinedHook(moderationHandler.EnforceOnJoin)
	handler := &Handler{
		web:        web,
		sessions:   sessions,
		oidc:       auth.NewOIDC(cfg, sessions),
		rooms:      rooms.NewHandler(roomStore, recording.NewMinIOStore(cfg)),
		lobby:      lobby.NewHandler(roomStore, registry, minter),
		moderation: moderationHandler,
		recording:  recordingHandler,
		minter:     minter,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.health)
	mux.Handle(
		"GET "+api.AuthLoginPath,
		withRateLimit(loginLimiter, http.HandlerFunc(handler.oidc.Login)),
	)
	mux.HandleFunc("GET "+api.AuthCallbackPath, handler.oidc.Callback)
	mux.Handle("POST "+api.AuthLogoutPath, handler.csrf(http.HandlerFunc(handler.oidc.Logout)))
	mux.Handle("GET "+api.MePath, handler.requireAuth(http.HandlerFunc(handler.oidc.Me)))

	mux.Handle("POST "+api.RoomsPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.rooms.Create))))
	mux.Handle("GET "+api.RoomsPath, handler.requireAuth(http.HandlerFunc(handler.rooms.List)))
	mux.HandleFunc("GET "+api.RoomPath, handler.rooms.Public)
	mux.Handle("PATCH "+api.RoomPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.rooms.Update))))
	mux.Handle("DELETE "+api.RoomPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.rooms.Delete))))
	mux.Handle("POST "+api.KickPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.moderation.Kick))))
	mux.Handle("POST "+api.MutePath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.moderation.Mute))))
	mux.Handle("POST "+api.RecordingStartPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.recording.Start))))
	mux.Handle("POST "+api.RecordingStopPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.recording.Stop))))
	mux.Handle("GET "+api.RoomRecordingsPath, handler.requireAuth(http.HandlerFunc(handler.recording.List)))
	mux.Handle("DELETE "+api.RecordingPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.recording.Delete))))
	mux.Handle("GET "+api.RecordingDownloadPath, handler.requireAuth(http.HandlerFunc(handler.recording.Download)))

	mux.Handle(
		"POST "+api.RoomJoinPath,
		withRateLimit(joinLimiter, handler.csrf(http.HandlerFunc(handler.lobby.Join))),
	)
	mux.Handle("GET "+api.RoomLobbyPath, handler.requireAuth(http.HandlerFunc(handler.lobby.Host)))
	mux.Handle(
		"GET "+api.LobbyWaitPath,
		withRateLimit(waitLimiter, http.HandlerFunc(handler.lobby.Wait)),
	)
	mux.Handle("POST "+api.LobbyApprovePath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.lobby.Approve))))
	mux.Handle("POST "+api.LobbyDenyPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.lobby.Deny))))

	mux.HandleFunc("POST "+api.LiveKitWebhookPath, handler.recording.Webhook)
	if cfg.DevMode {
		mux.HandleFunc("GET "+api.DevTokenPath, handler.devToken)
	}
	registerMethodFallback(mux, "/healthz", http.MethodGet)
	registerMethodFallback(mux, api.AuthLoginPath, http.MethodGet)
	registerMethodFallback(mux, api.AuthCallbackPath, http.MethodGet)
	registerMethodFallback(mux, api.AuthLogoutPath, http.MethodPost)
	registerMethodFallback(mux, api.MePath, http.MethodGet)
	registerMethodFallback(mux, api.RoomsPath, http.MethodGet+", "+http.MethodPost)
	registerMethodFallback(mux, api.RoomPath, http.MethodGet+", "+http.MethodPatch+", "+http.MethodDelete)
	registerMethodFallback(mux, api.RoomJoinPath, http.MethodPost)
	registerMethodFallback(mux, api.KickPath, http.MethodPost)
	registerMethodFallback(mux, api.MutePath, http.MethodPost)
	registerMethodFallback(mux, api.RecordingStartPath, http.MethodPost)
	registerMethodFallback(mux, api.RecordingStopPath, http.MethodPost)
	registerMethodFallback(mux, api.RoomRecordingsPath, http.MethodGet)
	registerMethodFallback(mux, api.RecordingPath, http.MethodDelete)
	registerMethodFallback(mux, api.RecordingDownloadPath, http.MethodGet)
	registerMethodFallback(mux, api.RoomLobbyPath, http.MethodGet)
	registerMethodFallback(mux, api.LobbyWaitPath, http.MethodGet)
	registerMethodFallback(mux, api.LobbyApprovePath, http.MethodPost)
	registerMethodFallback(mux, api.LobbyDenyPath, http.MethodPost)
	registerMethodFallback(mux, api.LiveKitWebhookPath, http.MethodPost)
	if cfg.DevMode {
		registerMethodFallback(mux, api.DevTokenPath, http.MethodGet)
	}
	mux.HandleFunc("/", handler.spa)
	return securityHeaders(handler.withSession(mux)), recordingHandler
}

func registerMethodFallback(mux *http.ServeMux, pattern, allow string) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allow)
		writeJSON(w, http.StatusMethodNotAllowed, api.ErrorResponse{Error: "Method not allowed."})
	})
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (h *Handler) devToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	room := strings.TrimSpace(r.URL.Query().Get("room"))
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if room == "" || name == "" {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: "Room and name are required."})
		return
	}
	token, err := h.minter.MintToken("dev:"+name, name, room, false, 10*time.Minute)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{Error: "Could not create a meeting token. Try again."})
		return
	}
	writeJSON(w, http.StatusOK, api.TokenResponse{Token: token, WSURL: h.minter.PublicURL()})
}

func (h *Handler) withSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if session, err := h.sessions.Read(r); err == nil {
			r = r.WithContext(auth.WithSession(r.Context(), session))
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.SessionFromContext(r.Context()); !ok {
			writeJSON(w, http.StatusUnauthorized, api.ErrorResponse{Error: "Authentication required."})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// State-changing browser routes require this lightweight CSRF signal. The
// HttpOnly SameSite=Lax session cookie supplies the remaining current defense.
func (h *Handler) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Klisi-Csrf") != "1" {
			writeJSON(w, http.StatusForbidden, api.ErrorResponse{Error: "Missing CSRF header."})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) spa(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") || h.web == nil {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusNotFound, api.ErrorResponse{Error: "API route not found."})
			return
		}
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	requested := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if requested == "." || requested == "" {
		requested = "index.html"
	}
	if info, err := fs.Stat(h.web, requested); err != nil || info.IsDir() {
		requested = "index.html"
	}
	h.serveFile(w, r, requested)
}

func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	contents, err := fs.ReadFile(h.web, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(contents))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

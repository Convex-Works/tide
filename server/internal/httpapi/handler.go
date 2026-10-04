package httpapi

import (
	"bytes"
	"context"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/config"
	"klisi/internal/httpx"
	klisilivekit "klisi/internal/livekit"
	"klisi/internal/lobby"
	"klisi/internal/moderation"
	"klisi/internal/recording"
	"klisi/internal/rooms"
	"klisi/internal/store"
)

type Handler struct {
	web         fs.FS
	sessions    *auth.Sessions
	oidc        *auth.OIDC
	rooms       *rooms.Handler
	lobby       *lobby.Handler
	moderation  *moderation.Handler
	recording   *recording.Handler
	transcripts *transcriptsFeature // nil when KLISI_TRANSCRIPTS is off
	minter      *klisilivekit.Minter
}

// Background is the work main runs beside the HTTP server: the reconciler
// that heals recording state when LiveKit webhooks are lost and removes the
// files of deleted recordings, and, with transcripts on, the reconciler that
// projects transcript rows onto moil jobs and the moil server machines
// connect to. Serve runs it and stops it in order.
type Background struct {
	recording   *recording.Handler
	transcripts *transcriptsFeature // nil when KLISI_TRANSCRIPTS is off
	// lobby's streams end when the HTTP server shuts down.
	lobby *lobby.Handler
}

// Run runs the reconcilers until ctx is done, and returns once they have
// returned, transcript jobs' followers included.
func (b *Background) Run(ctx context.Context) {
	var reconciler sync.WaitGroup
	reconciler.Add(1)
	go func() {
		defer reconciler.Done()
		b.recording.RunReconciler(ctx, time.Minute)
	}()
	b.transcripts.run(ctx)
	reconciler.Wait()
}

// Close disconnects every machine, ends every transcript job with
// moil.ErrClosed, and saves what machines last reported. Jobs' rows stay
// pending until the next start resubmits them. Without transcripts there is
// nothing to close.
func (b *Background) Close() error {
	return b.transcripts.close()
}

// New builds the HTTP handler and the Background work main must run beside it.
// transcribe is the bundle machines run to transcribe recordings: main hands
// in transcripts.Bundle(), as it hands in the embedded SPA. It is used only
// when cfg.Transcripts is on, and may be nil when it is off.
func New(cfg config.Config, web fs.FS, roomStore *store.Store, transcribe *moil.Bundle) (http.Handler, *Background, error) {
	sessions := auth.NewSessions(cfg.SessionSecret, cfg.BaseURL, roomStore)
	minter := klisilivekit.NewMinter(cfg)
	registry := lobby.NewRegistry(lobby.DefaultRequestTTL)
	ips := newClientIPResolver(cfg.TrustedProxies)
	joinRate := orDefault(cfg.JoinRateLimit, config.DefaultJoinRateLimit)
	joinLimiter := newRateLimiter(joinRate, time.Minute)
	// Room lookups get their own bucket of the join limit's
	// size: a guest's meeting page looks the room up once and joins once,
	// so the two never compete, and a deployment that raises the join limit
	// (the media gate does) raises this one with it. It keeps a client from
	// testing slugs faster than it could join with them (§15).
	lookupLimiter := newRateLimiter(joinRate, time.Minute)
	waitLimiter := newRateLimiter(orDefault(cfg.WaitRateLimit, config.DefaultWaitRateLimit), time.Minute)
	loginLimiter := newRateLimiter(
		orDefault(cfg.LoginRateLimit, config.DefaultLoginRateLimit),
		time.Minute,
	)
	// Kick bans must outlive any cached admission token (see finding #2 in
	// docs/REVIEW-2026-07-19.md), so the denylist TTL is the token TTL.
	denylist := moderation.NewDenylist(lobby.TokenTTL)
	moderationHandler := moderation.NewHandler(roomStore, moderation.NewRoomService(cfg), denylist)
	// One storage client for every handler, which keeps its connections.
	objects := recording.NewMinIOStore(cfg)
	recordingHandler := recording.New(cfg, roomStore, objects)

	// Transcripts run on machines hosts pair through moil (ARCHITECTURE.md
	// §8.1), only when the operator turns them on.
	var transcriptsOn *transcriptsFeature
	if cfg.Transcripts {
		feature, err := newTranscriptsFeature(cfg, roomStore, objects, transcribe)
		if err != nil {
			return nil, nil, err
		}
		transcriptsOn = feature
	}

	// A participant joining both enforces bans (moderation) and marks the room
	// active so the dashboard can show "idle · Nd ago" once it empties.
	recordingHandler.SetParticipantJoinedHook(func(ctx context.Context, roomName, identity string) {
		moderationHandler.EnforceOnJoin(ctx, roomName, identity)
		if err := roomStore.TouchRoomActive(ctx, roomName, time.Now().Unix()); err != nil {
			log.Printf("rooms: touch active for %q: %v", roomName, err)
		}
	})
	roomsHandler := rooms.NewHandler(roomStore, objects, rooms.NewLiveKitSource(cfg), registry)
	transcriptsOn.attach(recordingHandler, roomsHandler)
	handler := &Handler{
		web:         web,
		sessions:    sessions,
		oidc:        auth.NewOIDC(cfg, sessions),
		rooms:       roomsHandler,
		lobby:       lobby.NewHandler(roomStore, registry, minter),
		moderation:  moderationHandler,
		recording:   recordingHandler,
		transcripts: transcriptsOn,
		minter:      minter,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.health)
	mux.Handle(
		"GET "+api.AuthLoginPath,
		withRateLimit(loginLimiter, ips, http.HandlerFunc(handler.oidc.Login)),
	)
	mux.HandleFunc("GET "+api.AuthCallbackPath, handler.oidc.Callback)
	mux.Handle("POST "+api.AuthLogoutPath, handler.csrf(http.HandlerFunc(handler.oidc.Logout)))
	mux.Handle("GET "+api.MePath, handler.requireAuth(http.HandlerFunc(handler.me)))

	mux.Handle("POST "+api.RoomsPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.rooms.Create))))
	mux.Handle("GET "+api.RoomsPath, handler.requireAuth(http.HandlerFunc(handler.rooms.List)))
	mux.Handle("GET "+api.RoomPath, withLookupRateLimit(lookupLimiter, ips, http.HandlerFunc(handler.rooms.Public)))
	mux.Handle("PATCH "+api.RoomPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.rooms.Update))))
	mux.Handle("DELETE "+api.RoomPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.rooms.Delete))))
	mux.Handle("POST "+api.KickPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.moderation.Kick))))
	mux.Handle("POST "+api.MutePath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.moderation.Mute))))
	mux.Handle("POST "+api.MeetingEndPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.moderation.EndMeeting))))
	mux.Handle("POST "+api.RecordingStartPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.recording.Start))))
	mux.Handle("POST "+api.RecordingStopPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.recording.Stop))))
	mux.Handle("GET "+api.RoomRecordingsPath, handler.requireAuth(http.HandlerFunc(handler.recording.List)))
	mux.Handle("DELETE "+api.RecordingPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.recording.Delete))))
	mux.Handle("GET "+api.RecordingDownloadPath, handler.requireAuth(http.HandlerFunc(handler.recording.Download)))

	mux.Handle(
		"POST "+api.RoomJoinPath,
		withRateLimit(joinLimiter, ips, handler.csrf(http.HandlerFunc(handler.lobby.Join))),
	)
	mux.Handle("GET "+api.RoomLobbyPath, handler.requireAuth(http.HandlerFunc(handler.lobby.Host)))
	mux.Handle(
		"GET "+api.LobbyWaitPath,
		withRateLimit(waitLimiter, ips, http.HandlerFunc(handler.lobby.Wait)),
	)
	mux.Handle("POST "+api.LobbyApprovePath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.lobby.Approve))))
	mux.Handle("POST "+api.LobbyDenyPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.lobby.Deny))))

	// The transcript, machine, pairing and moil routes, with their method
	// fallbacks, exist only with transcripts on.
	transcriptsOn.routes(mux, handler, ips)

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
	registerMethodFallback(mux, api.MeetingEndPath, http.MethodPost)
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
	background := &Background{recording: recordingHandler, transcripts: transcriptsOn, lobby: handler.lobby}
	return securityHeaders(handler.withSession(mux)), background, nil
}

func registerMethodFallback(mux *http.ServeMux, pattern, allow string) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allow)
		httpx.WriteError(w, http.StatusMethodNotAllowed, "Method not allowed.")
	})
}

// me answers who is signed in, and what this deployment offers them.
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, api.Me{
		Sub: session.Sub, Email: session.Email, Name: session.Name,
		Transcripts: h.transcripts.enabled(),
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
		httpx.WriteError(w, http.StatusBadRequest, "Room and name are required.")
		return
	}
	token, err := h.minter.MintToken("dev:"+name, name, room, false, 10*time.Minute)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not create a meeting token. Try again.")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, api.TokenResponse{Token: token, WSURL: h.minter.PublicURL()})
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
			httpx.WriteError(w, http.StatusUnauthorized, "Authentication required.")
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
			httpx.WriteError(w, http.StatusForbidden, "Missing CSRF header.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) spa(w http.ResponseWriter, r *http.Request) {
	// The moil paths are never the SPA: without transcripts, which serve
	// them, a moil app finds nothing there rather than a web page.
	if r.URL.Path == api.MoilBasePath || strings.HasPrefix(r.URL.Path, api.MoilBasePath+"/") {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") || h.web == nil {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			httpx.WriteError(w, http.StatusNotFound, "API route not found.")
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

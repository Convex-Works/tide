package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	"tide/internal/api"
	"tide/internal/auth"
	"tide/internal/config"
	"tide/internal/httpx"
	tidelivekit "tide/internal/livekit"
	"tide/internal/lobby"
	"tide/internal/media"
	"tide/internal/moderation"
	"tide/internal/recording"
	"tide/internal/rooms"
	"tide/internal/store"
)

type Handler struct {
	web         fs.FS
	sessions    *auth.Sessions
	oidc        *auth.OIDC // nil in anonymous mode
	rooms       *rooms.Handler
	lobby       *lobby.Handler
	moderation  *moderation.Handler
	recording   *recording.Handler
	transcripts *transcriptsFeature // nil when TIDE_TRANSCRIPTS is off
	minter      *tidelivekit.Minter
	// anonymous and recordingOn are the deployment's modes (ARCHITECTURE.md
	// §2.1), for /api/me.
	anonymous   bool
	recordingOn bool
}

// Background is the work main runs beside the HTTP server: with recording
// on, the reconciler that heals recording state when LiveKit webhooks are
// lost and removes the files of deleted recordings; in anonymous mode, the
// sweep that deletes rooms nobody uses; and, with transcripts on, the
// reconciler that projects transcript rows onto moil jobs and the moil
// server machines connect to. Serve runs it and stops it in order.
type Background struct {
	recording   *recording.Handler  // nil when recording is off
	sweeper     *rooms.Sweeper      // nil unless tide is anonymous
	transcripts *transcriptsFeature // nil when TIDE_TRANSCRIPTS is off
	// lobby's streams end when the HTTP server shuts down.
	lobby *lobby.Handler
}

// Run runs the reconcilers and the sweep until ctx is done, and returns
// once they have returned, transcript jobs' followers included.
func (b *Background) Run(ctx context.Context) {
	var reconcilers sync.WaitGroup
	if b.recording != nil {
		reconcilers.Go(func() { b.recording.RunReconciler(ctx, time.Minute) })
	}
	if b.sweeper != nil {
		reconcilers.Go(func() { b.sweeper.Run(ctx, rooms.SweepInterval) })
	}
	b.transcripts.run(ctx)
	reconcilers.Wait()
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
// when cfg.Transcripts is on, and may be nil when it is off. signal is the
// embedded media server's signaling (media.Server.SignalHandler), mounted at
// media.SignalPath; it is nil with an external media server, whose
// signaling browsers reach directly.
func New(cfg config.Config, web fs.FS, roomStore *store.Store, transcribe *moil.Bundle, signal http.Handler) (http.Handler, *Background, error) {
	if cfg.Transcripts && !cfg.Recording {
		return nil, nil, errors.New("transcripts are on but recording is off: transcripts need recording")
	}
	if cfg.Recording && cfg.Anonymous {
		return nil, nil, errors.New("recording is on in anonymous mode: recording needs sign-in")
	}
	sessions := auth.NewSessions(cfg.SessionSecret, cfg.BaseURL, roomStore)
	minter := tidelivekit.NewMinter(cfg)
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
	// Recording needs storage (ARCHITECTURE.md §8): one storage client for
	// every handler, which keeps its connections, and none without it.
	var objects *recording.MinIOStore
	var roomObjects interface {
		Remove(ctx context.Context, key string) error
	}
	if cfg.Recording {
		objects = recording.NewMinIOStore(cfg)
		roomObjects = objects
	}
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
	liveRooms := rooms.NewLiveKitSource(cfg)
	roomsHandler := rooms.NewHandler(roomStore, roomObjects, liveRooms, registry)
	transcriptsOn.attach(recordingHandler, roomsHandler)
	handler := &Handler{
		web:         web,
		sessions:    sessions,
		rooms:       roomsHandler,
		lobby:       lobby.NewHandler(roomStore, registry, minter),
		moderation:  moderationHandler,
		recording:   recordingHandler,
		transcripts: transcriptsOn,
		minter:      minter,
		anonymous:   cfg.Anonymous,
		recordingOn: cfg.Recording,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.health)
	if cfg.Anonymous {
		// No identity provider (ARCHITECTURE.md §4.1): signing in issues an
		// anonymous session, as /api/me does, and there is no callback.
		mux.Handle(
			"GET "+api.AuthLoginPath,
			withRateLimit(loginLimiter, ips, http.HandlerFunc(sessions.AnonymousLogin)),
		)
		mux.Handle("POST "+api.AuthLogoutPath, handler.csrf(http.HandlerFunc(sessions.Logout)))
		mux.Handle("GET "+api.MePath, handler.anonymousMe(loginLimiter, ips))
	} else {
		handler.oidc = auth.NewOIDC(cfg, sessions)
		mux.Handle(
			"GET "+api.AuthLoginPath,
			withRateLimit(loginLimiter, ips, http.HandlerFunc(handler.oidc.Login)),
		)
		mux.HandleFunc("GET "+api.AuthCallbackPath, handler.oidc.Callback)
		mux.Handle("POST "+api.AuthLogoutPath, handler.csrf(http.HandlerFunc(handler.oidc.Logout)))
		mux.Handle("GET "+api.MePath, handler.requireAuth(http.HandlerFunc(handler.me)))
	}

	createRoom := handler.requireAuth(http.HandlerFunc(handler.rooms.Create))
	if cfg.Anonymous {
		// Creating a room takes no account, so it is limited per client
		// address, in a bucket of its own sized by the join limit (§15).
		createRoom = withRateLimit(newRateLimiter(joinRate, time.Minute), ips, createRoom)
	}
	mux.Handle("POST "+api.RoomsPath, handler.csrf(createRoom))
	mux.Handle("GET "+api.RoomsPath, handler.requireAuth(http.HandlerFunc(handler.rooms.List)))
	mux.Handle("GET "+api.RoomPath, withLookupRateLimit(lookupLimiter, ips, !cfg.Anonymous, http.HandlerFunc(handler.rooms.Public)))
	mux.Handle("PATCH "+api.RoomPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.rooms.Update))))
	mux.Handle("DELETE "+api.RoomPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.rooms.Delete))))
	mux.Handle("POST "+api.KickPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.moderation.Kick))))
	mux.Handle("POST "+api.MutePath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.moderation.Mute))))
	mux.Handle("POST "+api.MeetingEndPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.moderation.EndMeeting))))
	// The recording routes exist only with recording on (ARCHITECTURE.md
	// §8): without it each answers 404 like any unknown path.
	if cfg.Recording {
		mux.Handle("POST "+api.RecordingStartPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.recording.Start))))
		mux.Handle("POST "+api.RecordingStopPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.recording.Stop))))
		mux.Handle("GET "+api.RoomRecordingsPath, handler.requireAuth(http.HandlerFunc(handler.recording.List)))
		mux.Handle("DELETE "+api.RecordingPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.recording.Delete))))
		mux.Handle("GET "+api.RecordingDownloadPath, handler.requireAuth(http.HandlerFunc(handler.recording.Download)))
	}

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

	// The media webhook stays without recording: it also enforces bans and
	// marks rooms active.
	mux.HandleFunc("POST "+api.LiveKitWebhookPath, handler.recording.Webhook)
	// Browsers reach the embedded media server's signaling on tide's own
	// origin (ARCHITECTURE.md §2.1). The media server checks the token on
	// every connection, so the route has no session, CSRF or rate limit of
	// its own; every method passes, WebSocket upgrades included.
	if signal != nil {
		mux.Handle(media.SignalPath, signal)
		mux.Handle(media.SignalPath+"/", signal)
	}
	if cfg.DevMode {
		mux.HandleFunc("GET "+api.DevTokenPath, handler.devToken)
	}
	registerMethodFallback(mux, "/healthz", http.MethodGet)
	registerMethodFallback(mux, api.AuthLoginPath, http.MethodGet)
	if !cfg.Anonymous {
		registerMethodFallback(mux, api.AuthCallbackPath, http.MethodGet)
	}
	registerMethodFallback(mux, api.AuthLogoutPath, http.MethodPost)
	registerMethodFallback(mux, api.MePath, http.MethodGet)
	registerMethodFallback(mux, api.RoomsPath, http.MethodGet+", "+http.MethodPost)
	registerMethodFallback(mux, api.RoomPath, http.MethodGet+", "+http.MethodPatch+", "+http.MethodDelete)
	registerMethodFallback(mux, api.RoomJoinPath, http.MethodPost)
	registerMethodFallback(mux, api.KickPath, http.MethodPost)
	registerMethodFallback(mux, api.MutePath, http.MethodPost)
	registerMethodFallback(mux, api.MeetingEndPath, http.MethodPost)
	if cfg.Recording {
		registerMethodFallback(mux, api.RecordingStartPath, http.MethodPost)
		registerMethodFallback(mux, api.RecordingStopPath, http.MethodPost)
		registerMethodFallback(mux, api.RoomRecordingsPath, http.MethodGet)
		registerMethodFallback(mux, api.RecordingPath, http.MethodDelete)
		registerMethodFallback(mux, api.RecordingDownloadPath, http.MethodGet)
	}
	registerMethodFallback(mux, api.RoomLobbyPath, http.MethodGet)
	registerMethodFallback(mux, api.LobbyWaitPath, http.MethodGet)
	registerMethodFallback(mux, api.LobbyApprovePath, http.MethodPost)
	registerMethodFallback(mux, api.LobbyDenyPath, http.MethodPost)
	registerMethodFallback(mux, api.LiveKitWebhookPath, http.MethodPost)
	if cfg.DevMode {
		registerMethodFallback(mux, api.DevTokenPath, http.MethodGet)
	}
	mux.HandleFunc("/", handler.spa)
	background := &Background{transcripts: transcriptsOn, lobby: handler.lobby}
	if cfg.Recording {
		background.recording = recordingHandler
	}
	if cfg.Anonymous {
		background.sweeper = rooms.NewSweeper(roomStore, liveRooms, rooms.AnonymousRoomIdle)
	}
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
	h.writeMe(w, session)
}

func (h *Handler) writeMe(w http.ResponseWriter, session auth.Session) {
	httpx.WriteJSON(w, http.StatusOK, api.Me{
		Sub: session.Sub, Email: session.Email, Name: session.Name,
		Transcripts: h.transcripts.enabled(),
		Recording:   h.recordingOn,
		Anonymous:   h.anonymous,
	})
}

// anonymousMe is /api/me without sign-in (ARCHITECTURE.md §4.1): a browser
// without a valid session gets an anonymous one rather than 401, so the SPA
// never shows a sign-in screen. Issuing one counts against the login limit,
// per client address, and is refused with 429 beyond it.
func (h *Handler) anonymousMe(limiter *rateLimiter, ips *clientIPResolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The answer may carry a new session: no cache may keep it.
		w.Header().Set("Cache-Control", "no-store")
		if session, ok := auth.SessionFromContext(r.Context()); ok {
			h.writeMe(w, session)
			return
		}
		if !limiter.allow(ips.key(r)) {
			httpx.WriteError(w, http.StatusTooManyRequests, "Too many requests. Try again later.")
			return
		}
		session, err := h.sessions.IssueAnonymous(w)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Could not start a session. Try again.")
			return
		}
		h.writeMe(w, session)
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
		if r.Header.Get("X-Tide-Csrf") != "1" {
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

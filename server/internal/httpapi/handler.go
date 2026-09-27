package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/config"
	"klisi/internal/httpx"
	klisilivekit "klisi/internal/livekit"
	"klisi/internal/lobby"
	"klisi/internal/machines"
	"klisi/internal/moderation"
	"klisi/internal/recording"
	"klisi/internal/rooms"
	"klisi/internal/store"
	"klisi/internal/transcripts"
)

type Handler struct {
	web        fs.FS
	sessions   *auth.Sessions
	oidc       *auth.OIDC
	rooms      *rooms.Handler
	lobby      *lobby.Handler
	moderation *moderation.Handler
	recording  *recording.Handler
	machines   *machines.Handler
	minter     *klisilivekit.Minter
}

// Background is the work main runs beside the HTTP server: the reconcilers
// that heal recording state when LiveKit webhooks are lost and project
// transcript rows onto moil jobs, and the moil server machines connect to.
type Background struct {
	recording   *recording.Handler
	transcripts *transcripts.Service
	moil        *moil.Server
}

// Run runs the reconcilers until ctx is done.
func (b *Background) Run(ctx context.Context) {
	go b.recording.RunReconciler(ctx, time.Minute)
	b.transcripts.Run(ctx, time.Minute)
}

// Close disconnects every machine. Transcript jobs in flight end without
// touching their rows, which stay pending until the next start resubmits them.
func (b *Background) Close() error {
	return b.moil.Close()
}

// New builds the HTTP handler and the Background work main must run beside it.
func New(cfg config.Config, web fs.FS, roomStore *store.Store) (http.Handler, *Background, error) {
	sessions := auth.NewSessions(cfg.SessionSecret, cfg.BaseURL, roomStore)
	minter := klisilivekit.NewMinter(cfg)
	registry := lobby.NewRegistry(lobby.DefaultRequestTTL)
	ips := newClientIPResolver(cfg.TrustedProxies)
	joinLimiter := newIPRateLimiter(orDefault(cfg.JoinRateLimit, config.DefaultJoinRateLimit), time.Minute)
	waitLimiter := newIPRateLimiter(orDefault(cfg.WaitRateLimit, config.DefaultWaitRateLimit), time.Minute)
	loginLimiter := newIPRateLimiter(
		orDefault(cfg.LoginRateLimit, config.DefaultLoginRateLimit),
		time.Minute,
	)
	// Kick bans must outlive any cached admission token (see finding #2 in
	// docs/REVIEW-2026-07-19.md), so the denylist TTL is the token TTL.
	denylist := moderation.NewDenylist(lobby.TokenTTL)
	moderationHandler := moderation.NewHandler(roomStore, moderation.NewRoomService(cfg), denylist)
	recordingHandler := recording.New(cfg, roomStore)

	// Transcripts run on machines hosts pair through moil (ARCHITECTURE.md §8.1).
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	transcribe, err := transcripts.Bundle()
	if err != nil {
		return nil, nil, fmt.Errorf("load the transcribe bundle: %w", err)
	}
	moilServer, err := moil.NewServer(moil.Config{
		Name:            "klisi",
		VerificationURL: baseURL + "/machines",
		Store:           roomStore,
		Logger:          slog.Default(),
		// A transcript travels as files, and klisi ignores data events: a
		// machine may make a job hold 4 MiB of them at most, and a finished
		// job leaves moil's memory after 5 minutes (ARCHITECTURE.md §8.1).
		MaxDataBytes: 4 << 20,
		KeepFinished: 5 * time.Minute,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("start moil: %w", err)
	}
	moilServer.AddBundle(transcribe)
	// Machines refuse plain http storage unless klisi and storage are both
	// on loopback. klisi still starts (the media gate runs that way), but
	// every transcript fails at once, naming the setting.
	storageProblem := transcripts.StorageWarning(cfg.BaseURL, cfg.S3PublicEndpoint)
	if storageProblem != "" {
		log.Printf("WARNING: every transcript will fail: %s", storageProblem)
	}
	transcriptService := transcripts.New(transcripts.Config{
		Moil: moilServer, Bundle: transcribe, Store: roomStore, Objects: recording.NewMinIOStore(cfg),
		StorageProblem: storageProblem,
	})
	recordingHandler.SetTranscripts(transcriptService)
	recordingHandler.SetRecordingsChangedHook(transcriptService.Nudge)
	pairLimiter := newIPRateLimiter(orDefault(cfg.PairRateLimit, config.DefaultPairRateLimit), time.Minute)

	// A participant joining both enforces bans (moderation) and marks the room
	// active so the dashboard can show "idle · Nd ago" once it empties.
	recordingHandler.SetParticipantJoinedHook(func(ctx context.Context, roomName, identity string) {
		moderationHandler.EnforceOnJoin(ctx, roomName, identity)
		if err := roomStore.TouchRoomActive(ctx, roomName, time.Now().Unix()); err != nil {
			log.Printf("rooms: touch active for %q: %v", roomName, err)
		}
	})
	roomsHandler := rooms.NewHandler(roomStore, recording.NewMinIOStore(cfg), rooms.NewLiveKitSource(cfg), registry)
	roomsHandler.SetRoomDeletedHook(transcriptService.Nudge)
	handler := &Handler{
		web:        web,
		sessions:   sessions,
		oidc:       auth.NewOIDC(cfg, sessions),
		rooms:      roomsHandler,
		lobby:      lobby.NewHandler(roomStore, registry, minter),
		moderation: moderationHandler,
		recording:  recordingHandler,
		machines:   machines.NewHandler(moilServer, transcribe, baseURL+api.MoilBasePath),
		minter:     minter,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.health)
	mux.Handle(
		"GET "+api.AuthLoginPath,
		withRateLimit(loginLimiter, ips, http.HandlerFunc(handler.oidc.Login)),
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

	mux.Handle("POST "+api.RecordingTranscriptPath, handler.csrf(handler.requireAuth(http.HandlerFunc(transcriptService.Request))))
	mux.Handle("GET "+api.RecordingTranscriptDownloadPath, handler.requireAuth(http.HandlerFunc(transcriptService.Download)))
	mux.Handle("GET "+api.MachinesPath, handler.requireAuth(http.HandlerFunc(handler.machines.List)))
	mux.Handle("DELETE "+api.MachinePath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.machines.Remove))))
	mux.Handle("GET "+api.PairingPath, handler.requireAuth(http.HandlerFunc(handler.machines.Pairing)))
	mux.Handle("POST "+api.PairingConfirmPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.machines.Confirm))))
	mux.Handle("POST "+api.PairingDenyPath, handler.csrf(handler.requireAuth(http.HandlerFunc(handler.machines.Deny))))

	// The machines' side of moil. Starting a pairing is the one moil endpoint
	// that takes no credentials, so it is rate limited per IP.
	moilHandler := http.StripPrefix(api.MoilBasePath, moilServer.Handler())
	mux.Handle("POST "+api.MoilBasePath+"/v1/pair", withMoilRateLimit(pairLimiter, ips, moilHandler))
	mux.Handle(api.MoilBasePath+"/", moilHandler)

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
	registerMethodFallback(mux, api.RecordingTranscriptPath, http.MethodPost)
	registerMethodFallback(mux, api.RecordingTranscriptDownloadPath, http.MethodGet)
	registerMethodFallback(mux, api.MachinesPath, http.MethodGet)
	registerMethodFallback(mux, api.MachinePath, http.MethodDelete)
	registerMethodFallback(mux, api.PairingPath, http.MethodGet)
	registerMethodFallback(mux, api.PairingConfirmPath, http.MethodPost)
	registerMethodFallback(mux, api.PairingDenyPath, http.MethodPost)
	registerMethodFallback(mux, api.RoomLobbyPath, http.MethodGet)
	registerMethodFallback(mux, api.LobbyWaitPath, http.MethodGet)
	registerMethodFallback(mux, api.LobbyApprovePath, http.MethodPost)
	registerMethodFallback(mux, api.LobbyDenyPath, http.MethodPost)
	registerMethodFallback(mux, api.LiveKitWebhookPath, http.MethodPost)
	if cfg.DevMode {
		registerMethodFallback(mux, api.DevTokenPath, http.MethodGet)
	}
	mux.HandleFunc("/", handler.spa)
	background := &Background{recording: recordingHandler, transcripts: transcriptService, moil: moilServer}
	return securityHeaders(handler.withSession(mux)), background, nil
}

func registerMethodFallback(mux *http.ServeMux, pattern, allow string) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allow)
		httpx.WriteError(w, http.StatusMethodNotAllowed, "Method not allowed.")
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

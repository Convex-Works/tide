package httpapi

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/livekit/protocol/auth"

	"klisi/internal/api"
	"klisi/internal/config"
)

type Handler struct {
	config config.Config
	web    fs.FS
}

func New(cfg config.Config, web fs.FS) http.Handler {
	handler := &Handler{config: cfg, web: web}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.health)
	if cfg.DevMode {
		mux.HandleFunc("GET "+api.DevTokenPath, handler.devToken)
	}
	mux.HandleFunc("/", handler.spa)
	return mux
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

	canPublish := true
	canSubscribe := true
	token := auth.NewAccessToken(h.config.LiveKitAPIKey, h.config.LiveKitAPISecret)
	token.SetIdentity("dev:" + name)
	token.SetName(name)
	token.SetValidFor(10 * time.Minute)
	token.SetVideoGrant(&auth.VideoGrant{
		RoomJoin:     true,
		Room:         room,
		CanPublish:   &canPublish,
		CanSubscribe: &canSubscribe,
	})

	signed, err := token.ToJWT()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.ErrorResponse{Error: "Could not create a meeting token. Try again."})
		return
	}

	writeJSON(w, http.StatusOK, api.TokenResponse{Token: signed, WSURL: h.config.LiveKitURL})
}

func (h *Handler) spa(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") || h.web == nil {
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

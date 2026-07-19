package lobby

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"klisi/internal/api"
	"klisi/internal/auth"
	klisilivekit "klisi/internal/livekit"
	"klisi/internal/store"
)

const (
	// TokenTTL bounds every admission token. Moderation's kick denylist must
	// keep entries at least this long so a ban outlives any cached token.
	TokenTTL           = 10 * time.Minute
	maxJSONRequestBody = 1 << 20
)

type Handler struct {
	store    *store.Store
	registry *Registry
	minter   *klisilivekit.Minter
}

func NewHandler(roomStore *store.Store, registry *Registry, minter *klisilivekit.Minter) *Handler {
	return &Handler{store: roomStore, registry: registry, minter: minter}
}

func (h *Handler) Join(w http.ResponseWriter, r *http.Request) {
	room, err := h.store.RoomBySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		writeLobbyError(w, http.StatusNotFound, "Room not found.")
		return
	}
	if err != nil {
		writeLobbyError(w, http.StatusInternalServerError, "Could not load the room. Try again.")
		return
	}
	var request api.JoinRequest
	if err := decodeLobbyRequest(w, r, &request); err != nil {
		writeLobbyError(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	name := strings.TrimSpace(request.Name)
	if name == "" || len(name) > 100 {
		writeLobbyError(w, http.StatusBadRequest, "Display name must be between 1 and 100 characters.")
		return
	}

	if session, ok := auth.SessionFromContext(r.Context()); ok && session.Sub == room.OwnerSub {
		hostName := strings.TrimSpace(session.Name)
		if hostName == "" {
			hostName = name
		}
		h.writeAdmission(w, "host:"+session.Sub, hostName, room.Slug, true)
		return
	}
	if !room.LobbyEnabled {
		identity, err := guestIdentity()
		if err != nil {
			writeLobbyError(w, http.StatusInternalServerError, "Could not join the room. Try again.")
			return
		}
		h.writeAdmission(w, identity, name, room.Slug, false)
		return
	}
	pending, err := h.registry.Add(room.Slug, name)
	if err != nil {
		writeLobbyError(w, http.StatusInternalServerError, "Could not enter the lobby. Try again.")
		return
	}
	writeLobbyJSON(w, http.StatusOK, api.JoinResponse{Status: string(StatusWaiting), RequestID: pending.ID})
}

func (h *Handler) Wait(w http.ResponseWriter, r *http.Request) {
	request, ok := h.registry.Get(r.PathValue("id"))
	if !ok {
		writeLobbyError(w, http.StatusNotFound, "Lobby request not found.")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeLobbyError(w, http.StatusInternalServerError, "Streaming is not supported.")
		return
	}
	beginStream(w)

	if request.Status != StatusWaiting {
		h.writeTerminal(w, flusher, request)
		return
	}
	writeSSE(w, "waiting", api.LobbyWaitingSSE{Status: string(StatusWaiting)})
	flusher.Flush()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-request.Decision:
			resolved, exists := h.registry.Get(request.ID)
			if !exists {
				resolved = Request{Status: StatusDenied}
			}
			h.writeTerminal(w, flusher, resolved)
			return
		case <-heartbeat.C:
			writeSSE(w, "waiting", api.LobbyWaitingSSE{Status: string(StatusWaiting)})
			flusher.Flush()
		}
	}
}

func (h *Handler) Host(w http.ResponseWriter, r *http.Request) {
	room, ok := h.requireOwner(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeLobbyError(w, http.StatusInternalServerError, "Streaming is not supported.")
		return
	}
	beginStream(w)
	updates, unsubscribe := h.registry.Subscribe(room.Slug)
	defer unsubscribe()
	h.writePending(w, flusher, room.Slug)
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-updates:
			h.writePending(w, flusher, room.Slug)
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func (h *Handler) Approve(w http.ResponseWriter, r *http.Request) {
	request, ok := h.authorizeRequest(w, r)
	if !ok {
		return
	}
	identity, err := guestIdentity()
	if err != nil {
		writeLobbyError(w, http.StatusInternalServerError, "Could not admit the guest. Try again.")
		return
	}
	token, err := h.minter.MintToken(identity, request.Name, request.RoomSlug, false, TokenTTL)
	if err != nil {
		writeLobbyError(w, http.StatusInternalServerError, "Could not admit the guest. Try again.")
		return
	}
	if err := h.registry.Approve(request.ID, token, h.minter.PublicURL()); err != nil {
		h.writeResolveError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Deny(w http.ResponseWriter, r *http.Request) {
	request, ok := h.authorizeRequest(w, r)
	if !ok {
		return
	}
	if err := h.registry.Deny(request.ID); err != nil {
		h.writeResolveError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) authorizeRequest(w http.ResponseWriter, r *http.Request) (Request, bool) {
	request, ok := h.registry.Get(r.PathValue("id"))
	if !ok {
		writeLobbyError(w, http.StatusNotFound, "Lobby request not found.")
		return Request{}, false
	}
	if _, ok := h.requireOwner(w, r, request.RoomSlug); !ok {
		return Request{}, false
	}
	return request, true
}

func (h *Handler) requireOwner(w http.ResponseWriter, r *http.Request, slug string) (store.Room, bool) {
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		writeLobbyError(w, http.StatusUnauthorized, "Authentication required.")
		return store.Room{}, false
	}
	room, err := h.store.RoomBySlug(r.Context(), slug)
	if errors.Is(err, sql.ErrNoRows) {
		writeLobbyError(w, http.StatusNotFound, "Room not found.")
		return store.Room{}, false
	}
	if err != nil {
		writeLobbyError(w, http.StatusInternalServerError, "Could not load the room. Try again.")
		return store.Room{}, false
	}
	if room.OwnerSub != session.Sub {
		writeLobbyError(w, http.StatusForbidden, "Only the room owner can manage this lobby.")
		return store.Room{}, false
	}
	return room, true
}

func (h *Handler) writeAdmission(w http.ResponseWriter, identity, name, room string, host bool) {
	token, err := h.minter.MintToken(identity, name, room, host, TokenTTL)
	if err != nil {
		writeLobbyError(w, http.StatusInternalServerError, "Could not join the room. Try again.")
		return
	}
	writeLobbyJSON(w, http.StatusOK, api.JoinResponse{
		Status: string(StatusAdmitted), Token: token, WSURL: h.minter.PublicURL(),
	})
}

func (h *Handler) writeTerminal(w http.ResponseWriter, flusher http.Flusher, request Request) {
	switch request.Status {
	case StatusAdmitted:
		writeSSE(w, "admitted", api.LobbyAdmittedSSE{Token: request.Token, WSURL: request.WSURL})
	case StatusExpired:
		writeSSE(w, "expired", api.LobbyDeniedSSE{})
	default:
		writeSSE(w, "denied", api.LobbyDeniedSSE{})
	}
	flusher.Flush()
}

func (h *Handler) writePending(w http.ResponseWriter, flusher http.Flusher, slug string) {
	pending := h.registry.Pending(slug)
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].Created == pending[j].Created {
			return pending[i].ID < pending[j].ID
		}
		return pending[i].Created < pending[j].Created
	})
	items := make([]api.LobbyRequestInfo, 0, len(pending))
	for _, request := range pending {
		items = append(items, api.LobbyRequestInfo{
			ID: request.ID, Name: request.Name, RequestedAt: request.Created,
		})
	}
	writeSSE(w, "pending", api.LobbyPendingSSE{Requests: items})
	flusher.Flush()
}

func (h *Handler) writeResolveError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrRequestNotFound) {
		writeLobbyError(w, http.StatusNotFound, "Lobby request not found.")
		return
	}
	writeLobbyError(w, http.StatusConflict, "Lobby request has already been resolved.")
}

// beginStream sets SSE headers and clears the server's global read/write
// deadlines for this connection — SSE streams outlive the 30-second timeouts
// that protect every ordinary route.
func beginStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	control := http.NewResponseController(w)
	_ = control.SetReadDeadline(time.Time{})
	_ = control.SetWriteDeadline(time.Time{})
}

func guestIdentity() (string, error) {
	id, err := randomHex(4)
	if err != nil {
		return "", err
	}
	return "guest:" + id, nil
}

func writeSSE(w io.Writer, event string, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
}

func decodeLobbyRequest(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func writeLobbyError(w http.ResponseWriter, status int, message string) {
	writeLobbyJSON(w, status, api.ErrorResponse{Error: message})
}

func writeLobbyJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

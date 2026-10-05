package lobby

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"tide/internal/api"
	"tide/internal/auth"
	"tide/internal/httpx"
	tidelivekit "tide/internal/livekit"
	"tide/internal/store"
)

const (
	// TokenTTL bounds every admission token. Moderation's kick denylist must
	// keep entries at least this long so a ban outlives any cached token.
	TokenTTL = 10 * time.Minute
)

const (
	// maxStreamsPerKey bounds concurrent SSE streams per host session+room
	// and per guest request, so one client cannot hold file descriptors and
	// heartbeat goroutines open without limit.
	maxStreamsPerKey = 4
	// sseWriteTimeout is the per-write deadline on stream writes; a client
	// that stops reading gets its stream torn down at the next heartbeat.
	sseWriteTimeout = 10 * time.Second
)

type Handler struct {
	store    *store.Store
	registry *Registry
	minter   *tidelivekit.Minter
	streams  *streamCaps

	// ending is closed by EndStreams.
	ending    chan struct{}
	endStream sync.Once
}

func NewHandler(roomStore *store.Store, registry *Registry, minter *tidelivekit.Minter) *Handler {
	return &Handler{
		store: roomStore, registry: registry, minter: minter,
		streams: newStreamCaps(maxStreamsPerKey),
		ending:  make(chan struct{}),
	}
}

// EndStreams ends every lobby stream, open or opening, for a server that is
// shutting down: they would otherwise hold it until its grace runs out.
// Browsers reconnect a stream that ends, and find the next server.
func (h *Handler) EndStreams() {
	h.endStream.Do(func() { close(h.ending) })
}

// streamCaps counts live SSE streams per key.
type streamCaps struct {
	mu     sync.Mutex
	counts map[string]int
	limit  int
}

func newStreamCaps(limit int) *streamCaps {
	return &streamCaps{counts: map[string]int{}, limit: limit}
}

func (c *streamCaps) acquire(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts[key] >= c.limit {
		return false
	}
	c.counts[key]++
	return true
}

func (c *streamCaps) release(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts[key] <= 1 {
		delete(c.counts, key)
	} else {
		c.counts[key]--
	}
}

func (h *Handler) Join(w http.ResponseWriter, r *http.Request) {
	room, err := h.store.RoomBySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "Room not found.")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not load the room. Try again.")
		return
	}
	var request api.JoinRequest
	if err := httpx.DecodeJSON(w, r, &request); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	name := strings.TrimSpace(request.Name)
	if name == "" || len(name) > 100 {
		httpx.WriteError(w, http.StatusBadRequest, "Display name must be between 1 and 100 characters.")
		return
	}

	if session, ok := auth.SessionFromContext(r.Context()); ok && httpx.CanManageRoom(session, room) {
		hostName := strings.TrimSpace(session.Name)
		if hostName == "" {
			hostName = name
		}
		// LiveKit allows one participant per identity, so a stable host
		// identity would make every new tab disconnect the previous one. The
		// nonce keeps each manager connection distinct.
		nonce, err := randomHex(4)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Could not join the room. Try again.")
			return
		}
		h.writeAdmission(w, r, "host:"+session.Sub+":"+nonce, hostName, room, true)
		return
	}
	if !room.LobbyEnabled {
		identity, err := guestIdentity()
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Could not join the room. Try again.")
			return
		}
		h.writeAdmission(w, r, identity, name, room, false)
		return
	}
	pending, err := h.registry.Add(room.Slug, name)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not enter the lobby. Try again.")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, api.JoinResponse{Status: string(StatusWaiting), RequestID: pending.ID})
}

func (h *Handler) Wait(w http.ResponseWriter, r *http.Request) {
	request, ok := h.registry.Get(r.PathValue("id"))
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "Lobby request not found.")
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		httpx.WriteError(w, http.StatusInternalServerError, "Streaming is not supported.")
		return
	}
	streamKey := "wait\x00" + request.ID
	if !h.streams.acquire(streamKey) {
		httpx.WriteError(w, http.StatusTooManyRequests, "Too many open streams for this request.")
		return
	}
	defer h.streams.release(streamKey)
	stream := beginStream(w)

	if request.Status != StatusWaiting {
		h.writeTerminal(stream, request)
		return
	}
	if err := stream.send("waiting", api.LobbyWaitingSSE{Status: string(StatusWaiting)}); err != nil {
		return
	}
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.ending:
			return
		case <-request.Decision:
			resolved, exists := h.registry.Get(request.ID)
			if !exists {
				resolved = Request{Status: StatusDenied}
			}
			h.writeTerminal(stream, resolved)
			return
		case <-heartbeat.C:
			if err := stream.send("waiting", api.LobbyWaitingSSE{Status: string(StatusWaiting)}); err != nil {
				return
			}
		}
	}
}

func (h *Handler) Host(w http.ResponseWriter, r *http.Request) {
	room, ok := h.requireManager(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		httpx.WriteError(w, http.StatusInternalServerError, "Streaming is not supported.")
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	streamKey := "host\x00" + room.Slug + "\x00" + session.Sub
	if !h.streams.acquire(streamKey) {
		httpx.WriteError(w, http.StatusTooManyRequests, "Too many open lobby streams.")
		return
	}
	defer h.streams.release(streamKey)
	stream := beginStream(w)
	updates, unsubscribe := h.registry.Subscribe(room.Slug)
	defer unsubscribe()
	if err := h.writePending(stream, room.Slug); err != nil {
		return
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.ending:
			return
		case <-updates:
			if err := h.writePending(stream, room.Slug); err != nil {
				return
			}
		case <-heartbeat.C:
			if err := stream.comment("heartbeat"); err != nil {
				return
			}
		}
	}
}

func (h *Handler) Approve(w http.ResponseWriter, r *http.Request) {
	request, room, ok := h.authorizeRequest(w, r)
	if !ok {
		return
	}
	identity, err := guestIdentity()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not admit the guest. Try again.")
		return
	}
	if !h.markActive(w, r, room) {
		return
	}
	token, err := h.minter.MintToken(identity, request.Name, room.Slug, false, TokenTTL)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not admit the guest. Try again.")
		return
	}
	if err := h.registry.Approve(request.ID, token, h.minter.PublicURL()); err != nil {
		h.writeResolveError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Deny(w http.ResponseWriter, r *http.Request) {
	request, _, ok := h.authorizeRequest(w, r)
	if !ok {
		return
	}
	if err := h.registry.Deny(request.ID); err != nil {
		h.writeResolveError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) authorizeRequest(w http.ResponseWriter, r *http.Request) (Request, store.Room, bool) {
	request, ok := h.registry.Get(r.PathValue("id"))
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "Lobby request not found.")
		return Request{}, store.Room{}, false
	}
	room, ok := h.requireManager(w, r, request.RoomSlug)
	if !ok {
		return Request{}, store.Room{}, false
	}
	return request, room, true
}

func (h *Handler) requireManager(w http.ResponseWriter, r *http.Request, slug string) (store.Room, bool) {
	room, _, ok := httpx.RequireRoomManager(
		w, r, h.store, slug, "Only a room administrator can manage this lobby.",
	)
	if !ok {
		return store.Room{}, false
	}
	return room, true
}

// markActive marks room in use before a token for it leaves tide, so that
// the anonymous sweep (ARCHITECTURE.md §5), which deletes a room only if it
// is still idle, never deletes one somebody is about to meet in. The media
// server's participant_joined webhook marks it again, later. It answers the
// request itself, and returns false, when the room can't be marked: in
// particular when it was deleted after the caller loaded it, since a token
// for a deleted room's slug could reach a room created under it later.
func (h *Handler) markActive(w http.ResponseWriter, r *http.Request, room store.Room) bool {
	found, err := h.store.MarkRoomActive(r.Context(), room.ID, time.Now().Unix())
	if err != nil {
		log.Printf("lobby: mark %q active: %v", room.Slug, err)
		httpx.WriteError(w, http.StatusInternalServerError, "Could not join the room. Try again.")
		return false
	}
	if !found {
		httpx.WriteError(w, http.StatusNotFound, "Room not found.")
		return false
	}
	return true
}

func (h *Handler) writeAdmission(w http.ResponseWriter, r *http.Request, identity, name string, room store.Room, host bool) {
	if !h.markActive(w, r, room) {
		return
	}
	token, err := h.minter.MintToken(identity, name, room.Slug, host, TokenTTL)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not join the room. Try again.")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, api.JoinResponse{
		Status: string(StatusAdmitted), Token: token, WSURL: h.minter.PublicURL(),
	})
}

func (h *Handler) writeTerminal(stream *sseStream, request Request) {
	switch request.Status {
	case StatusAdmitted:
		_ = stream.send("admitted", api.LobbyAdmittedSSE{Token: request.Token, WSURL: request.WSURL})
	case StatusExpired:
		_ = stream.send("expired", api.LobbyDeniedSSE{})
	default:
		_ = stream.send("denied", api.LobbyDeniedSSE{})
	}
}

func (h *Handler) writePending(stream *sseStream, slug string) error {
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
	return stream.send("pending", api.LobbyPendingSSE{Requests: items})
}

func (h *Handler) writeResolveError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrRequestNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "Lobby request not found.")
		return
	}
	httpx.WriteError(w, http.StatusConflict, "Lobby request has already been resolved.")
}

// sseStream wraps a response for event streaming: every write carries its
// own deadline, so a client that stops reading tears the stream down at the
// next heartbeat instead of wedging a goroutine forever.
type sseStream struct {
	w       http.ResponseWriter
	control *http.ResponseController
}

// beginStream sets SSE headers and clears the server's global read/write
// deadlines for this connection — SSE streams outlive the 30-second timeouts
// that protect every ordinary route. Writes get per-write deadlines instead.
func beginStream(w http.ResponseWriter) *sseStream {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	control := http.NewResponseController(w)
	_ = control.SetReadDeadline(time.Time{})
	_ = control.SetWriteDeadline(time.Time{})
	return &sseStream{w: w, control: control}
}

func (s *sseStream) send(event string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.write(fmt.Sprintf("event: %s\ndata: %s\n\n", event, payload))
}

func (s *sseStream) comment(text string) error {
	return s.write(": " + text + "\n\n")
}

func (s *sseStream) write(frame string) error {
	_ = s.control.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
	if _, err := io.WriteString(s.w, frame); err != nil {
		return err
	}
	if err := s.control.Flush(); err != nil {
		return err
	}
	_ = s.control.SetWriteDeadline(time.Time{})
	return nil
}

func guestIdentity() (string, error) {
	id, err := randomHex(4)
	if err != nil {
		return "", err
	}
	return "guest:" + id, nil
}

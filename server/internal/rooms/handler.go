package rooms

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/httpx"
	"klisi/internal/store"
)

// objectStore is the part of the recording object store room deletion needs:
// stored files must be removed before their database rows disappear.
type objectStore interface {
	Remove(ctx context.Context, key string) error
}

type pendingLobbySource interface {
	HasPending(roomSlug string) bool
}

type Handler struct {
	store        *store.Store
	service      *Service
	objects      objectStore
	live         LiveRoomSource
	pendingLobby pendingLobbySource
}

// NewHandler builds the rooms handler. live and pendingLobby may be nil in
// focused tests. Without live state the list reports every room as inactive.
func NewHandler(
	roomStore *store.Store,
	objects objectStore,
	live LiveRoomSource,
	pendingLobby pendingLobbySource,
) *Handler {
	return &Handler{
		store: roomStore, service: NewService(roomStore), objects: objects,
		live: live, pendingLobby: pendingLobby,
	}
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	var request api.CreateRoomRequest
	if err := httpx.DecodeJSON(w, r, &request); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	name := strings.TrimSpace(request.Name)
	if name == "" || len(name) > 100 {
		httpx.WriteError(w, http.StatusBadRequest, "Room name must be between 1 and 100 characters.")
		return
	}
	room, err := h.service.Create(r.Context(), name, session.Sub)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not create the room. Try again.")
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, roomInfo(room))
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	var owned []store.Room
	var err error
	if session.IsAdmin {
		owned, err = h.store.Rooms(r.Context())
	} else {
		owned, err = h.store.RoomsByOwner(r.Context(), session.Sub)
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not load rooms. Try again.")
		return
	}
	// Live state is best-effort: if the SFU is unreachable the dashboard still
	// renders with rooms shown as inactive rather than erroring.
	var liveRooms map[string]LiveRoom
	if h.live != nil {
		if live, liveErr := h.live.ActiveRooms(r.Context()); liveErr != nil {
			log.Printf("rooms: live state unavailable: %v", liveErr)
		} else {
			liveRooms = live
		}
	}
	now := time.Now().Unix()
	response := make([]api.RoomInfo, 0, len(owned))
	for _, room := range owned {
		info := roomInfo(room)
		if live, ok := liveRooms[room.Slug]; ok && live.NumParticipants > 0 {
			info.Active = true
			info.NumParticipants = live.NumParticipants
			info.Recording = live.Recording
			// A currently-live room is active now, even if the join webhook
			// that persists last_active_at was lost.
			if info.LastActiveAt == nil || *info.LastActiveAt < now {
				info.LastActiveAt = &now
			}
		}
		response = append(response, info)
	}
	httpx.WriteJSON(w, http.StatusOK, response)
}

func (h *Handler) Public(w http.ResponseWriter, r *http.Request) {
	room, err := h.store.RoomBySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "Room not found.")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not load the room. Try again.")
		return
	}
	session, _ := auth.SessionFromContext(r.Context())
	httpx.WriteJSON(w, http.StatusOK, api.PublicRoomInfo{
		Slug: room.Slug, Name: room.Name, LobbyEnabled: room.LobbyEnabled,
		CanManage: session.Sub != "" && httpx.CanManageRoom(session, room),
	})
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	room, err := h.store.RoomBySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "Room not found.")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not load the room. Try again.")
		return
	}
	if !httpx.CanManageRoom(session, room) {
		httpx.WriteError(w, http.StatusForbidden, "Only a room administrator can change this room.")
		return
	}
	var request api.UpdateRoomRequest
	if err := httpx.DecodeJSON(w, r, &request); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	if request.Name == nil && request.Slug == nil && request.LobbyEnabled == nil {
		httpx.WriteError(w, http.StatusBadRequest, "Provide a room name, slug, or lobby setting to update.")
		return
	}
	if request.Name != nil {
		name := strings.TrimSpace(*request.Name)
		if name == "" || len(name) > 100 {
			httpx.WriteError(w, http.StatusBadRequest, "Room name must be between 1 and 100 characters.")
			return
		}
		room.Name = name
	}
	if request.LobbyEnabled != nil {
		room.LobbyEnabled = *request.LobbyEnabled
	}
	if request.Slug != nil {
		slug := normalizeSlug(*request.Slug)
		if err := validateSlug(slug); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, err.Error()+".")
			return
		}
		if slug != room.Slug {
			if h.pendingLobby != nil && h.pendingLobby.HasPending(room.Slug) {
				httpx.WriteError(w, http.StatusConflict, "Admit or deny waiting guests before changing the room link.")
				return
			}
			if _, err := h.store.ActiveRecordingByRoomID(r.Context(), room.ID); err == nil {
				httpx.WriteError(w, http.StatusConflict, "Stop the recording before changing the room link.")
				return
			} else if !errors.Is(err, sql.ErrNoRows) {
				httpx.WriteError(w, http.StatusInternalServerError, "Could not check the recording state. Try again.")
				return
			}
			if h.live != nil {
				liveRooms, err := h.live.ActiveRooms(r.Context())
				if err != nil {
					httpx.WriteError(w, http.StatusServiceUnavailable, "Could not verify that the room is idle. Try again.")
					return
				}
				if live := liveRooms[room.Slug]; live.NumParticipants > 0 || live.Recording {
					httpx.WriteError(w, http.StatusConflict, "Wait until the meeting is empty before changing the room link.")
					return
				}
			}
			room.Slug = slug
		}
	}
	if err := h.store.UpdateRoom(r.Context(), room); err != nil {
		if store.IsSlugConflict(err) {
			httpx.WriteError(w, http.StatusConflict, "That room link is already in use.")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "Could not update the room. Try again.")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, roomInfo(room))
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	room, err := h.store.RoomBySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "Room not found.")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not load the room. Try again.")
		return
	}
	if !httpx.CanManageRoom(session, room) {
		httpx.WriteError(w, http.StatusForbidden, "Only a room administrator can delete this room.")
		return
	}
	// An in-progress recording would keep writing to a room that no longer
	// exists; the reconciler guarantees stuck rows eventually go terminal,
	// so refusing here can never brick deletion permanently.
	if _, err := h.store.ActiveRecordingByRoomID(r.Context(), room.ID); err == nil {
		httpx.WriteError(w, http.StatusConflict, "Stop the recording before deleting the room.")
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not check the recording state. Try again.")
		return
	}
	// Remove stored files before the rows: once the room is gone the files
	// would be unreachable through the app forever.
	recordings, err := h.store.RecordingsByRoomSlug(r.Context(), room.Slug)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not load the room's recordings. Try again.")
		return
	}
	for _, recording := range recordings {
		if recording.S3Key == nil || *recording.S3Key == "" {
			continue
		}
		if err := h.objects.Remove(r.Context(), *recording.S3Key); err != nil {
			httpx.WriteError(w, http.StatusBadGateway, "Could not delete the room's recording files. Try again.")
			return
		}
	}
	// Recording rows go with the room via ON DELETE CASCADE (foreign_keys=ON).
	if err := h.store.DeleteRoom(r.Context(), room.ID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not delete the room. Try again.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func roomInfo(room store.Room) api.RoomInfo {
	return api.RoomInfo{
		ID: room.ID, Slug: room.Slug, Name: room.Name,
		LobbyEnabled: room.LobbyEnabled, CreatedAt: room.CreatedAt,
		LastActiveAt: room.LastActiveAt,
	}
}

package rooms

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

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

type Handler struct {
	store   *store.Store
	service *Service
	objects objectStore
}

func NewHandler(roomStore *store.Store, objects objectStore) *Handler {
	return &Handler{store: roomStore, service: NewService(roomStore), objects: objects}
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
	owned, err := h.store.RoomsByOwner(r.Context(), session.Sub)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not load rooms. Try again.")
		return
	}
	response := make([]api.RoomInfo, 0, len(owned))
	for _, room := range owned {
		response = append(response, roomInfo(room))
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
	httpx.WriteJSON(w, http.StatusOK, api.PublicRoomInfo{
		Slug: room.Slug, Name: room.Name, LobbyEnabled: room.LobbyEnabled,
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
	if room.OwnerSub != session.Sub {
		httpx.WriteError(w, http.StatusForbidden, "Only the room owner can change this room.")
		return
	}
	var request api.UpdateRoomRequest
	if err := httpx.DecodeJSON(w, r, &request); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	if request.Name == nil && request.LobbyEnabled == nil {
		httpx.WriteError(w, http.StatusBadRequest, "Provide a room name or lobby setting to update.")
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
	if err := h.store.UpdateRoom(r.Context(), room); err != nil {
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
	if room.OwnerSub != session.Sub {
		httpx.WriteError(w, http.StatusForbidden, "Only the room owner can delete this room.")
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
	}
}

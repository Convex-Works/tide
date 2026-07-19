package rooms

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/store"
)

type Handler struct {
	store   *store.Store
	service *Service
}

func NewHandler(roomStore *store.Store) *Handler {
	return &Handler{store: roomStore, service: NewService(roomStore)}
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	var request api.CreateRoomRequest
	if err := decodeRequest(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	name := strings.TrimSpace(request.Name)
	if name == "" || len(name) > 100 {
		writeError(w, http.StatusBadRequest, "Room name must be between 1 and 100 characters.")
		return
	}
	room, err := h.service.Create(r.Context(), name, session.Sub)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not create the room. Try again.")
		return
	}
	writeJSON(w, http.StatusCreated, roomInfo(room))
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	owned, err := h.store.RoomsByOwner(r.Context(), session.Sub)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load rooms. Try again.")
		return
	}
	response := make([]api.RoomInfo, 0, len(owned))
	for _, room := range owned {
		response = append(response, roomInfo(room))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) Public(w http.ResponseWriter, r *http.Request) {
	room, err := h.store.RoomBySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Room not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load the room. Try again.")
		return
	}
	writeJSON(w, http.StatusOK, api.PublicRoomInfo{
		Slug: room.Slug, Name: room.Name, LobbyEnabled: room.LobbyEnabled,
	})
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	room, err := h.store.RoomBySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Room not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load the room. Try again.")
		return
	}
	if room.OwnerSub != session.Sub {
		writeError(w, http.StatusForbidden, "Only the room owner can change this room.")
		return
	}
	var request api.UpdateRoomRequest
	if err := decodeRequest(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "Request body must be valid JSON.")
		return
	}
	if request.Name == nil && request.LobbyEnabled == nil {
		writeError(w, http.StatusBadRequest, "Provide a room name or lobby setting to update.")
		return
	}
	if request.Name != nil {
		name := strings.TrimSpace(*request.Name)
		if name == "" || len(name) > 100 {
			writeError(w, http.StatusBadRequest, "Room name must be between 1 and 100 characters.")
			return
		}
		room.Name = name
	}
	if request.LobbyEnabled != nil {
		room.LobbyEnabled = *request.LobbyEnabled
	}
	if err := h.store.UpdateRoom(r.Context(), room); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update the room. Try again.")
		return
	}
	writeJSON(w, http.StatusOK, roomInfo(room))
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	room, err := h.store.RoomBySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Room not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load the room. Try again.")
		return
	}
	if room.OwnerSub != session.Sub {
		writeError(w, http.StatusForbidden, "Only the room owner can delete this room.")
		return
	}
	if err := h.store.DeleteRoom(r.Context(), room.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not delete the room. Try again.")
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

func decodeRequest(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
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

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, api.ErrorResponse{Error: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

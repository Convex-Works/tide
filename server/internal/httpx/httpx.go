package httpx

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"klisi/internal/api"
	"klisi/internal/auth/sessionctx"
	"klisi/internal/store"
)

const maxJSONRequestBody = 1 << 20

type RoomLoader interface {
	RoomBySlug(context.Context, string) (store.Room, error)
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func WriteError(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, api.ErrorResponse{Error: message})
}

func DecodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
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

func RequireRoomOwner(
	w http.ResponseWriter,
	r *http.Request,
	rooms RoomLoader,
	slug string,
	forbidden string,
) (store.Room, sessionctx.Session, bool) {
	session, ok := sessionctx.FromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "Authentication required.")
		return store.Room{}, sessionctx.Session{}, false
	}
	room, err := rooms.RoomBySlug(r.Context(), slug)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "Room not found.")
		return store.Room{}, sessionctx.Session{}, false
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Could not load the room. Try again.")
		return store.Room{}, sessionctx.Session{}, false
	}
	if room.OwnerSub != session.Sub {
		WriteError(w, http.StatusForbidden, forbidden)
		return store.Room{}, sessionctx.Session{}, false
	}
	return room, session, true
}

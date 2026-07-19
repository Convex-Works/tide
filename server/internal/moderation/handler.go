package moderation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	protocol "github.com/livekit/protocol/livekit"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/store"
)

type roomStore interface {
	RoomBySlug(context.Context, string) (store.Room, error)
}

// RoomService is the small part of LiveKit's server client used for moderation.
// Keeping it narrow makes authorization and failure behavior testable without an
// SFU.
type RoomService interface {
	ListParticipants(context.Context, *protocol.ListParticipantsRequest) (*protocol.ListParticipantsResponse, error)
	RemoveParticipant(context.Context, *protocol.RoomParticipantIdentity) (*protocol.RemoveParticipantResponse, error)
	MutePublishedTrack(context.Context, *protocol.MuteRoomTrackRequest) (*protocol.MuteRoomTrackResponse, error)
}

type Handler struct {
	store   roomStore
	service RoomService
}

func NewHandler(rooms roomStore, service RoomService) *Handler {
	return &Handler{store: rooms, service: service}
}

func (h *Handler) Kick(w http.ResponseWriter, r *http.Request) {
	room, ok := h.requireOwner(w, r)
	if !ok {
		return
	}
	identity := r.PathValue("identity")
	if _, ok := h.findParticipant(w, r, room.Slug, identity); !ok {
		return
	}
	if _, err := h.service.RemoveParticipant(r.Context(), &protocol.RoomParticipantIdentity{
		Room: room.Slug, Identity: identity,
	}); err != nil {
		writeError(w, http.StatusBadGateway, "LiveKit could not remove the participant. Try again.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Mute(w http.ResponseWriter, r *http.Request) {
	room, ok := h.requireOwner(w, r)
	if !ok {
		return
	}
	identity := r.PathValue("identity")
	participant, ok := h.findParticipant(w, r, room.Slug, identity)
	if !ok {
		return
	}
	var microphoneSID string
	for _, track := range participant.Tracks {
		if track.Source == protocol.TrackSource_MICROPHONE {
			microphoneSID = track.Sid
			break
		}
	}
	if microphoneSID == "" {
		writeError(w, http.StatusNotFound, "Participant microphone not found.")
		return
	}
	if _, err := h.service.MutePublishedTrack(r.Context(), &protocol.MuteRoomTrackRequest{
		Room: room.Slug, Identity: identity, TrackSid: microphoneSID, Muted: true,
	}); err != nil {
		writeError(w, http.StatusBadGateway, "LiveKit could not mute the participant. Try again.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) requireOwner(w http.ResponseWriter, r *http.Request) (store.Room, bool) {
	session, ok := auth.SessionFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Authentication required.")
		return store.Room{}, false
	}
	room, err := h.store.RoomBySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "Room not found.")
		return store.Room{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load the room. Try again.")
		return store.Room{}, false
	}
	if room.OwnerSub != session.Sub {
		writeError(w, http.StatusForbidden, "Only the room owner can moderate participants.")
		return store.Room{}, false
	}
	return room, true
}

func (h *Handler) findParticipant(
	w http.ResponseWriter,
	r *http.Request,
	roomName string,
	identity string,
) (*protocol.ParticipantInfo, bool) {
	response, err := h.service.ListParticipants(r.Context(), &protocol.ListParticipantsRequest{Room: roomName})
	if err != nil {
		writeError(w, http.StatusBadGateway, "LiveKit could not list participants. Try again.")
		return nil, false
	}
	for _, participant := range response.Participants {
		if participant.Identity == identity {
			return participant, true
		}
	}
	writeError(w, http.StatusNotFound, "Participant not found.")
	return nil, false
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(api.ErrorResponse{Error: message})
}

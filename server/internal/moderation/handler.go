package moderation

import (
	"context"
	"log"
	"net/http"
	"strings"

	protocol "github.com/livekit/protocol/livekit"

	"klisi/internal/httpx"
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
	store    roomStore
	service  RoomService
	denylist *Denylist
}

func NewHandler(rooms roomStore, service RoomService, denylist *Denylist) *Handler {
	return &Handler{store: rooms, service: service, denylist: denylist}
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
	// Ban before removing so a rejoin racing the kick is still caught. The
	// owner's own identities are never banned — kicking your own other tab
	// must not lock you out of your own room.
	if h.denylist != nil && !isOwnerIdentity(identity, room.OwnerSub) {
		h.denylist.Ban(room.Slug, identity)
	}
	if _, err := h.service.RemoveParticipant(r.Context(), &protocol.RoomParticipantIdentity{
		Room: room.Slug, Identity: identity,
	}); err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "LiveKit could not remove the participant. Try again.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// EnforceOnJoin re-removes a kicked participant that reconnected with a
// cached admission token. Driven by the LiveKit participant_joined webhook,
// so the rejoin window is one webhook round-trip.
func (h *Handler) EnforceOnJoin(ctx context.Context, room, identity string) {
	if h.denylist == nil || !h.denylist.Banned(room, identity) {
		return
	}
	if _, err := h.service.RemoveParticipant(ctx, &protocol.RoomParticipantIdentity{
		Room: room, Identity: identity,
	}); err != nil {
		log.Printf("moderation: could not re-remove banned participant %s from %s: %v", identity, room, err)
	}
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
		httpx.WriteError(w, http.StatusNotFound, "Participant microphone not found.")
		return
	}
	if _, err := h.service.MutePublishedTrack(r.Context(), &protocol.MuteRoomTrackRequest{
		Room: room.Slug, Identity: identity, TrackSid: microphoneSID, Muted: true,
	}); err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "LiveKit could not mute the participant. Try again.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// isOwnerIdentity matches the owner's LiveKit identities: "host:<sub>" plus
// the per-connection "host:<sub>:<nonce>" form minted by the lobby.
func isOwnerIdentity(identity, ownerSub string) bool {
	prefix := "host:" + ownerSub
	return identity == prefix || strings.HasPrefix(identity, prefix+":")
}

func (h *Handler) requireOwner(w http.ResponseWriter, r *http.Request) (store.Room, bool) {
	room, _, ok := httpx.RequireRoomOwner(
		w, r, h.store, r.PathValue("slug"), "Only the room owner can moderate participants.",
	)
	if !ok {
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
		httpx.WriteError(w, http.StatusBadGateway, "LiveKit could not list participants. Try again.")
		return nil, false
	}
	for _, participant := range response.Participants {
		if participant.Identity == identity {
			return participant, true
		}
	}
	httpx.WriteError(w, http.StatusNotFound, "Participant not found.")
	return nil, false
}

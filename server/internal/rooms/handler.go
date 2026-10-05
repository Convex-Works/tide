package rooms

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"tide/internal/api"
	"tide/internal/auth"
	"tide/internal/httpx"
	"tide/internal/recording"
	"tide/internal/store"
)

// maxNameLength is the longest room name, in characters (runes), not bytes:
// a name in Greek or Japanese takes two or three bytes a character.
const maxNameLength = 100

// objectStore is the part of the recording object store room deletion
// needs: it removes the files of the room's recordings.
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
	// ender ends the meeting in a room as it is deleted; nil in focused
	// tests.
	ender MeetingEnder
	// roomCap is how many rooms may exist before Create refuses another; 0
	// means no cap. Only an anonymous deployment has one (§4.1).
	roomCap int
	// onRoomDeleted is called after a room and its recordings are deleted,
	// so that the transcripts reconciler can stop their jobs at once.
	onRoomDeleted func()
}

// AnonymousRoomCap is how many rooms an anonymous deployment keeps in
// memory before creating another answers 503 (ARCHITECTURE.md §4.1): with
// addresses cheap, only a ceiling bounds memory.
const AnonymousRoomCap = 10_000

// SetMeetingEnder sets what ends the meeting in a room as it is deleted.
func (h *Handler) SetMeetingEnder(ender MeetingEnder) {
	h.ender = ender
}

// SetRoomCap caps how many rooms may exist; 0 removes the cap.
func (h *Handler) SetRoomCap(n int) {
	h.roomCap = n
}

// SetRoomDeletedHook registers a callback invoked after a room is deleted,
// with its recordings. It must not block.
func (h *Handler) SetRoomDeletedHook(hook func()) {
	h.onRoomDeleted = hook
}

// NewHandler builds the rooms handler. live and pendingLobby may be nil in
// focused tests. Without live state the list reports every room as inactive.
// objects is nil when recording is off: deleting a room then leaves its
// recordings' files queued for removal.
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
	// Both are optional (ARCHITECTURE.md §5): a blank name becomes the
	// slug, and a blank slug is generated, like a missing one.
	name := strings.TrimSpace(request.Name)
	if utf8.RuneCountInString(name) > maxNameLength {
		httpx.WriteError(w, http.StatusBadRequest, "Room name must be at most 100 characters.")
		return
	}
	var slug string
	if request.Slug != nil {
		slug = normalizeSlug(*request.Slug)
	}
	if slug != "" {
		if err := validateSlug(slug); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, err.Error()+".")
			return
		}
	}
	if h.roomCap > 0 {
		// The count and the insert aren't one statement, so creates racing
		// at the ceiling may pass it by a few: a bound on memory, not a
		// quota.
		count, err := h.store.CountRooms(r.Context())
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Could not create the room. Try again.")
			return
		}
		if count >= h.roomCap {
			httpx.WriteError(w, http.StatusServiceUnavailable, "This server has too many rooms. Try again later.")
			return
		}
	}
	room, err := h.service.Create(r.Context(), name, slug, session.Sub)
	if errors.Is(err, ErrSlugTaken) {
		httpx.WriteError(w, http.StatusConflict, "That room link is already in use.")
		return
	}
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
		if name == "" || utf8.RuneCountInString(name) > maxNameLength {
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
	if err := h.store.UpdateRoom(r.Context(), room, time.Now().Unix()); err != nil {
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
	// The room and its recordings go, and every file of those recordings is
	// queued for removal, at once (ARCHITECTURE.md §8.1): from then on the
	// files are removed even if storage is down now.
	now := time.Now().Unix()
	keys, err := h.store.DeleteRoom(r.Context(), room.ID, now)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, http.StatusNotFound, "Room not found.")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "Could not delete the room. Try again.")
		return
	}
	if h.onRoomDeleted != nil {
		h.onRoomDeleted()
	}
	endMeeting(r.Context(), h.ender, room.Slug)
	// Without recording there is no storage to remove them from: the keys
	// stay queued until storage is configured again (ARCHITECTURE.md §8).
	if h.objects != nil {
		recording.RemoveDeleted(r.Context(), h.objects, h.store, keys, now)
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

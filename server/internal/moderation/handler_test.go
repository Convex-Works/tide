package moderation

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	protocol "github.com/livekit/protocol/livekit"
	"github.com/twitchtv/twirp"

	"klisi/internal/auth"
	"klisi/internal/store"
)

type fakeRoomStore struct {
	room store.Room
	err  error
}

func (f fakeRoomStore) RoomBySlug(context.Context, string) (store.Room, error) {
	return f.room, f.err
}

type fakeRoomService struct {
	participants []*protocol.ParticipantInfo
	listErr      error
	removeErr    error
	muteErr      error
	deleteErr    error
	listed       int
	removed      *protocol.RoomParticipantIdentity
	muted        *protocol.MuteRoomTrackRequest
	deleted      *protocol.DeleteRoomRequest
}

func (f *fakeRoomService) ListParticipants(context.Context, *protocol.ListParticipantsRequest) (*protocol.ListParticipantsResponse, error) {
	f.listed++
	return &protocol.ListParticipantsResponse{Participants: f.participants}, f.listErr
}

func (f *fakeRoomService) RemoveParticipant(_ context.Context, request *protocol.RoomParticipantIdentity) (*protocol.RemoveParticipantResponse, error) {
	f.removed = request
	return &protocol.RemoveParticipantResponse{}, f.removeErr
}

func (f *fakeRoomService) MutePublishedTrack(_ context.Context, request *protocol.MuteRoomTrackRequest) (*protocol.MuteRoomTrackResponse, error) {
	f.muted = request
	return &protocol.MuteRoomTrackResponse{}, f.muteErr
}

func (f *fakeRoomService) DeleteRoom(_ context.Context, request *protocol.DeleteRoomRequest) (*protocol.DeleteRoomResponse, error) {
	f.deleted = request
	return &protocol.DeleteRoomResponse{}, f.deleteErr
}

func TestModerationOwnershipMatrix(t *testing.T) {
	room := store.Room{Slug: "calm-otter-412", OwnerSub: "owner"}
	participant := &protocol.ParticipantInfo{
		Identity: "guest:1234",
		Tracks:   []*protocol.TrackInfo{{Sid: "TR_audio", Source: protocol.TrackSource_MICROPHONE}},
	}
	tests := []struct {
		name       string
		store      fakeRoomStore
		sessionSub string
		wantStatus int
		wantList   bool
	}{
		{name: "authentication required", store: fakeRoomStore{room: room}, wantStatus: http.StatusUnauthorized},
		{name: "room not found", store: fakeRoomStore{err: sql.ErrNoRows}, sessionSub: "owner", wantStatus: http.StatusNotFound},
		{name: "non-owner forbidden", store: fakeRoomStore{room: room}, sessionSub: "someone-else", wantStatus: http.StatusForbidden},
		{name: "owner authorized", store: fakeRoomStore{room: room}, sessionSub: "owner", wantStatus: http.StatusNoContent, wantList: true},
	}

	for _, action := range []struct {
		name string
		run  func(*Handler, http.ResponseWriter, *http.Request)
	}{
		{name: "kick", run: (*Handler).Kick},
		{name: "mute", run: (*Handler).Mute},
	} {
		t.Run(action.name, func(t *testing.T) {
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					service := &fakeRoomService{participants: []*protocol.ParticipantInfo{participant}}
					handler := NewHandler(test.store, service, NewDenylist(time.Minute))
					request := httptest.NewRequest(http.MethodPost, "/moderate", nil)
					request.SetPathValue("slug", room.Slug)
					request.SetPathValue("identity", participant.Identity)
					if test.sessionSub != "" {
						request = request.WithContext(auth.WithSession(request.Context(), auth.Session{Sub: test.sessionSub}))
					}
					recorder := httptest.NewRecorder()
					action.run(handler, recorder, request)

					if recorder.Code != test.wantStatus {
						t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
					}
					if (service.listed > 0) != test.wantList {
						t.Fatalf("ListParticipants calls = %d", service.listed)
					}
					if test.wantStatus == http.StatusNoContent {
						if action.name == "kick" && (service.removed == nil || service.removed.Identity != participant.Identity) {
							t.Fatalf("RemoveParticipant request = %#v", service.removed)
						}
						if action.name == "mute" && (service.muted == nil || service.muted.TrackSid != "TR_audio" || !service.muted.Muted) {
							t.Fatalf("MutePublishedTrack request = %#v", service.muted)
						}
					}
				})
			}
		})
	}
}

func TestModerationParticipantAndLiveKitFailures(t *testing.T) {
	room := store.Room{Slug: "calm-otter-412", OwnerSub: "owner"}
	request := func(identity string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/moderate", nil)
		r.SetPathValue("slug", room.Slug)
		r.SetPathValue("identity", identity)
		return r.WithContext(auth.WithSession(r.Context(), auth.Session{Sub: "owner"}))
	}

	t.Run("unknown participant", func(t *testing.T) {
		handler := NewHandler(fakeRoomStore{room: room}, &fakeRoomService{}, NewDenylist(time.Minute))
		recorder := httptest.NewRecorder()
		handler.Kick(recorder, request("guest:missing"))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("microphone missing", func(t *testing.T) {
		service := &fakeRoomService{participants: []*protocol.ParticipantInfo{{Identity: "guest:1234"}}}
		handler := NewHandler(fakeRoomStore{room: room}, service, NewDenylist(time.Minute))
		recorder := httptest.NewRecorder()
		handler.Mute(recorder, request("guest:1234"))
		if recorder.Code != http.StatusNotFound || service.muted != nil {
			t.Fatalf("status = %d, mute request = %#v", recorder.Code, service.muted)
		}
	})

	t.Run("LiveKit rejection", func(t *testing.T) {
		service := &fakeRoomService{
			participants: []*protocol.ParticipantInfo{{Identity: "guest:1234"}},
			removeErr:    errors.New("rejected"),
		}
		handler := NewHandler(fakeRoomStore{room: room}, service, NewDenylist(time.Minute))
		recorder := httptest.NewRecorder()
		handler.Kick(recorder, request("guest:1234"))
		if recorder.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("LiveKit list rejection", func(t *testing.T) {
		handler := NewHandler(fakeRoomStore{room: room}, &fakeRoomService{listErr: errors.New("rejected")}, NewDenylist(time.Minute))
		recorder := httptest.NewRecorder()
		handler.Kick(recorder, request("guest:1234"))
		if recorder.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("LiveKit mute rejection", func(t *testing.T) {
		service := &fakeRoomService{
			participants: []*protocol.ParticipantInfo{{
				Identity: "guest:1234",
				Tracks: []*protocol.TrackInfo{{
					Sid: "TR_audio", Source: protocol.TrackSource_MICROPHONE,
				}},
			}},
			muteErr: errors.New("rejected"),
		}
		handler := NewHandler(fakeRoomStore{room: room}, service, NewDenylist(time.Minute))
		recorder := httptest.NewRecorder()
		handler.Mute(recorder, request("guest:1234"))
		if recorder.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestEndMeeting(t *testing.T) {
	room := store.Room{Slug: "calm-otter-412", OwnerSub: "owner"}
	request := func(sub string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/end", nil)
		r.SetPathValue("slug", room.Slug)
		if sub != "" {
			r = r.WithContext(auth.WithSession(r.Context(), auth.Session{Sub: sub}))
		}
		return r
	}

	t.Run("owner ends the meeting", func(t *testing.T) {
		service := &fakeRoomService{}
		handler := NewHandler(fakeRoomStore{room: room}, service, NewDenylist(time.Minute))
		recorder := httptest.NewRecorder()
		handler.EndMeeting(recorder, request("owner"))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if service.deleted == nil || service.deleted.Room != room.Slug {
			t.Fatalf("DeleteRoom request = %#v", service.deleted)
		}
	})

	t.Run("non-owner forbidden", func(t *testing.T) {
		service := &fakeRoomService{}
		handler := NewHandler(fakeRoomStore{room: room}, service, NewDenylist(time.Minute))
		recorder := httptest.NewRecorder()
		handler.EndMeeting(recorder, request("someone-else"))
		if recorder.Code != http.StatusForbidden || service.deleted != nil {
			t.Fatalf("status = %d, DeleteRoom request = %#v", recorder.Code, service.deleted)
		}
	})

	t.Run("LiveKit rejection", func(t *testing.T) {
		service := &fakeRoomService{deleteErr: errors.New("rejected")}
		handler := NewHandler(fakeRoomStore{room: room}, service, NewDenylist(time.Minute))
		recorder := httptest.NewRecorder()
		handler.EndMeeting(recorder, request("owner"))
		if recorder.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("no live session is success", func(t *testing.T) {
		// LiveKit reports not_found when the room has no live session — the
		// meeting is already over, so ending it succeeds.
		service := &fakeRoomService{deleteErr: twirp.NotFoundError("room not found")}
		handler := NewHandler(fakeRoomStore{room: room}, service, NewDenylist(time.Minute))
		recorder := httptest.NewRecorder()
		handler.EndMeeting(recorder, request("owner"))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestLiveKitHTTPURL(t *testing.T) {
	tests := map[string]string{
		"ws://livekit:7880":          "http://livekit:7880",
		"wss://livekit.example/path": "https://livekit.example/path",
		"http://livekit:7880":        "http://livekit:7880",
	}
	for input, want := range tests {
		if got := liveKitHTTPURL(input); got != want {
			t.Errorf("liveKitHTTPURL(%q) = %q, want %q", input, got, want)
		}
	}
}

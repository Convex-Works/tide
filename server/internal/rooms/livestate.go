package rooms

import (
	"context"
	"errors"
	"log"
	"net/url"

	protocol "github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/twitchtv/twirp"

	"tide/internal/config"
)

// LiveRoom is the current SFU-side state of one room, keyed by slug (LiveKit
// room name equals the room slug).
type LiveRoom struct {
	NumParticipants int
	Recording       bool
}

// LiveRoomSource reports which rooms are currently live. The List handler
// treats it as best-effort: an error degrades the dashboard to "no live state"
// rather than failing the whole request.
type LiveRoomSource interface {
	ActiveRooms(ctx context.Context) (map[string]LiveRoom, error)
}

// roomLister is the sliver of LiveKit's RoomServiceClient used here; narrowing
// it keeps ActiveRooms testable without an SFU.
type roomLister interface {
	ListRooms(context.Context, *protocol.ListRoomsRequest) (*protocol.ListRoomsResponse, error)
}

type liveKitSource struct {
	client roomLister
}

// NewLiveKitSource builds a LiveRoomSource backed by LiveKit's RoomService.
func NewLiveKitSource(cfg config.Config) LiveRoomSource {
	client := lksdk.NewRoomServiceClient(liveKitHTTPURL(cfg.LiveKitURL), cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)
	return &liveKitSource{client: client}
}

func (s *liveKitSource) ActiveRooms(ctx context.Context) (map[string]LiveRoom, error) {
	response, err := s.client.ListRooms(ctx, &protocol.ListRoomsRequest{})
	if err != nil {
		return nil, err
	}
	live := make(map[string]LiveRoom, len(response.Rooms))
	for _, room := range response.Rooms {
		live[room.Name] = LiveRoom{
			NumParticipants: int(room.NumParticipants),
			Recording:       room.ActiveRecording,
		}
	}
	return live, nil
}

// A MeetingEnder ends the meeting live in a room, if there is one, as End
// meeting does: everyone in it is disconnected. Deleting a room calls it
// (ARCHITECTURE.md §5), so nobody stays on in a meeting whose slug someone
// else can now create and own.
type MeetingEnder interface {
	EndMeeting(ctx context.Context, slug string) error
}

type roomDeleter interface {
	DeleteRoom(context.Context, *protocol.DeleteRoomRequest) (*protocol.DeleteRoomResponse, error)
}

type liveKitEnder struct {
	client roomDeleter
}

// NewLiveKitMeetingEnder builds a MeetingEnder backed by LiveKit's
// RoomService.
func NewLiveKitMeetingEnder(cfg config.Config) MeetingEnder {
	client := lksdk.NewRoomServiceClient(liveKitHTTPURL(cfg.LiveKitURL), cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)
	return &liveKitEnder{client: client}
}

// EndMeeting deletes the room's media session. A room without one (twirp
// not_found) has no meeting to end, which is success.
func (e *liveKitEnder) EndMeeting(ctx context.Context, slug string) error {
	_, err := e.client.DeleteRoom(ctx, &protocol.DeleteRoomRequest{Room: slug})
	var twirpError twirp.Error
	if errors.As(err, &twirpError) && twirpError.Code() == twirp.NotFound {
		return nil
	}
	return err
}

// endMeeting ends the meeting in slug, if ender is set, logging a failure:
// the room is gone either way, and a meeting nobody can find any more ends
// when its last participant leaves.
func endMeeting(ctx context.Context, ender MeetingEnder, slug string) {
	if ender == nil {
		return
	}
	if err := ender.EndMeeting(ctx, slug); err != nil {
		log.Printf("rooms: end the meeting in deleted room %q: %v", slug, err)
	}
}

// liveKitHTTPURL converts a ws(s):// SFU URL to its http(s):// API form.
func liveKitHTTPURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	switch parsed.Scheme {
	case "ws":
		parsed.Scheme = "http"
	case "wss":
		parsed.Scheme = "https"
	}
	return parsed.String()
}

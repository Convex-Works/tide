package rooms

import (
	"context"
	"net/url"

	protocol "github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"

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

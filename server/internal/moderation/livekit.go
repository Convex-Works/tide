package moderation

import (
	"net/url"

	lksdk "github.com/livekit/server-sdk-go/v2"

	"tide/internal/config"
)

func NewRoomService(cfg config.Config) RoomService {
	return lksdk.NewRoomServiceClient(liveKitHTTPURL(cfg.LiveKitURL), cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)
}

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

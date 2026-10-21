package httpapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"tide/internal/api"
	"tide/internal/config"
)

// Dev mode's token route mints for anyone who asks, so it must never mint
// admin (ARCHITECTURE.md §4).
func TestDevTokenIsAGuestToken(t *testing.T) {
	k := startTide(t, func(cfg *config.Config, _ *http.Server) { cfg.DevMode = true })
	status, _, body := k.request(http.MethodGet, api.DevTokenPath+"?room=calm-otter-412&name=Ada", "", nil, false)
	if status != http.StatusOK {
		t.Fatalf("dev token = %d %s", status, body)
	}
	var minted api.TokenResponse
	if err := json.Unmarshal([]byte(body), &minted); err != nil {
		t.Fatal(err)
	}
	claims := grants(t, k.cfg, minted.Token)
	video := claims.Video
	if video.RoomAdmin || claims.Metadata != "" || !video.RoomJoin || video.Room != "calm-otter-412" ||
		claims.Identity != "dev:Ada" || claims.Name != "Ada" ||
		!reflect.DeepEqual(video.CanPublishSources, []string{"camera", "microphone", "screen_share", "screen_share_audio"}) {
		t.Fatalf("dev token grants %+v, identity %q, metadata %q", video, claims.Identity, claims.Metadata)
	}
}

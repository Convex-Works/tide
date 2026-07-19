package livekit

import (
	"time"

	protocolauth "github.com/livekit/protocol/auth"
	protocol "github.com/livekit/protocol/livekit"

	"klisi/internal/config"
)

type Minter struct {
	apiKey    string
	apiSecret string
	publicURL string
}

func NewMinter(cfg config.Config) *Minter {
	return &Minter{
		apiKey: cfg.LiveKitAPIKey, apiSecret: cfg.LiveKitAPISecret, publicURL: cfg.LiveKitPublicURL,
	}
}

func (m *Minter) MintToken(identity, name, room string, host bool, ttl time.Duration) (string, error) {
	canPublish := true
	canSubscribe := true
	grant := &protocolauth.VideoGrant{
		RoomJoin: true, Room: room, RoomAdmin: host,
		CanPublish: &canPublish, CanSubscribe: &canSubscribe,
	}
	grant.SetCanPublishSources([]protocol.TrackSource{
		protocol.TrackSource_CAMERA,
		protocol.TrackSource_MICROPHONE,
		protocol.TrackSource_SCREEN_SHARE,
		protocol.TrackSource_SCREEN_SHARE_AUDIO,
	})
	token := protocolauth.NewAccessToken(m.apiKey, m.apiSecret).
		SetIdentity(identity).
		SetName(name).
		SetValidFor(ttl).
		SetVideoGrant(grant)
	if host {
		token.SetMetadata(`{"role":"host"}`)
	}
	return token.ToJWT()
}

func (m *Minter) PublicURL() string {
	return m.publicURL
}

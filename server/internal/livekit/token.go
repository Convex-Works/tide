package livekit

import (
	"fmt"
	"time"

	protocolauth "github.com/livekit/protocol/auth"
	protocol "github.com/livekit/protocol/livekit"

	"tide/internal/config"
)

var (
	participantSources = []protocol.TrackSource{
		protocol.TrackSource_CAMERA,
		protocol.TrackSource_MICROPHONE,
		protocol.TrackSource_SCREEN_SHARE,
		protocol.TrackSource_SCREEN_SHARE_AUDIO,
	}
	agentSources = []protocol.TrackSource{
		protocol.TrackSource_MICROPHONE,
		protocol.TrackSource_SCREEN_SHARE,
		protocol.TrackSource_SCREEN_SHARE_AUDIO,
	}
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

// A Role is what a participant may do in a room. It decides every grant in
// the token, and nothing a client sends can change it (ARCHITECTURE.md §4).
type Role int

const (
	// RoleGuest publishes and subscribes.
	RoleGuest Role = iota
	// RoleHost is a guest who can also administer the room.
	RoleHost
	// RoleAgent is a program acting for the room's owner: it hears the room
	// and answers with audio, chat or a screen share, in view of everyone.
	// It can't turn a camera on or administer the room, and it doesn't keep
	// the room open.
	RoleAgent
	// RoleObserver is tide itself watching who speaks during a recording: it
	// subscribes, publishes nothing, is hidden from the room, and doesn't
	// keep the room open. It's kind AGENT rather than the Recorder grant,
	// which would tell clients the room is recording.
	RoleObserver
)

func (m *Minter) MintToken(identity, name, room string, role Role, ttl time.Duration) (string, error) {
	canPublish := role != RoleObserver
	canSubscribe := true
	grant := &protocolauth.VideoGrant{
		RoomJoin: true, Room: room, RoomAdmin: role == RoleHost,
		CanPublish: &canPublish, CanSubscribe: &canSubscribe,
	}
	token := protocolauth.NewAccessToken(m.apiKey, m.apiSecret).
		SetIdentity(identity).
		SetName(name).
		SetValidFor(ttl)
	switch role {
	case RoleGuest:
		grant.SetCanPublishSources(participantSources)
	case RoleHost:
		grant.SetCanPublishSources(participantSources)
		token.SetMetadata(`{"role":"host"}`)
	case RoleAgent:
		grant.SetCanPublishData(true)
		grant.SetCanPublishSources(agentSources)
		token.SetKind(protocol.ParticipantInfo_AGENT).SetMetadata(`{"role":"agent"}`)
	case RoleObserver:
		grant.SetCanPublishData(false)
		grant.Hidden = true
		token.SetKind(protocol.ParticipantInfo_AGENT)
	default:
		return "", fmt.Errorf("unknown role %d", role)
	}
	return token.SetVideoGrant(grant).ToJWT()
}

func (m *Minter) PublicURL() string {
	return m.publicURL
}

package lobby

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/url"
	"strings"
	"time"

	protocol "github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"

	"tide/internal/config"
	"tide/internal/store"
)

// RejoinTTL caps a rejoin grant's life, however long its meeting runs. Kick
// bans must last at least this long, so a kicked guest's grant never
// outlives the ban (moderation.NewKickDenylist).
const RejoinTTL = 12 * time.Hour

// maxRejoinGrant bounds the work a forged grant can cost; real ones are a
// few hundred bytes.
const maxRejoinGrant = 1024

const rejoinDomain = "tide rejoin grant v1\x00"

// RoomLister is the sliver of LiveKit's RoomServiceClient a rejoin needs:
// whether the meeting a grant was issued in is still the one running.
type RoomLister interface {
	ListRooms(context.Context, *protocol.ListRoomsRequest) (*protocol.ListRoomsResponse, error)
}

// BanList reports kicked participants (moderation.Denylist).
type BanList interface {
	Banned(room, identity string) bool
}

// NewLiveKitRooms builds a RoomLister backed by LiveKit's RoomService.
func NewLiveKitRooms(cfg config.Config) RoomLister {
	return lksdk.NewRoomServiceClient(liveKitHTTPURL(cfg.LiveKitURL), cfg.LiveKitAPIKey, cfg.LiveKitAPISecret)
}

// rejoiner issues and redeems rejoin grants: what a guest admitted through
// the lobby is handed so that, having left, lost the connection or
// reloaded, they return to the same meeting as the same participant
// without the host admitting them again. A grant is signed with a key that
// lives only in this process, as the lobby and the kick denylist do, so a
// restart sends every guest through the lobby again.
type rejoiner struct {
	key   []byte
	rooms RoomLister
	bans  BanList
	now   func() time.Time
}

// rejoinClaims are what a grant binds: the room row (not its slug, which a
// deleted room frees for someone else), the LiveKit session the guest was
// admitted to (a meeting that ends takes its session id with it), the
// identity and name the host admitted, and the cap.
type rejoinClaims struct {
	Room     string `json:"r"`
	Meeting  string `json:"m"`
	Identity string `json:"i"`
	Name     string `json:"n"`
	Expires  int64  `json:"e"`
}

// EnableRejoin makes Approve hand guests a rejoin grant and Join honour it.
// bans must keep a kick for at least RejoinTTL.
func (h *Handler) EnableRejoin(rooms RoomLister, bans BanList) {
	key := make([]byte, 32)
	// crypto/rand.Read never returns an error; it crashes the process instead.
	_, _ = rand.Read(key)
	h.rejoin = &rejoiner{key: key, rooms: rooms, bans: bans, now: time.Now}
}

// issue returns a grant for a guest just admitted to room as identity, or ""
// when there is no live meeting to bind it to.
func (g *rejoiner) issue(ctx context.Context, room store.Room, identity, name string) string {
	if g == nil {
		return ""
	}
	meeting, ok := g.meeting(ctx, room.Slug)
	if !ok {
		return ""
	}
	payload, err := json.Marshal(rejoinClaims{
		Room: room.ID, Meeting: meeting, Identity: identity, Name: name,
		Expires: g.now().Add(RejoinTTL).Unix(),
	})
	if err != nil {
		return ""
	}
	encoding := base64.RawURLEncoding
	return encoding.EncodeToString(payload) + "." + encoding.EncodeToString(g.sign(payload))
}

// redeem returns the identity and name to admit grant's holder to room as,
// and false for anything else: a malformed, forged, expired or other room's
// grant, a kicked participant's, or one whose meeting has ended. The
// signature and the cheap checks run before LiveKit is asked anything.
func (g *rejoiner) redeem(ctx context.Context, room store.Room, grant string) (string, string, bool) {
	if g == nil || grant == "" || len(grant) > maxRejoinGrant {
		return "", "", false
	}
	encodedPayload, encodedSignature, found := strings.Cut(grant, ".")
	if !found {
		return "", "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return "", "", false
	}
	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if err != nil || !hmac.Equal(signature, g.sign(payload)) {
		return "", "", false
	}
	var claims rejoinClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", "", false
	}
	if claims.Room != room.ID || claims.Identity == "" || claims.Meeting == "" ||
		!g.now().Before(time.Unix(claims.Expires, 0)) {
		return "", "", false
	}
	if g.bans.Banned(room.Slug, claims.Identity) {
		return "", "", false
	}
	meeting, ok := g.meeting(ctx, room.Slug)
	if !ok || meeting != claims.Meeting {
		return "", "", false
	}
	return claims.Identity, claims.Name, true
}

func (g *rejoiner) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, g.key)
	mac.Write([]byte(rejoinDomain))
	mac.Write(payload)
	return mac.Sum(nil)
}

// meeting returns the session id of the meeting running in slug: a LiveKit
// room with somebody in it. A room every participant has left is ending,
// and a new meeting there gets a new session id.
func (g *rejoiner) meeting(ctx context.Context, slug string) (string, bool) {
	response, err := g.rooms.ListRooms(ctx, &protocol.ListRoomsRequest{Names: []string{slug}})
	if err != nil {
		log.Printf("lobby: look up the meeting in %q: %v", slug, err)
		return "", false
	}
	for _, room := range response.GetRooms() {
		if room.GetName() == slug && room.GetSid() != "" && room.GetNumParticipants() > 0 {
			return room.GetSid(), true
		}
	}
	return "", false
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

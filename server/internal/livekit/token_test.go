package livekit

import (
	"reflect"
	"testing"
	"time"

	protocolauth "github.com/livekit/protocol/auth"

	"tide/internal/config"
)

const testSecret = "test-livekit-secret-with-enough-bytes"

// A token carries exactly the grants tide means it to, for each role. The
// media server trusts these and nothing else (ARCHITECTURE.md §4).
func TestMintTokenGrants(t *testing.T) {
	minter := NewMinter(config.Config{
		LiveKitAPIKey: "devkey", LiveKitAPISecret: testSecret, LiveKitPublicURL: "ws://public.example",
	})
	if minter.PublicURL() != "ws://public.example" {
		t.Fatalf("PublicURL = %q", minter.PublicURL())
	}
	yes, no := true, false
	everySource := []string{"camera", "microphone", "screen_share", "screen_share_audio"}
	roomGrant := func(grant protocolauth.VideoGrant) *protocolauth.VideoGrant {
		grant.RoomJoin, grant.Room, grant.CanSubscribe = true, "calm-otter-412", &yes
		return &grant
	}
	tests := []struct {
		name     string
		role     Role
		metadata string
		kind     string
		video    *protocolauth.VideoGrant
	}{
		{name: "guest", role: RoleGuest, video: roomGrant(protocolauth.VideoGrant{
			CanPublish: &yes, CanPublishSources: everySource,
		})},
		{name: "host", role: RoleHost, metadata: `{"role":"host"}`, video: roomGrant(protocolauth.VideoGrant{
			RoomAdmin: true, CanPublish: &yes, CanPublishSources: everySource,
		})},
		{name: "agent", role: RoleAgent, metadata: `{"role":"agent"}`, kind: "agent", video: roomGrant(protocolauth.VideoGrant{
			CanPublish: &yes, CanPublishData: &yes,
			CanPublishSources: []string{"microphone", "screen_share", "screen_share_audio"},
		})},
		{name: "observer", role: RoleObserver, kind: "agent", video: roomGrant(protocolauth.VideoGrant{
			CanPublish: &no, CanPublishData: &no, Hidden: true,
		})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			token, err := minter.MintToken("someone:1", "Ada", "calm-otter-412", test.role, 7*time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			verifier, err := protocolauth.ParseAPIToken(token)
			if err != nil {
				t.Fatal(err)
			}
			if verifier.APIKey() != "devkey" {
				t.Fatalf("API key = %q", verifier.APIKey())
			}
			registered, claims, err := verifier.Verify(testSecret)
			if err != nil {
				t.Fatal(err)
			}
			if ttl := registered.ExpiresAt.Sub(registered.NotBefore.Time); ttl != 7*time.Minute {
				t.Fatalf("token lives %s, want 7m", ttl)
			}
			want := &protocolauth.ClaimGrants{
				Identity: "someone:1", Name: "Ada", Metadata: test.metadata, Kind: test.kind, Video: test.video,
			}
			if !reflect.DeepEqual(claims, want) {
				t.Fatalf("grants\n%+v %+v\nwant\n%+v %+v", claims, claims.Video, want, want.Video)
			}
		})
	}
}

// Only a Role tide defines gets a token at all.
func TestMintTokenRefusesUnknownRole(t *testing.T) {
	minter := NewMinter(config.Config{LiveKitAPIKey: "devkey", LiveKitAPISecret: testSecret})
	if _, err := minter.MintToken("someone:1", "Ada", "room", Role(99), time.Minute); err == nil {
		t.Fatal("minted a token for an unknown role")
	}
}

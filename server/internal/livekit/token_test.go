package livekit

import (
	"reflect"
	"testing"
	"time"

	protocolauth "github.com/livekit/protocol/auth"

	"tide/internal/config"
)

const testSecret = "test-livekit-secret-with-enough-bytes"

// A token carries exactly the grants tide means it to: the room, the four
// sources a participant publishes, and admin with the host role only for a
// host. The media server trusts these and nothing else (ARCHITECTURE.md §4).
func TestMintTokenGrants(t *testing.T) {
	minter := NewMinter(config.Config{
		LiveKitAPIKey: "devkey", LiveKitAPISecret: testSecret, LiveKitPublicURL: "ws://public.example",
	})
	if minter.PublicURL() != "ws://public.example" {
		t.Fatalf("PublicURL = %q", minter.PublicURL())
	}
	for _, host := range []bool{false, true} {
		token, err := minter.MintToken("someone:1", "Ada", "calm-otter-412", host, 7*time.Minute)
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
			t.Fatalf("host=%v: token lives %s, want 7m", host, ttl)
		}
		yes := true
		want := &protocolauth.ClaimGrants{
			Identity: "someone:1", Name: "Ada",
			Video: &protocolauth.VideoGrant{
				RoomJoin: true, Room: "calm-otter-412", RoomAdmin: host,
				CanPublish: &yes, CanSubscribe: &yes,
				CanPublishSources: []string{"camera", "microphone", "screen_share", "screen_share_audio"},
			},
		}
		if host {
			want.Metadata = `{"role":"host"}`
		}
		if !reflect.DeepEqual(claims, want) {
			t.Fatalf("host=%v: grants\n%+v %+v\nwant\n%+v %+v", host, claims, claims.Video, want, want.Video)
		}
	}
}

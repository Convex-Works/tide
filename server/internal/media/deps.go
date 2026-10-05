package media

// The media track builds on these; importing them here keeps go.mod's
// requirements in place for every track from the contract on.
import (
	_ "github.com/alicebob/miniredis/v2"
	_ "github.com/livekit/livekit-server/pkg/service"
)

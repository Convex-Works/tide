package machines

import (
	"testing"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	"tide/internal/api"
)

// The page shows one of the api.Machine* states whatever moil reports, and a
// state from a newer moil as a connected machine that isn't free: busy. (The
// states moil has today are checked end to end, with machines over moil, in
// internal/httpapi.)
func TestEveryMoilStateIsOneThePageKnows(t *testing.T) {
	for state, want := range map[moil.MachineState]string{
		moil.Idle:     api.MachineIdle,
		moil.Busy:     api.MachineBusy,
		moil.Paused:   api.MachinePaused,
		moil.Offline:  api.MachineOffline,
		"updating":    api.MachineBusy,
		"hibernating": api.MachineBusy,
	} {
		if got := machineState(state); got != want {
			t.Errorf("machineState(%q) = %q, want %q", state, got, want)
		}
	}
}

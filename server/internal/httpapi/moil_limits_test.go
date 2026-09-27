package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"

	"klisi/internal/auth"
	"klisi/internal/transcripts"
)

// klisi keeps at most 4 MiB of a job's data events: a machine that sends
// more has its attempt stopped and its job failed, rather than making klisi
// hold what it sends.
func TestAJobKeepsAtMost4MiBOfData(t *testing.T) {
	k := startKlisi(t, nil)
	alice := k.signIn(auth.Session{Sub: "alice"})
	bundle, err := transcripts.Bundle()
	if err != nil {
		t.Fatal(err)
	}
	m := alice.pair()
	m.Approve(bundle)
	m.Connect()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run, err := k.moil.Submit(ctx, moil.Job{ID: "chatty", Bundle: bundle, Eligible: moil.OwnedBy("alice")})
	if err != nil {
		t.Fatal(err)
	}
	a := m.NextAttempt()
	// Each event counts its payload and 256 bytes: 20 of these fit in
	// 4 MiB, 21 don't.
	chunk := strings.Repeat("a", 200<<10)
	for range 20 {
		a.Data(chunk)
	}
	m.Sync()
	if state := run.State(); state != moil.Running {
		t.Fatalf("after 20 data events the job is %s", state)
	}
	a.Data(chunk)
	if _, err := run.Wait(ctx); !errors.Is(err, moil.ErrTooMuchData) {
		t.Fatalf("after 21 data events: %v, want ErrTooMuchData", err)
	}
	a.WaitCancelled()
}

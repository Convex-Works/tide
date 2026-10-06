package machines

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"
	"git.convex.works/ConvexWorks/moil/sdk/go/moiltest"

	"tide/internal/auth"
	"tide/internal/transcripts"
)

// A host confirming a pairing doesn't wait for another host's confirmation:
// each host's confirmations are serialized, to keep to their limit, but
// no one else's.
func TestHostsConfirmPairingsWithoutWaitingForEachOther(t *testing.T) {
	machines := &slowStore{MemoryStore: moil.NewMemoryStore(), slow: "alice", entered: make(chan struct{}, 1), release: make(chan struct{})}
	h, moilURL := serveMoil(t, machines)
	aliceCode := moiltest.New(t, moilURL).StartPairing()
	bobCode := moiltest.New(t, moilURL).StartPairing()

	// Alice's confirmation waits for her machines to load...
	alice := make(chan int, 1)
	go func() { alice <- confirm(h, "alice", aliceCode) }()
	select {
	case <-machines.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("alice's confirmation never loaded her machines")
	}
	// ...and Bob's goes through meanwhile.
	bob := make(chan int, 1)
	go func() { bob <- confirm(h, "bob", bobCode) }()
	select {
	case status := <-bob:
		if status != http.StatusCreated {
			t.Fatalf("bob's confirmation = %d", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bob's confirmation waited for alice's")
	}
	close(machines.release)
	if status := <-alice; status != http.StatusCreated {
		t.Fatalf("alice's confirmation = %d", status)
	}
	h.confirming.mu.Lock()
	defer h.confirming.mu.Unlock()
	if n := len(h.confirming.locks); n != 0 {
		t.Fatalf("%d hosts' locks kept after their confirmations", n)
	}
}

// slowStore is a moil store whose owner slow's machines load only once
// release is closed.
type slowStore struct {
	*moil.MemoryStore
	slow    string
	entered chan struct{}
	release chan struct{}
}

func (s *slowStore) Machines(ctx context.Context, owner string) ([]moil.MachineRecord, error) {
	if owner == s.slow {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.MemoryStore.Machines(ctx, owner)
}

// serveMoil serves a moil server with the transcribe bundle over HTTP, as
// tide does under /moil, and returns the machines handler beside it and
// the moil base URL.
func serveMoil(t *testing.T, store moil.Store) (*Handler, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + listener.Addr().String()
	server, err := moil.NewServer(moil.Config{Name: "tide", VerificationURL: base + "/machines", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := transcripts.Bundle()
	if err != nil {
		t.Fatal(err)
	}
	server.AddBundle(bundle)
	web := &http.Server{Handler: http.StripPrefix("/moil", server.Handler())}
	go func() { _ = web.Serve(listener) }()
	t.Cleanup(func() {
		_ = web.Close()
		_ = server.Close()
	})
	return NewHandler(server, bundle, base+"/moil"), base + "/moil"
}

// confirm confirms a pairing code as host, and returns the status.
func confirm(h *Handler, host, code string) int {
	r := httptest.NewRequest(http.MethodPost, "/api/machines/pairings/"+code+"/confirm", nil)
	r.SetPathValue("code", code)
	r = r.WithContext(auth.WithSession(r.Context(), auth.Session{Sub: host}))
	response := httptest.NewRecorder()
	h.Confirm(response, r)
	return response.Code
}

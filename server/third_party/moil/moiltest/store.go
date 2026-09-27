package moiltest

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"
)

// TestStore checks a moil.Store implementation against the contract the
// Server relies on. newStore must return an empty store each time it's
// called. Run it from a service's own tests:
//
//	func TestStore(t *testing.T) {
//		moiltest.TestStore(t, func(t *testing.T) moil.Store { return newSQLStore(t) })
//	}
func TestStore(t *testing.T, newStore func(t *testing.T) moil.Store) {
	ctx := context.Background()
	paired := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	record := func(id, owner string, age time.Duration) moil.MachineRecord {
		return moil.MachineRecord{
			ID:        id,
			Owner:     owner,
			TokenHash: fmt.Sprintf("%x", sha256.Sum256([]byte("token of "+id))),
			PairedAt:  paired.Add(-age),
			Report:    moil.MachineReport{Name: id + " name", OS: "linux", Arch: "x86_64", AppVersion: "0.1.0"},
		}
	}

	t.Run("a machine reads back as added, by ID and by token hash", func(t *testing.T) {
		s := newStore(t)
		want := record("m_1", "alice", 0)
		want.TokenHash = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
		mustStore(t, s.AddMachine(ctx, want))
		got, err := s.Machine(ctx, "m_1")
		mustStore(t, err)
		checkRecord(t, got, want)
		got, err = s.MachineByToken(ctx, want.TokenHash)
		mustStore(t, err)
		checkRecord(t, got, want)
	})

	t.Run("unknown machines and tokens are ErrNotFound", func(t *testing.T) {
		s := newStore(t)
		mustStore(t, s.AddMachine(ctx, record("m_1", "alice", 0)))
		if _, err := s.Machine(ctx, "m_2"); !errors.Is(err, moil.ErrNotFound) {
			t.Errorf("Machine(unknown) = %v, want ErrNotFound", err)
		}
		if _, err := s.MachineByToken(ctx, "0000000000000000000000000000000000000000000000000000000000000000"); !errors.Is(err, moil.ErrNotFound) {
			t.Errorf("MachineByToken(unknown) = %v, want ErrNotFound", err)
		}
	})

	t.Run("IDs are unique", func(t *testing.T) {
		s := newStore(t)
		mustStore(t, s.AddMachine(ctx, record("m_1", "alice", 0)))
		if err := s.AddMachine(ctx, record("m_1", "bob", 0)); err == nil {
			t.Error("adding a second machine with the same ID succeeded")
		}
		got, err := s.Machine(ctx, "m_1")
		mustStore(t, err)
		if got.Owner != "alice" {
			t.Errorf("the duplicate replaced the original: owner %q", got.Owner)
		}
	})

	t.Run("Machines lists one owner's machines, oldest first", func(t *testing.T) {
		s := newStore(t)
		mustStore(t, s.AddMachine(ctx, record("m_new", "alice", 0)))
		mustStore(t, s.AddMachine(ctx, record("m_bob", "bob", time.Hour)))
		mustStore(t, s.AddMachine(ctx, record("m_old", "alice", 2*time.Hour)))
		got, err := s.Machines(ctx, "alice")
		mustStore(t, err)
		var ids []string
		for _, m := range got {
			ids = append(ids, m.ID)
		}
		if !slices.Equal(ids, []string{"m_old", "m_new"}) {
			t.Errorf("Machines(alice) = %v, want [m_old m_new]", ids)
		}
		if got, err := s.Machines(ctx, "carol"); err != nil || len(got) != 0 {
			t.Errorf("Machines(carol) = %v, %v; want none", got, err)
		}
	})

	t.Run("SaveReport replaces the report and nothing else", func(t *testing.T) {
		s := newStore(t)
		want := record("m_1", "alice", 0)
		mustStore(t, s.AddMachine(ctx, want))
		want.Report = moil.MachineReport{
			Name: "renamed", OS: "macos", Arch: "aarch64", AppVersion: "0.2.0",
			CPUs: 12, MemoryBytes: 38654705664,
			GPUs:     []moil.GPU{{Name: "Apple M3 Pro"}, {Name: "eGPU", MemoryBytes: 17179869184}},
			Approved: []string{"a00a65c166291e905da7546a67518a81cc27cb498f009c75b824f28a8adfffb2"},
			LastSeen: paired.Add(time.Minute),
		}
		mustStore(t, s.SaveReport(ctx, "m_1", want.Report))
		got, err := s.Machine(ctx, "m_1")
		mustStore(t, err)
		checkRecord(t, got, want)
	})

	t.Run("removing a machine forgets it and its token for good", func(t *testing.T) {
		s := newStore(t)
		rec := record("m_1", "alice", 0)
		mustStore(t, s.AddMachine(ctx, rec))
		mustStore(t, s.RemoveMachine(ctx, "m_1"))
		if _, err := s.Machine(ctx, "m_1"); !errors.Is(err, moil.ErrNotFound) {
			t.Errorf("Machine after RemoveMachine = %v, want ErrNotFound", err)
		}
		if _, err := s.MachineByToken(ctx, rec.TokenHash); !errors.Is(err, moil.ErrNotFound) {
			t.Errorf("MachineByToken after RemoveMachine = %v, want ErrNotFound", err)
		}
		if err := s.RemoveMachine(ctx, "m_1"); !errors.Is(err, moil.ErrNotFound) {
			t.Errorf("removing twice = %v, want ErrNotFound", err)
		}
		// A report arriving after removal must not bring the machine, and
		// its token, back.
		if err := s.SaveReport(ctx, "m_1", rec.Report); !errors.Is(err, moil.ErrNotFound) {
			t.Errorf("SaveReport after RemoveMachine = %v, want ErrNotFound", err)
		}
		if _, err := s.MachineByToken(ctx, rec.TokenHash); !errors.Is(err, moil.ErrNotFound) {
			t.Error("SaveReport recreated a removed machine")
		}
	})

	t.Run("records returned don't share memory with the store", func(t *testing.T) {
		s := newStore(t)
		rec := record("m_1", "alice", 0)
		rec.Report.Approved = []string{"a00a65c166291e905da7546a67518a81cc27cb498f009c75b824f28a8adfffb2"}
		mustStore(t, s.AddMachine(ctx, rec))
		rec.Report.Approved[0] = "changed after AddMachine"
		got, err := s.Machine(ctx, "m_1")
		mustStore(t, err)
		got.Report.Approved[0] = "changed after Machine"
		again, err := s.Machine(ctx, "m_1")
		mustStore(t, err)
		if again.Report.Approved[0] != "a00a65c166291e905da7546a67518a81cc27cb498f009c75b824f28a8adfffb2" {
			t.Errorf("the stored record changed through a caller's slice: %q", again.Report.Approved[0])
		}
	})

	t.Run("concurrent use is safe", func(t *testing.T) {
		s := newStore(t)
		for i := range 4 {
			mustStore(t, s.AddMachine(ctx, record(fmt.Sprintf("m_%d", i), "alice", time.Duration(i)*time.Minute)))
		}
		var wg sync.WaitGroup
		for i := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id := fmt.Sprintf("m_%d", i)
				for j := range 20 {
					if err := s.SaveReport(ctx, id, moil.MachineReport{Name: fmt.Sprint(j)}); err != nil {
						t.Error(err)
						return
					}
					if _, err := s.Machines(ctx, "alice"); err != nil {
						t.Error(err)
						return
					}
				}
			}()
		}
		wg.Wait()
		got, err := s.Machines(ctx, "alice")
		mustStore(t, err)
		for _, m := range got {
			if m.Report.Name != "19" {
				t.Errorf("%s's last report is %q, want 19", m.ID, m.Report.Name)
			}
		}
	})
}

func mustStore(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func checkRecord(t *testing.T, got, want moil.MachineRecord) {
	t.Helper()
	g, w := got.Report, want.Report
	same := got.ID == want.ID && got.Owner == want.Owner && got.TokenHash == want.TokenHash &&
		got.PairedAt.Equal(want.PairedAt) &&
		g.Name == w.Name && g.OS == w.OS && g.Arch == w.Arch && g.AppVersion == w.AppVersion &&
		g.CPUs == w.CPUs && g.MemoryBytes == w.MemoryBytes &&
		slices.Equal(g.GPUs, w.GPUs) && slices.Equal(g.Approved, w.Approved) &&
		g.LastSeen.Equal(w.LastSeen)
	if !same {
		t.Errorf("record differs\n got: %+v\nwant: %+v", got, want)
	}
}

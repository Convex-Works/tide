package store

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"
	"git.convex.works/ConvexWorks/moil/sdk/go/moiltest"
)

// The moil SDK's own contract for a Store, which the moil.Server relies on.
func TestMachinesSatisfyMoilStoreContract(t *testing.T) {
	moiltest.TestStore(t, func(t *testing.T) moil.Store {
		db, err := Open(filepath.Join(t.TempDir(), "tide.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	})
}

// What a machine reported survives a restart: the server reopens the same
// database and still knows which bundles each machine's owner approved.
func TestMachineReportSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tide.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	paired := time.Date(2026, 9, 27, 9, 30, 0, 0, time.UTC)
	record := moil.MachineRecord{
		ID: "m_studio", Owner: "alice", TokenHash: "ab12", PairedAt: paired,
		Report: moil.MachineReport{Name: "Studio", OS: "macos", Arch: "aarch64", AppVersion: "0.1.0"},
	}
	if err := db.AddMachine(ctx, record); err != nil {
		t.Fatal(err)
	}
	report := moil.MachineReport{
		Name: "Studio", OS: "macos", Arch: "aarch64", AppVersion: "0.1.0",
		CPUs: 12, MemoryBytes: 64 << 30,
		GPUs:     []moil.GPU{{Name: "Apple M3 Max", MemoryBytes: 48 << 30}},
		Approved: []string{"42d30e3fb23030627d993330b1e2917a0ac2f4abef567fc6e82328d8acedeae6"},
		LastSeen: paired.Add(90 * time.Minute).Add(123 * time.Millisecond),
	}
	if err := db.SaveReport(ctx, record.ID, report); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	got, err := db.MachineByToken(ctx, "ab12")
	if err != nil {
		t.Fatal(err)
	}
	if !got.PairedAt.Equal(paired) || got.Owner != "alice" || got.Report.CPUs != 12 ||
		!slices.Equal(got.Report.GPUs, report.GPUs) || !slices.Equal(got.Report.Approved, report.Approved) ||
		!got.Report.LastSeen.Equal(report.LastSeen) {
		t.Fatalf("machine after reopen = %+v, want report %+v", got, report)
	}
}

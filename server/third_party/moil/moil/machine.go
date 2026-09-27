package moil

import (
	"context"
	"errors"
	"slices"
	"time"
)

// A Machine is a paired machine as the service sees it: who owns it, what
// it last reported about itself and, while it's connected, its state.
type Machine struct {
	// ID is the service's identifier for the machine, chosen at pairing.
	ID string
	// Owner is the service's identifier for the user who paired the
	// machine, as passed to ConfirmPairing. moil never interprets it.
	Owner    string
	PairedAt time.Time
	// MachineReport is what the machine last said about itself. It is
	// kept by the Store, so it outlives the connection.
	MachineReport
	// State is Offline unless the machine is connected.
	State MachineState
}

// Online reports whether the machine has a control channel open.
func (m Machine) Online() bool { return m.State != Offline && m.State != "" }

// HasApproved reports whether the machine last reported the bundle hash as
// approved by its owner.
func (m Machine) HasApproved(hash string) bool { return slices.Contains(m.Approved, hash) }

// MachineReport is what a machine says about itself: its name and hardware
// in hello, and its owner's approvals in hello and approved (spec §6.2).
// Pairing seeds the name, OS, architecture and app version.
type MachineReport struct {
	Name        string
	OS          string // e.g. macos, linux, windows
	Arch        string // e.g. aarch64, x86_64
	AppVersion  string
	CPUs        int
	MemoryBytes int64
	GPUs        []GPU
	Approved    []string  // bundle hashes the owner approved for this service
	LastSeen    time.Time // when the machine last connected, reported or disconnected
}

func (r MachineReport) clone() MachineReport {
	r.GPUs = slices.Clone(r.GPUs)
	r.Approved = slices.Clone(r.Approved)
	return r
}

// A GPU is one of a machine's GPUs. MemoryBytes is 0 when the machine
// didn't say.
type GPU struct {
	Name        string
	MemoryBytes int64
}

// MachineState is what a machine is doing, as it reports it.
type MachineState string

// Machine states. Idle, Busy and Paused are reported by a connected
// machine; Offline means it has no control channel open.
const (
	Idle    MachineState = "idle"    // can take a job
	Busy    MachineState = "busy"    // running a job, for this service or another
	Paused  MachineState = "paused"  // its owner paused it
	Offline MachineState = "offline" // not connected
)

// AnyMachine is an eligibility policy that allows every paired machine.
// Use it only for jobs whose inputs any machine owner may see.
func AnyMachine(Machine) bool { return true }

// OwnedBy returns an eligibility policy that allows only the machines
// paired by owner.
func OwnedBy(owner string) func(Machine) bool {
	return func(m Machine) bool { return m.Owner == owner }
}

// ErrNotFound is returned by a Store, and by the Server, for a machine that
// doesn't exist.
var ErrNotFound = errors.New("moil: not found")

// A Store keeps paired machines. It never sees tokens, only their SHA-256.
// The Server calls it from many goroutines at once.
//
// Use NewMemoryStore for tests, NewFileStore for small deployments, or
// implement it over the service's own database; moiltest.TestStore checks
// an implementation against this contract.
type Store interface {
	// AddMachine records a newly paired machine. IDs are unique.
	AddMachine(ctx context.Context, m MachineRecord) error
	// Machine returns the machine with the given ID, or ErrNotFound.
	Machine(ctx context.Context, id string) (MachineRecord, error)
	// MachineByToken returns the machine whose token has the given
	// hash, or ErrNotFound.
	MachineByToken(ctx context.Context, tokenHash string) (MachineRecord, error)
	// Machines returns the machines paired by owner, oldest first.
	Machines(ctx context.Context, owner string) ([]MachineRecord, error)
	// SaveReport replaces what the machine last reported about itself. It
	// returns ErrNotFound, and must not recreate the machine, if the
	// machine was removed.
	SaveReport(ctx context.Context, id string, r MachineReport) error
	// RemoveMachine forgets a machine, so its token stops working. It
	// returns ErrNotFound if there's no such machine.
	RemoveMachine(ctx context.Context, id string) error
}

// A MachineRecord is what a Store keeps about a paired machine.
type MachineRecord struct {
	ID    string
	Owner string
	// TokenHash is the hex SHA-256 of the machine's token. The token
	// itself is never stored.
	TokenHash string
	PairedAt  time.Time
	Report    MachineReport
}

func (r MachineRecord) clone() MachineRecord {
	r.Report = r.Report.clone()
	return r
}

func (r MachineRecord) machine() Machine {
	return Machine{ID: r.ID, Owner: r.Owner, PairedAt: r.PairedAt, MachineReport: r.Report.clone(), State: Offline}
}

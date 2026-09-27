package moil

import (
	"cmp"
	"context"
	"iter"
	"slices"
	"sync"
)

// A Run is a submitted job. Its methods are safe to call from any
// goroutine.
//
// Runs live in memory: when the service restarts, unfinished jobs are
// lost, and machines abandon the attempts they were running for them. A
// service that must finish a job resubmits it, by the same ID, on startup.
//
// A run keeps its events, so that every reader can replay them, within
// bounds a machine can't push it past:
//
//   - A progress event replaces the one before it, if nothing came between
//     them: progress is state, and only the latest matters.
//   - Log and phase events count 256 bytes plus their text toward 1 MiB.
//     Once that's full, later ones are dropped, and a warning says so.
//   - Data events count 256 bytes plus their payload toward
//     Config.MaxDataBytes. An attempt that sends more is stopped, and the
//     job fails with ErrTooMuchData.
type Run struct {
	id     string
	cancel func()

	mu        sync.Mutex
	state     RunState
	history   []entry // the events kept, in order
	seq       uint64  // the sequence number of the last event
	logBytes  int     // what the log and phase events kept count
	logFull   bool
	dataBytes int // what the data events kept count
	maxData   int
	wake      chan struct{} // closed and replaced when something changes
	done      chan struct{} // closed when the run ends
	ended     bool
	result    *Result
	err       error
}

// An entry is an event a run keeps, numbered in the order it came.
type entry struct {
	seq uint64
	ev  Event
}

const (
	// eventCost is what a kept event counts besides its text or payload.
	eventCost = 256
	// maxLogBytes bounds what a run's log and phase events count.
	maxLogBytes = 1 << 20
)

func newRun(id string, cancel func(), maxData int) *Run {
	return &Run{id: id, cancel: cancel, maxData: maxData, state: Queued, wake: make(chan struct{}), done: make(chan struct{})}
}

// ID is the job's ID.
func (r *Run) ID() string { return r.id }

// State reports where the run is now.
func (r *Run) State() RunState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// Events returns the run's events in order, from the first it keeps, then
// each new one as it happens. The sequence ends after the run ends and
// every event has been yielded, or when ctx is done. Each call starts from
// the first event. The Server never waits for readers: a slow reader only
// delays itself, and misses only progress that newer progress replaced.
func (r *Run) Events(ctx context.Context) iter.Seq[Event] {
	return func(yield func(Event) bool) {
		var last uint64 // the sequence number of the last event yielded
		for {
			r.mu.Lock()
			i, _ := slices.BinarySearchFunc(r.history, last+1, func(e entry, seq uint64) int { return cmp.Compare(e.seq, seq) })
			// A copy: the last entry changes when progress replaces it.
			pending := slices.Clone(r.history[i:])
			ended, wake := r.ended, r.wake
			r.mu.Unlock()
			for _, e := range pending {
				if !yield(e.ev) {
					return
				}
				last = e.seq
			}
			if len(pending) > 0 {
				continue
			}
			if ended {
				return
			}
			select {
			case <-wake:
			case <-ctx.Done():
				return
			}
		}
	}
}

// Done is closed when the run ends.
func (r *Run) Done() <-chan struct{} { return r.done }

// Wait blocks until the run ends or ctx is done. A succeeded job returns
// its Result. A failed one returns a *JobError, the error from
// Job.Prepare, or an error wrapping ErrTooMuchData or ErrBundleRemoved;
// one cancelled with Cancel ErrCancelled, and one the Server gave up on
// when closing ErrClosed. A machine can't end a run as cancelled: an
// attempt it reports cancelled unasked is a failure (see ErrCancelled).
func (r *Run) Wait(ctx context.Context) (*Result, error) {
	select {
	case <-r.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result, r.err
}

// Cancel asks for the job to stop. A job that is waiting for a machine
// ends at once; a running one ends when its machine confirms, or at once
// if the machine is offline. Either way Wait returns ErrCancelled, even if
// the attempt failed before its machine saw the request; only an attempt
// that succeeded first ends the run with its Result. Cancelling an ended
// run does nothing.
func (r *Run) Cancel() { r.cancel() }

// The methods below are called by the scheduler.

// emit keeps an event, within the run's bounds, and wakes the readers. It
// reports false, keeping nothing, for a data event over the run's budget.
func (r *Run) emit(ev Event) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch ev.Kind {
	case EventProgress:
		if n := len(r.history); n > 0 && r.history[n-1].ev.Kind == EventProgress && r.history[n-1].ev.Attempt == ev.Attempt {
			r.history = r.history[:n-1]
		}
	case EventLog, EventPhase:
		if r.logFull {
			return true
		}
		cost := eventCost + len(ev.Message) + len(ev.Level) + len(ev.Phase)
		if r.logBytes+cost <= maxLogBytes {
			r.logBytes += cost
			break
		}
		r.logFull = true
		ev = Event{Kind: EventLog, Time: ev.Time, Attempt: ev.Attempt, Machine: ev.Machine, Level: "warn",
			Message: "moil: this run's log is full; later log and phase events are dropped"}
	case EventData:
		cost := eventCost + len(ev.Data)
		if r.dataBytes+cost > r.maxData {
			return false
		}
		r.dataBytes += cost
	}
	r.seq++
	r.history = append(r.history, entry{seq: r.seq, ev: ev})
	close(r.wake)
	r.wake = make(chan struct{})
	return true
}

func (r *Run) setState(s RunState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state = s
}

func (r *Run) finish(s RunState, res *Result, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state, r.result, r.err, r.ended = s, res, err, true
	close(r.wake)
	r.wake = make(chan struct{})
	close(r.done)
}

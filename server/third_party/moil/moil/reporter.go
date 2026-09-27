package moil

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// A reporter saves what machines report about themselves to the Store,
// off the scheduler's goroutine. It keeps only each machine's latest
// report, so a machine reporting faster than the Store saves can't make
// it hold more than one.
type reporter struct {
	store Store
	log   *slog.Logger

	mu      sync.Mutex
	pending map[string]MachineReport
	order   []string // machines with a pending report, oldest first
	closed  bool
	wake    chan struct{} // holds a token when there's something to do

	// ctx ends the writes in flight when close gives up on them.
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed when run returns
}

// reportTimeout bounds one save.
const reportTimeout = 10 * time.Second

func newReporter(store Store, log *slog.Logger) *reporter {
	r := &reporter{store: store, log: log, pending: map[string]MachineReport{}, wake: make(chan struct{}, 1), done: make(chan struct{})}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	go r.run()
	return r
}

// save queues a machine's report, replacing one of the machine's that is
// still waiting.
func (r *reporter) save(id string, rep MachineReport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if _, waiting := r.pending[id]; !waiting {
		r.order = append(r.order, id)
	}
	r.pending[id] = rep
	r.signal()
}

func (r *reporter) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *reporter) run() {
	defer close(r.done)
	for {
		id, rep, ok := r.next()
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.ctx, reportTimeout)
		err := r.store.SaveReport(ctx, id, rep)
		cancel()
		if err != nil && !errors.Is(err, ErrNotFound) {
			r.log.Error("moil: saving a machine's report", "machine", id, "err", err)
		}
	}
}

// next waits for the oldest pending report. It reports false once the
// reporter is closed and has saved everything, or given up.
func (r *reporter) next() (string, MachineReport, bool) {
	for {
		r.mu.Lock()
		if len(r.order) > 0 && r.ctx.Err() == nil {
			id := r.order[0]
			r.order = r.order[1:]
			rep := r.pending[id]
			delete(r.pending, id)
			r.mu.Unlock()
			return id, rep, true
		}
		closed := r.closed
		r.mu.Unlock()
		if closed {
			return "", MachineReport{}, false
		}
		<-r.wake
	}
}

// close stops taking reports and saves those pending, giving the Store
// up to grace to do so, then gives up on the rest.
func (r *reporter) close(grace time.Duration) {
	r.mu.Lock()
	r.closed = true
	r.signal()
	r.mu.Unlock()
	giveUp := time.AfterFunc(grace, r.cancel)
	<-r.done
	giveUp.Stop()
	r.cancel()
}

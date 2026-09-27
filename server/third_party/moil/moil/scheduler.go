package moil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/internal/wire"
)

// The scheduler holds everything about connected machines and jobs. Only
// the Server's loop goroutine touches it, so none of it needs locks; timers
// and connections reach it through Server.post.
type scheduler struct {
	s      *Server
	cfg    *Config
	log    *slog.Logger
	closed bool
	// dirty says something changed that could let a queued job run.
	dirty bool
	// gen numbers the changes to connected machines (see liveMachine.gen).
	gen uint64

	bundles      []*Bundle
	bundleByHash map[string]*Bundle

	machines map[string]*liveMachine // connected machines by ID
	removed  map[string]bool         // machines removed while this Server ran

	jobs      map[string]*jobState // known jobs by ID, finished ones included
	queue     []*jobState          // unfinished jobs, in submission order
	running   map[string]*jobState // machine ID → the job whose current attempt it holds
	preparing map[string]*jobState // machine ID → the job whose next attempt Job.Prepare is making for it
}

// A liveMachine is a machine with an open control channel.
type liveMachine struct {
	sess     *session
	m        Machine
	approved map[string]bool
	// gen changes whenever something about the machine changes that
	// could change its answer to an offer: it connects, reports a state
	// or approvals. A job isn't offered again to a machine that declined
	// it until gen changes, so a machine can't make the scheduler loop.
	gen uint64
	// offer is the job whose offer to this machine is open, if any.
	offer *jobState
}

type jobPhase int

const (
	phaseQueued    jobPhase = iota // waiting for a machine
	phaseOffering                  // offers are out
	phasePreparing                 // Job.Prepare is making the next attempt's files
	phaseRunning                   // an attempt is assigned
	phaseDone
)

// jobState is the scheduler's record of a job.
type jobState struct {
	id          string
	run         *Run
	bundle      *Bundle
	title       string
	params      json.RawMessage
	inputs      map[string]wire.Download
	outputs     map[string]wire.Upload
	eligible    func(Machine) bool
	prefer      func(Machine) int
	prepare     func(context.Context, Assignment) (map[string]Download, map[string]Upload, error)
	timeout     time.Duration
	maxAttempts int

	phase    jobPhase
	attempts int // attempts assigned so far
	counted  int // attempts that count toward maxAttempts
	refused  int // attempts that never started: machines refused them, or backed out while Prepare ran
	round    *offerRound
	prep     *preparation
	cur      *attemptState
	// cancelled says the service asked to cancel the job.
	cancelled bool
	// skip maps a machine to its gen when it declined, ignored or refused
	// this job.
	skip map[string]uint64
	// failedOn holds the machines an attempt of this job failed on, or
	// that refused or backed out of one.
	failedOn map[string]bool
	forget   *time.Timer
}

// An offerRound is one set of offers for a job, answered by bids and
// declines.
type offerRound struct {
	answered map[string]bool // offered machine → whether it answered
	pending  int
	bids     []string // machines that bid, in order
	timer    *time.Timer
}

// A preparation is Job.Prepare making an attempt's files, while the
// machine that won the attempt is held for it.
type preparation struct {
	n       int
	machine string
	cancel  context.CancelFunc
}

type attemptState struct {
	n       int
	machine string
	outputs map[string]wire.Upload // the outputs the attempt may produce
	lastSeq int64
	// The lease runs out at deadline; timer checks it.
	deadline time.Time
	timer    *time.Timer
}

func newScheduler(s *Server) *scheduler {
	return &scheduler{
		s:            s,
		cfg:          &s.cfg,
		log:          s.log,
		bundleByHash: map[string]*Bundle{},
		machines:     map[string]*liveMachine{},
		removed:      map[string]bool{},
		jobs:         map[string]*jobState{},
		running:      map[string]*jobState{},
		preparing:    map[string]*jobState{},
	}
}

func (sc *scheduler) nextGen() uint64 {
	sc.gen++
	return sc.gen
}

// Bundles.

func (sc *scheduler) addBundles(bundles []*Bundle) {
	added := false
	for _, b := range bundles {
		if sc.bundleByHash[b.Hash()] != nil {
			continue
		}
		sc.bundles = append(sc.bundles, b)
		sc.bundleByHash[b.Hash()] = b
		added = true
		sc.log.Info("moil: bundle added", "bundle", b.String())
	}
	if !added {
		return
	}
	msg := wire.Bundles{Bundles: sc.bundleInfos()}
	for _, lm := range sc.machines {
		lm.sess.send(msg)
	}
}

func (sc *scheduler) removeBundles(bundles []*Bundle) {
	removed := false
	for _, b := range bundles {
		if b == nil || sc.bundleByHash[b.Hash()] == nil {
			continue
		}
		delete(sc.bundleByHash, b.Hash())
		sc.bundles = slices.DeleteFunc(sc.bundles, func(x *Bundle) bool { return x.Hash() == b.Hash() })
		removed = true
		sc.log.Info("moil: bundle removed", "bundle", b.String())
	}
	if !removed {
		return
	}
	msg := wire.Bundles{Bundles: sc.bundleInfos()}
	for _, lm := range sc.machines {
		lm.sess.send(msg)
	}
	for _, j := range slices.Clone(sc.queue) {
		if j.cur == nil && sc.bundleByHash[j.bundle.Hash()] == nil {
			sc.finish(j, Failed, nil, fmt.Errorf("%w: %s", ErrBundleRemoved, j.bundle))
		}
	}
}

func (sc *scheduler) bundleInfos() []wire.BundleInfo {
	infos := make([]wire.BundleInfo, len(sc.bundles))
	for i, b := range sc.bundles {
		infos[i] = b.info()
	}
	return infos
}

// Machines.

var errRemoved = errors.New("moil: machine removed")

// register takes a machine's new control channel after its hello, and
// queues the welcome on it.
func (sc *scheduler) register(sess *session, rec MachineRecord, hello wire.Hello) error {
	if sc.closed {
		return ErrClosed
	}
	id := rec.ID
	if sc.removed[id] {
		return errRemoved
	}
	if old := sc.machines[id]; old != nil {
		sc.log.Info("moil: machine reconnected; closing its previous connection", "machine", id)
		old.sess.close(wire.CloseReplaced, "replaced by a newer connection")
		sc.dropLive(old)
	}
	lm := &liveMachine{
		sess: sess,
		m: Machine{
			ID:       id,
			Owner:    rec.Owner,
			PairedAt: rec.PairedAt,
			MachineReport: MachineReport{
				Name:        hello.Machine.Name,
				OS:          hello.Machine.OS,
				Arch:        hello.Machine.Arch,
				AppVersion:  hello.AppVersion,
				CPUs:        hello.Machine.CPUs,
				MemoryBytes: hello.Machine.MemoryBytes,
				GPUs:        make([]GPU, len(hello.Machine.GPUs)),
				Approved:    slices.Clone(hello.Approved),
				LastSeen:    time.Now(),
			},
			State: MachineState(hello.State),
		},
		gen: sc.nextGen(),
	}
	for i, g := range hello.Machine.GPUs {
		lm.m.GPUs[i] = GPU{Name: g.Name, MemoryBytes: g.MemoryBytes}
	}
	lm.approved = setOf(hello.Approved)
	sc.machines[id] = lm
	sc.s.saveReport(id, lm.m.MachineReport.clone())

	// Resume the attempts both sides still hold; the ones the machine
	// forgot are lost.
	held := map[wire.AttemptRef]bool{}
	for _, a := range hello.Attempts {
		held[a] = true
	}
	resume := []wire.Resume{}
	var lost []*jobState
	if j := sc.running[id]; j != nil {
		if held[wire.AttemptRef{JobID: j.id, Attempt: j.cur.n}] {
			resume = append(resume, wire.Resume{JobID: j.id, Attempt: j.cur.n, Seq: j.cur.lastSeq})
		} else {
			lost = append(lost, j)
		}
	}
	sess.send(wire.Welcome{
		Protocol:  wire.Protocol,
		MachineID: id,
		Service:   wire.ServiceInfo{Name: sc.cfg.Name},
		Bundles:   sc.bundleInfos(),
		Resume:    resume,
	})
	sc.log.Info("moil: machine connected", "machine", id, "state", hello.State, "approved", len(hello.Approved), "resumed", len(resume))
	for _, j := range lost {
		sc.attemptFailed(j, &JobError{Code: CodeLost, Message: "the machine reconnected without the attempt", Retryable: true})
	}
	sc.dirty = true
	return nil
}

// disconnected handles the end of a control channel.
func (sc *scheduler) disconnected(sess *session) {
	lm := sc.machines[sess.machineID]
	if lm == nil || lm.sess != sess {
		return // replaced or removed already
	}
	sc.log.Info("moil: machine disconnected", "machine", lm.m.ID)
	lm.m.LastSeen = time.Now()
	sc.s.saveReport(lm.m.ID, lm.m.MachineReport.clone())
	sc.dropLive(lm)
	// A cancelled attempt ends when its machine goes away; there's nobody
	// left to confirm it. Other attempts keep their leases.
	if j := sc.running[lm.m.ID]; j != nil && j.cancelled {
		sc.finish(j, Cancelled, nil, ErrCancelled)
	}
}

// dropLive forgets a machine's control channel. Its open offer counts as
// unanswered, and an attempt being prepared for it goes back to the queue,
// as one the machine refused.
func (sc *scheduler) dropLive(lm *liveMachine) {
	delete(sc.machines, lm.m.ID)
	if j := lm.offer; j != nil {
		lm.offer = nil
		sc.answer(j, lm.m.ID, false)
	}
	if j := sc.preparing[lm.m.ID]; j != nil {
		p := j.prep
		sc.endPreparation(j)
		sc.unprepared(j, p, errLeftDuringPreparation())
	}
	sc.dirty = true
}

func (sc *scheduler) machineRemoved(id string) {
	sc.removed[id] = true
	if lm := sc.machines[id]; lm != nil {
		lm.sess.close(wire.CloseUnauthorized, "machine removed")
		sc.dropLive(lm)
	}
	if j := sc.running[id]; j != nil {
		sc.attemptFailed(j, &JobError{Code: CodeLost, Message: "the machine was removed", Retryable: true})
	}
}

// handle applies one message from a machine's current control channel.
func (sc *scheduler) handle(sess *session, msg wire.Message) {
	lm := sc.machines[sess.machineID]
	if lm == nil || lm.sess != sess {
		return // from a replaced connection
	}
	switch m := msg.(type) {
	case wire.State:
		lm.m.State = MachineState(m.State)
		lm.gen = sc.nextGen()
		sc.dirty = true
	case wire.Approved:
		lm.approved = setOf(m.Hashes)
		lm.m.Approved = slices.Clone(m.Hashes)
		lm.m.LastSeen = time.Now()
		lm.gen = sc.nextGen()
		sc.s.saveReport(lm.m.ID, lm.m.MachineReport.clone())
		sc.dirty = true
	case wire.Bid:
		if j := sc.jobs[m.JobID]; j != nil {
			sc.answer(j, lm.m.ID, true)
		}
	case wire.Decline:
		if j := sc.jobs[m.JobID]; j != nil {
			sc.answer(j, lm.m.ID, false)
		}
	case wire.Event:
		if j, a := sc.attemptFor(lm, m.JobID, m.Attempt); a != nil {
			sc.renew(a)
			if m.Seq <= a.lastSeq {
				return // replayed
			}
			a.lastSeq = m.Seq
			if ev, ok := jobEvent(m.Event); ok {
				ev.Attempt, ev.Machine = a.n, a.machine
				if !j.run.emit(ev) {
					sc.tooMuchData(j, a)
				}
			}
		}
	case wire.Renew:
		if _, a := sc.attemptFor(lm, m.JobID, m.Attempt); a != nil {
			sc.renew(a)
		}
	case wire.Done:
		// Acknowledge every done, even for attempts that are over, so the
		// machine can drop what it kept for them.
		sess.send(wire.Ack{JobID: m.JobID, Attempt: m.Attempt})
		if j, a := sc.attemptFor(lm, m.JobID, m.Attempt); a != nil && m.Seq > a.lastSeq {
			a.lastSeq = m.Seq
			sc.attemptDone(j, a, m)
		}
	}
}

// attemptFor returns the job and its current attempt if the message names
// that attempt and comes from the machine holding it.
func (sc *scheduler) attemptFor(lm *liveMachine, jobID string, n int) (*jobState, *attemptState) {
	j := sc.jobs[jobID]
	if j == nil || j.cur == nil || j.cur.n != n || j.cur.machine != lm.m.ID {
		return nil, nil
	}
	return j, j.cur
}

// Jobs.

func (sc *scheduler) submit(j *jobState) (*Run, error) {
	if sc.closed {
		return nil, ErrClosed
	}
	if existing := sc.jobs[j.id]; existing != nil {
		if existing.phase != phaseDone {
			return existing.run, nil
		}
		// It runs again. Its attempt numbers carry on from the last run,
		// so a machine still holding an old attempt can tell it apart.
		existing.forget.Stop()
		j.attempts = existing.attempts
	}
	if sc.bundleByHash[j.bundle.Hash()] == nil {
		return nil, fmt.Errorf("moil: bundle %s wasn't added to the server; call AddBundle first", j.bundle)
	}
	j.run = newRun(j.id, func() { sc.s.post(func(sc *scheduler) { sc.cancel(j) }) }, sc.cfg.MaxDataBytes)
	sc.jobs[j.id] = j
	sc.queue = append(sc.queue, j)
	j.run.emit(Event{Kind: EventQueued, Time: time.Now()})
	sc.log.Debug("moil: job queued", "job", j.id, "bundle", j.bundle.String())
	sc.dirty = true
	return j.run, nil
}

func (sc *scheduler) scheduleIfDirty() {
	for sc.dirty && !sc.closed {
		sc.dirty = false
		sc.schedule()
	}
}

// schedule offers each queued job, oldest first, to every machine that
// could take it and has no other offer open.
func (sc *scheduler) schedule() {
	ids := slices.Sorted(maps.Keys(sc.machines))
	for _, j := range sc.queue {
		if j.phase != phaseQueued {
			continue
		}
		var candidates []*liveMachine
		for _, id := range ids {
			lm := sc.machines[id]
			if lm.offer != nil {
				continue
			}
			if g, ok := j.skip[id]; ok && g == lm.gen {
				continue
			}
			if sc.canTake(j, lm) {
				candidates = append(candidates, lm)
			}
		}
		if len(candidates) > 0 {
			sc.offer(j, candidates)
		}
	}
}

// canTake reports whether a machine may be offered, or assigned, the job.
func (sc *scheduler) canTake(j *jobState, lm *liveMachine) bool {
	return lm.m.State == Idle &&
		sc.running[lm.m.ID] == nil &&
		sc.preparing[lm.m.ID] == nil &&
		lm.approved[j.bundle.Hash()] &&
		sc.isEligible(j, lm.m)
}

func (sc *scheduler) isEligible(j *jobState, m Machine) (ok bool) {
	defer func() {
		if p := recover(); p != nil {
			sc.log.Error("moil: Job.Eligible panicked; treating the machine as ineligible", "job", j.id, "machine", m.ID, "panic", p)
			ok = false
		}
	}()
	return j.eligible(m)
}

func (sc *scheduler) offer(j *jobState, machines []*liveMachine) {
	r := &offerRound{answered: map[string]bool{}, pending: len(machines)}
	msg := wire.Offer{JobID: j.id, BundleHash: j.bundle.Hash(), Title: j.title, ExpiresMS: max(1, sc.cfg.BidWindow.Milliseconds())}
	for _, lm := range machines {
		lm.offer = j
		r.answered[lm.m.ID] = false
		lm.sess.send(msg)
	}
	j.round = r
	j.phase = phaseOffering
	r.timer = time.AfterFunc(sc.cfg.BidWindow, func() {
		sc.s.post(func(sc *scheduler) {
			if j.round == r {
				sc.decide(j)
			}
		})
	})
	sc.log.Debug("moil: job offered", "job", j.id, "machines", len(machines))
}

// answer records a machine's bid or decline, and decides once every
// offered machine has answered.
func (sc *scheduler) answer(j *jobState, machine string, bid bool) {
	r := j.round
	if r == nil {
		return
	}
	answered, offered := r.answered[machine]
	if !offered || answered {
		return
	}
	r.answered[machine] = true
	r.pending--
	if lm := sc.machines[machine]; lm != nil && lm.offer == j {
		lm.offer = nil
	}
	if bid {
		r.bids = append(r.bids, machine)
	} else if lm := sc.machines[machine]; lm != nil {
		j.skip[machine] = lm.gen
	}
	if r.pending == 0 {
		sc.decide(j)
	}
}

// decide ends an offer round: it assigns the job to the best bidder that
// can still take it, or puts it back in the queue.
func (sc *scheduler) decide(j *jobState) {
	r := sc.closeRound(j)
	j.phase = phaseQueued
	sc.dirty = true
	var bidders []Machine
	for _, id := range r.bids {
		if lm := sc.machines[id]; lm != nil && sc.canTake(j, lm) {
			bidders = append(bidders, lm.m)
		}
	}
	if len(bidders) == 0 {
		return
	}
	sc.start(j, sc.machines[sc.pick(j, bidders)])
}

// closeRound ends a job's offer round, releasing the machines it holds.
// Machines that didn't answer aren't offered the job again until they
// change.
func (sc *scheduler) closeRound(j *jobState) *offerRound {
	r := j.round
	j.round = nil
	r.timer.Stop()
	for id, answered := range r.answered {
		lm := sc.machines[id]
		if lm == nil {
			continue
		}
		if lm.offer == j {
			lm.offer = nil
		}
		if !answered {
			j.skip[id] = lm.gen
		}
	}
	return r
}

// pick chooses among bidders, given in bid order: machines the job
// already failed on come last, then the highest Prefer score, then the
// earliest bid.
func (sc *scheduler) pick(j *jobState, bidders []Machine) string {
	return pick(bidders, j.failedOn, func(m Machine) (score int) {
		if j.prefer == nil {
			return 0
		}
		defer func() {
			if p := recover(); p != nil {
				sc.log.Error("moil: Job.Prefer panicked; scoring the machine 0", "job", j.id, "machine", m.ID, "panic", p)
				score = 0
			}
		}()
		return j.prefer(m)
	})
}

func pick(bidders []Machine, failedOn map[string]bool, score func(Machine) int) string {
	best, bestFailed, bestScore := -1, false, 0
	for i, m := range bidders {
		failed, s := failedOn[m.ID], score(m)
		if best < 0 || (bestFailed && !failed) || (failed == bestFailed && s > bestScore) {
			best, bestFailed, bestScore = i, failed, s
		}
	}
	return bidders[best].ID
}

// start begins the job's next attempt on a machine: at once, or once
// Job.Prepare has made the attempt's files.
func (sc *scheduler) start(j *jobState, lm *liveMachine) {
	j.attempts++
	if j.prepare == nil {
		sc.assign(j, lm, j.attempts, j.inputs, j.outputs)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &preparation{n: j.attempts, machine: lm.m.ID, cancel: cancel}
	j.prep, j.phase = p, phasePreparing
	sc.preparing[p.machine] = j
	a := Assignment{JobID: j.id, Attempt: p.n, Machine: lm.m}
	a.Machine.MachineReport = a.Machine.MachineReport.clone()
	sc.s.prepares.Add(1)
	go func() {
		defer sc.s.prepares.Done()
		inputs, outputs, err := callPrepare(ctx, j.prepare, a)
		sc.s.post(func(sc *scheduler) { sc.prepared(j, p, inputs, outputs, err) })
	}()
}

func callPrepare(ctx context.Context, prepare func(context.Context, Assignment) (map[string]Download, map[string]Upload, error), a Assignment) (inputs map[string]Download, outputs map[string]Upload, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("Job.Prepare panicked: %v", p)
		}
	}()
	return prepare(ctx, a)
}

// prepared assigns an attempt whose files Job.Prepare made, if its
// machine can still take it.
func (sc *scheduler) prepared(j *jobState, p *preparation, inputs map[string]Download, outputs map[string]Upload, err error) {
	if j.prep != p {
		return // the job ended, or the machine left, meanwhile
	}
	sc.endPreparation(j)
	var retry *JobError
	if errors.As(err, &retry) && retry.Retryable {
		e := *retry
		e.Machine, e.Attempt = p.machine, p.n
		sc.retry(j, &e)
		return
	}
	var ins map[string]wire.Download
	var outs map[string]wire.Upload
	if err == nil {
		ins, outs, err = wireFiles(sc.cfg.urlPolicy(), inputs, outputs)
	}
	if err == nil {
		err = checkSize(j.assign(p.n, sc.cfg.LeaseTTL, ins, outs))
	}
	if err != nil {
		sc.finish(j, Failed, nil, fmt.Errorf("moil: preparing attempt %d of job %s: %w", p.n, j.id, err))
		return
	}
	lm := sc.machines[p.machine]
	switch {
	case lm == nil:
		// dropLive ends the preparation of a machine that leaves, so this
		// can't happen; if it did, the machine would have left.
		sc.unprepared(j, p, errLeftDuringPreparation())
	case lm.m.State != Idle:
		// The machine would refuse the attempt (spec §7.2).
		sc.unprepared(j, p, &JobError{Code: CodeBusy, Message: fmt.Sprintf("the machine reported %s while the attempt was being prepared", lm.m.State), Retryable: true})
	case !lm.approved[j.bundle.Hash()]:
		sc.unprepared(j, p, &JobError{Code: CodeNotApproved, Message: "the machine withdrew its approval of the bundle while the attempt was being prepared", Retryable: true})
	case !sc.canTake(j, lm):
		// The service's Eligible no longer allows the machine. Only the
		// service's policy changed, so the machine isn't charged; the job
		// waits for another.
	default:
		sc.assign(j, lm, p.n, ins, outs)
	}
}

// unprepared puts back in the queue a job whose attempt Job.Prepare made,
// or was making, for a machine that backed out meanwhile: it left,
// reported busy or paused, or withdrew its approval. e is the refusal the
// machine would have answered the attempt with (spec §7.2), CodeBusy or
// CodeNotApproved. The attempt never reached the machine, so, like a
// refusal, it counts toward maxRefusals rather than MaxAttempts, and a
// machine that keeps bidding and backing out can't make Prepare run
// forever.
func (sc *scheduler) unprepared(j *jobState, p *preparation, e *JobError) {
	e.Machine, e.Attempt = p.machine, p.n
	j.failedOn[p.machine] = true
	sc.retry(j, e)
}

// errLeftDuringPreparation is why an attempt is given up whose machine
// disconnected while Job.Prepare made it. It's CodeBusy, as for a paused
// machine: an offline machine can't take the attempt either, and the
// attempt never started. CodeLost is for attempts a machine had, and
// counts toward MaxAttempts.
func errLeftDuringPreparation() *JobError {
	return &JobError{Code: CodeBusy, Message: "the machine disconnected while the attempt was being prepared", Retryable: true}
}

func (sc *scheduler) endPreparation(j *jobState) {
	p := j.prep
	j.prep = nil
	j.phase = phaseQueued
	p.cancel()
	if sc.preparing[p.machine] == j {
		delete(sc.preparing, p.machine)
	}
	sc.dirty = true
}

func (sc *scheduler) assign(j *jobState, lm *liveMachine, n int, inputs map[string]wire.Download, outputs map[string]wire.Upload) {
	a := &attemptState{n: n, machine: lm.m.ID, outputs: outputs}
	j.cur = a
	j.phase = phaseRunning
	sc.running[lm.m.ID] = j
	sc.renew(a)
	a.timer = time.AfterFunc(sc.cfg.LeaseTTL, func() { sc.s.post(func(sc *scheduler) { sc.checkLease(j, a) }) })
	lm.sess.send(j.assign(a.n, sc.cfg.LeaseTTL, inputs, outputs))
	j.run.setState(Running)
	j.run.emit(Event{Kind: EventAssigned, Time: time.Now(), Attempt: a.n, Machine: a.machine})
	sc.log.Info("moil: attempt assigned", "job", j.id, "attempt", a.n, "machine", a.machine)
}

func (j *jobState) assign(n int, lease time.Duration, inputs map[string]wire.Download, outputs map[string]wire.Upload) wire.Assign {
	return wire.Assign{
		JobID:      j.id,
		Attempt:    n,
		BundleHash: j.bundle.Hash(),
		Title:      j.title,
		Params:     j.params,
		Inputs:     inputs,
		Outputs:    outputs,
		LeaseMS:    ceilMilliseconds(lease),
		TimeoutMS:  ceilMilliseconds(j.timeout),
	}
}

// ceilMilliseconds rounds d up to whole milliseconds, so that a positive
// duration never becomes 0, which means none.
func ceilMilliseconds(d time.Duration) int64 {
	return int64((d + time.Millisecond - 1) / time.Millisecond)
}

func (sc *scheduler) renew(a *attemptState) {
	a.deadline = time.Now().Add(sc.cfg.LeaseTTL)
}

// checkLease fails the attempt if its lease ran out, or checks again when
// it will.
func (sc *scheduler) checkLease(j *jobState, a *attemptState) {
	if j.cur != a {
		return
	}
	if left := time.Until(a.deadline); left > 0 {
		a.timer.Reset(left)
		return
	}
	if lm := sc.machines[a.machine]; lm != nil {
		// The machine is connected but silent; tell it to stop, in case
		// the attempt is still running there.
		lm.sess.send(wire.Cancel{JobID: j.id, Attempt: a.n})
	}
	sc.attemptFailed(j, &JobError{Code: CodeLeaseExpired, Message: "the machine stopped reporting on the attempt", Retryable: true})
}

// tooMuchData stops an attempt that sent more data than its run keeps, and
// ends its job: another attempt would send as much.
func (sc *scheduler) tooMuchData(j *jobState, a *attemptState) {
	if lm := sc.machines[a.machine]; lm != nil {
		lm.sess.send(wire.Cancel{JobID: j.id, Attempt: a.n})
	}
	sc.log.Warn("moil: an attempt sent more data than Config.MaxDataBytes; stopping it", "job", j.id, "attempt", a.n, "machine", a.machine)
	sc.finish(j, Failed, nil, fmt.Errorf("%w: attempt %d on machine %s sent more than Config.MaxDataBytes (%d bytes)", ErrTooMuchData, a.n, a.machine, sc.cfg.MaxDataBytes))
}

func (sc *scheduler) attemptDone(j *jobState, a *attemptState, m wire.Done) {
	switch m.Outcome {
	case wire.OutcomeSucceeded:
		res := &Result{Meta: m.Result.Meta, Files: make(map[string]File, len(m.Result.Files)), Machine: a.machine, Attempt: a.n}
		for name, f := range m.Result.Files {
			if _, declared := a.outputs[name]; !declared {
				sc.log.Warn("moil: a machine reported an output the job didn't declare; ignoring it", "job", j.id, "attempt", a.n, "machine", a.machine, "output", name)
				continue
			}
			res.Files[name] = File{SizeBytes: f.SizeBytes, SHA256: f.SHA256}
		}
		sc.finish(j, Succeeded, res, nil)
	case wire.OutcomeFailed:
		e := m.Error
		sc.attemptFailed(j, &JobError{
			Code:       ErrorCode(e.Code),
			Message:    e.Message,
			Retryable:  e.Retryable,
			ExitCode:   e.ExitCode,
			StderrTail: e.StderrTail,
		})
	case wire.OutcomeCancelled:
		if j.cancelled {
			sc.finish(j, Cancelled, nil, ErrCancelled)
			return
		}
		// A machine may report cancelled only after the service's cancel
		// (spec §7.6). Taken at its word, an unasked one would end the job
		// as if the service had cancelled it, and a service that resubmits
		// jobs it didn't cancel would do so for as long as the machine
		// answers that way. The attempt did stop on the machine, which the
		// spec calls interrupted: a retryable failure that counts toward
		// MaxAttempts.
		sc.log.Warn("moil: a machine reported an attempt cancelled that the service didn't cancel; counting it as interrupted", "job", j.id, "attempt", a.n, "machine", a.machine)
		sc.attemptFailed(j, &JobError{
			Code:      CodeInterrupted,
			Message:   "the machine reported the attempt cancelled, but the service didn't cancel it",
			Retryable: true,
		})
	}
}

// attemptFailed ends the current attempt and retries the job if the error
// allows it and attempts remain.
func (sc *scheduler) attemptFailed(j *jobState, e *JobError) {
	a := sc.endAttempt(j)
	e.Machine, e.Attempt = a.machine, a.n
	j.failedOn[a.machine] = true
	sc.retry(j, e)
}

// maxRefusals is how many attempts may end before they start before the
// job fails: attempts machines refused (busy, not_approved), and attempts
// whose machines backed out while Job.Prepare made them. They don't count
// toward MaxAttempts, but a machine that keeps bidding and then refusing
// or backing out mustn't hold a job, or keep Prepare running, forever.
const maxRefusals = 10

// retry puts a job whose attempt failed with e back in the queue, or ends
// it if e isn't retryable or no attempts remain.
func (sc *scheduler) retry(j *jobState, e *JobError) {
	sc.log.Info("moil: attempt failed", "job", j.id, "attempt", e.Attempt, "machine", e.Machine, "code", e.Code, "message", e.Message)
	if j.cancelled {
		sc.finish(j, Cancelled, nil, ErrCancelled)
		return
	}
	if e.Code.NeverStarted() {
		// The machine refused; don't hand it the job again until it
		// reports a change.
		if lm := sc.machines[e.Machine]; lm != nil {
			j.skip[e.Machine] = lm.gen
		}
		j.refused++
	} else {
		j.counted++
	}
	if !e.Retryable || j.counted >= j.maxAttempts || j.refused >= maxRefusals {
		sc.finish(j, Failed, nil, e)
		return
	}
	if sc.bundleByHash[j.bundle.Hash()] == nil {
		sc.finish(j, Failed, nil, fmt.Errorf("%w; it isn't retried: %w", e, ErrBundleRemoved))
		return
	}
	j.phase = phaseQueued
	j.run.setState(Queued)
	j.run.emit(Event{Kind: EventRetrying, Time: time.Now(), Attempt: e.Attempt, Machine: e.Machine, Err: e})
	sc.dirty = true
}

func (sc *scheduler) endAttempt(j *jobState) *attemptState {
	a := j.cur
	j.cur = nil
	a.timer.Stop()
	if sc.running[a.machine] == j {
		delete(sc.running, a.machine)
	}
	sc.dirty = true
	return a
}

func (sc *scheduler) cancel(j *jobState) {
	if j.phase == phaseDone || j.cancelled {
		return
	}
	j.cancelled = true
	a := j.cur
	if a == nil {
		sc.finish(j, Cancelled, nil, ErrCancelled)
		return
	}
	lm := sc.machines[a.machine]
	if lm == nil {
		sc.finish(j, Cancelled, nil, ErrCancelled)
		return
	}
	lm.sess.send(wire.Cancel{JobID: j.id, Attempt: a.n})
}

// finish ends a job for good.
func (sc *scheduler) finish(j *jobState, state RunState, res *Result, err error) {
	if j.round != nil {
		sc.closeRound(j)
	}
	if j.prep != nil {
		sc.endPreparation(j)
	}
	if j.cur != nil {
		sc.endAttempt(j)
	}
	j.phase = phaseDone
	sc.queue = slices.DeleteFunc(sc.queue, func(q *jobState) bool { return q == j })
	j.run.finish(state, res, err)
	sc.log.Info("moil: job finished", "job", j.id, "state", state)
	j.forget = time.AfterFunc(sc.cfg.KeepFinished, func() {
		sc.s.post(func(sc *scheduler) {
			if sc.jobs[j.id] == j {
				delete(sc.jobs, j.id)
			}
		})
	})
	sc.dirty = true
}

// shutdown ends every unfinished job and closes every control channel.
func (sc *scheduler) shutdown() {
	sc.closed = true
	for _, j := range slices.Clone(sc.queue) {
		sc.finish(j, Failed, nil, ErrClosed)
	}
	for _, j := range sc.jobs {
		if j.forget != nil {
			j.forget.Stop()
		}
	}
	for _, lm := range sc.machines {
		lm.sess.close(1001, "the service is shutting down")
	}
	clear(sc.machines)
}

// jobEvent converts an event forwarded by a machine. Event types this SDK
// doesn't know come from newer machines and are skipped.
func jobEvent(e wire.JobEvent) (Event, bool) {
	ev := Event{Time: time.Now()}
	switch e.Type {
	case "phase":
		ev.Kind, ev.Phase = EventPhase, e.Phase
	case "progress":
		ev.Kind, ev.Fraction = EventProgress, e.Fraction
		if e.Message != nil {
			ev.Message = *e.Message
		}
	case "log":
		ev.Kind, ev.Level, ev.Message = EventLog, e.Level, *e.Message
		if ev.Level == "" {
			ev.Level = "info"
		}
	case "data":
		ev.Kind, ev.Data = EventData, e.Payload
	default:
		return Event{}, false
	}
	return ev, true
}

func setOf(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, s := range items {
		set[s] = true
	}
	return set
}

package moil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/internal/wire"
)

// Config configures a Server. Name, VerificationURL and Store are
// required; zero durations and limits take the defaults shown.
type Config struct {
	// Name is how the service introduces itself to machines, e.g. "klisi".
	Name string
	// VerificationURL is the service's page where a signed-in user
	// confirms a pairing code. Machines open it with ?code=XXXX-XXXX
	// appended; the page calls PendingPairing, then ConfirmPairing or
	// DenyPairing.
	//
	// It must be on the same origin (scheme, host and port) as the base
	// URL machines pair with, where the Handler is mounted, such as
	// https://klisi.example.com/machines/pair for
	// https://klisi.example.com/moil. Machines refuse to pair otherwise,
	// so that no service can relay another's pairing and have the user
	// confirm, on the other service's page, a machine of its choosing
	// (spec §4). NewServer can't check this, since the Server doesn't know
	// the URL it's mounted at; moiltest's fake machine checks it, as real
	// machines do.
	VerificationURL string
	// Store keeps paired machines.
	Store Store
	// Logger receives the Server's logs. Nil discards them.
	Logger *slog.Logger

	// PairingTTL is how long a pairing code stays valid. Default 10 minutes.
	PairingTTL time.Duration
	// PairingInterval is how often a pairing machine may poll for its
	// token; machines polling faster are told to slow down. Default 2s.
	PairingInterval time.Duration
	// MaxPendingPairings bounds the pairings in progress, so that
	// anonymous requests can't exhaust memory. When it's reached, a new
	// pairing replaces the oldest one nobody confirmed. Default 10000.
	// POST /v1/pair needs no credentials: also limit how often one client
	// may call it, in front of the Handler.
	MaxPendingPairings int

	// BidWindow is how long machines have to answer an offer. The Server
	// decides as soon as every offered machine answered. Default 2s.
	BidWindow time.Duration
	// LeaseTTL is how long an attempt may go without word from its
	// machine before it fails with CodeLeaseExpired. Default 2 minutes.
	// Keep it at least 90 seconds (spec §7.3): a machine takes up to 45
	// seconds to notice a silently dead channel, and must reconnect
	// before its lease runs out.
	LeaseTTL time.Duration
	// MaxAttempts is how many attempts a job may use. Default 3.
	// Attempts that never started don't count: those a machine refused (it
	// was busy or lacked the bundle, spec §7.2), and those whose machine
	// backed out while Job.Prepare made them (it left, reported busy or
	// paused, or withdrew its approval). But a job fails after 10 of
	// those, with the last one's *JobError (CodeBusy or CodeNotApproved),
	// so that a machine that keeps bidding and then refusing or backing
	// out can't hold it, or keep Job.Prepare running, forever.
	MaxAttempts int
	// PingInterval is how often the Server pings each machine; a machine
	// that doesn't answer before the next ping is disconnected. It also
	// bounds how long a machine may take to say hello. Default and
	// maximum 20s: machines take a channel that stays silent for 45
	// seconds for dead (spec §6.1).
	PingInterval time.Duration
	// KeepFinished is how long Server.Run finds a finished job. Default
	// 1 hour.
	KeepFinished time.Duration
	// MaxDataBytes bounds the data events a Run keeps, so that a machine
	// can't make the service hold unbounded memory: each counts its
	// payload's size plus 256 bytes. An attempt that sends more is
	// stopped, and its job fails with ErrTooMuchData. Default 16 MiB.
	MaxDataBytes int

	// AllowInsecureHTTP lets jobs use plain http input and output URLs on
	// any host, for development against storage without TLS, such as a
	// local MinIO. Machines refuse such URLs too unless their owners also
	// allow insecure http in the machine's settings, so leave it off in
	// production. Without it Submit accepts https URLs, and http URLs to
	// loopback hosts, which machines accept only from a service they
	// reach on loopback, as in local development (spec §3).
	AllowInsecureHTTP bool
}

// urlPolicy is what Submit accepts for input and output URLs: what a
// machine accepts from a service on loopback, since the SDK can't know
// the base URL machines reach it at.
func (c *Config) urlPolicy() wire.URLPolicy {
	return wire.URLPolicy{LoopbackHTTP: true, AnyHTTP: c.AllowInsecureHTTP}
}

func (c *Config) setDefaults() error {
	if c.Name == "" {
		return errors.New("moil: Config.Name is required")
	}
	if wire.Chars(c.Name) > 64 {
		return errors.New("moil: Config.Name must be at most 64 characters")
	}
	u, err := url.Parse(c.VerificationURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return errors.New("moil: Config.VerificationURL must be an http or https URL without a user name or password, on the same origin as the moil base URL, such as https://example.com/machines/pair for https://example.com/moil")
	}
	if c.Store == nil {
		return errors.New("moil: Config.Store is required; use NewMemoryStore for tests or NewFileStore for a small deployment")
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	durations := []struct {
		d   *time.Duration
		def time.Duration
	}{
		{&c.PairingTTL, 10 * time.Minute},
		{&c.PairingInterval, 2 * time.Second},
		{&c.BidWindow, 2 * time.Second},
		{&c.LeaseTTL, 2 * time.Minute},
		{&c.PingInterval, 20 * time.Second},
		{&c.KeepFinished, time.Hour},
	}
	for _, d := range durations {
		if *d.d < 0 {
			return errors.New("moil: Config durations must not be negative")
		}
		if *d.d == 0 {
			*d.d = d.def
		}
	}
	if c.PingInterval > 20*time.Second {
		return errors.New("moil: Config.PingInterval must be at most 20s; machines take a channel that stays silent for 45 seconds for dead")
	}
	if c.MaxPendingPairings <= 0 {
		c.MaxPendingPairings = 10000
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 3
	}
	if c.MaxDataBytes <= 0 {
		c.MaxDataBytes = 16 << 20
	}
	return nil
}

// A Server is the service side of moil: it pairs machines, keeps their
// control channels, and schedules jobs onto them. Serve its Handler under
// the service's moil base URL. Its methods are safe to call from any
// goroutine.
//
// Machines persist in the Store; jobs live in memory (see Run).
type Server struct {
	cfg      Config
	log      *slog.Logger
	store    Store
	pairings *pairings
	handler  http.Handler

	// Scheduler state is owned by one goroutine, which runs the
	// functions sent on cmds in order.
	cmds     chan func(*scheduler)
	stop     chan struct{} // closed once the scheduler has shut down
	loopDone chan struct{}

	// baseCtx bounds connections that haven't said hello yet.
	baseCtx    context.Context
	cancelBase context.CancelFunc

	connMu  sync.Mutex
	closing bool
	conns   sync.WaitGroup

	prepares sync.WaitGroup // Job.Prepare calls in flight

	reports *reporter

	closeOnce sync.Once
}

// NewServer starts a Server. Call Close to stop it.
func NewServer(cfg Config) (*Server, error) {
	if err := cfg.setDefaults(); err != nil {
		return nil, err
	}
	s := &Server{
		cfg:      cfg,
		log:      cfg.Logger,
		store:    cfg.Store,
		pairings: newPairings(),
		cmds:     make(chan func(*scheduler)),
		stop:     make(chan struct{}),
		loopDone: make(chan struct{}),
		reports:  newReporter(cfg.Store, cfg.Logger),
	}
	s.baseCtx, s.cancelBase = context.WithCancel(context.Background())
	s.handler = s.routes()
	go s.loop(newScheduler(s))
	return s, nil
}

// Handler serves the moil endpoints (spec §3), with paths relative to the
// service's moil base URL. Mount it with http.StripPrefix, e.g.
//
//	mux.Handle("/moil/", http.StripPrefix("/moil", srv.Handler()))
func (s *Server) Handler() http.Handler { return s.handler }

// Close disconnects every machine and ends every unfinished Run with
// ErrClosed. It waits for the Server's goroutines to finish, Job.Prepare
// calls included, and saves what machines last reported, giving the Store
// up to five seconds.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.connMu.Lock()
		s.closing = true
		s.connMu.Unlock()
		s.cancelBase()
		_ = s.call(func(sc *scheduler) { sc.shutdown() })
		close(s.stop)
		<-s.loopDone
		s.conns.Wait()
		s.prepares.Wait()
		s.reports.close(closeGrace)
	})
	return nil
}

// closeGrace is how long Close waits for the Store to save what machines
// last reported.
const closeGrace = 5 * time.Second

func (s *Server) loop(sc *scheduler) {
	defer close(s.loopDone)
	for {
		select {
		case f := <-s.cmds:
			f(sc)
			sc.scheduleIfDirty()
		case <-s.stop:
			return
		}
	}
}

// post runs f on the scheduler goroutine without waiting for it. It
// reports false if the Server is closed.
func (s *Server) post(f func(*scheduler)) bool {
	select {
	case s.cmds <- f:
		return true
	case <-s.stop:
		return false
	}
}

// call runs f on the scheduler goroutine and waits until the scheduler has
// acted on what f changed: when Submit returns, for example, the job's
// offers are already queued.
func (s *Server) call(f func(*scheduler)) error {
	done := make(chan struct{})
	if !s.post(func(sc *scheduler) {
		defer close(done)
		f(sc)
		sc.scheduleIfDirty()
	}) {
		return ErrClosed
	}
	<-done
	return nil
}

// saveReport persists what a machine reported, without making the
// scheduler wait for the Store.
func (s *Server) saveReport(id string, r MachineReport) { s.reports.save(id, r) }

// AddBundle makes bundles available for jobs. Connected machines are told
// at once, so their owners can review them; machines connecting later
// learn of them in welcome. Adding a bundle twice does nothing.
func (s *Server) AddBundle(bundles ...*Bundle) {
	for _, b := range bundles {
		if b == nil {
			panic("moil: AddBundle(nil)")
		}
	}
	_ = s.call(func(sc *scheduler) { sc.addBundles(bundles) })
}

// RemoveBundle retires bundles, such as a version found to be vulnerable.
// Connected machines are told at once, machines can no longer fetch them,
// and no machine is offered their jobs any more: jobs waiting for a
// machine fail with ErrBundleRemoved. An attempt already running finishes,
// but isn't retried. Removing a bundle that isn't there does nothing.
func (s *Server) RemoveBundle(bundles ...*Bundle) {
	_ = s.call(func(sc *scheduler) { sc.removeBundles(bundles) })
}

// Reschedule offers the jobs waiting for a machine again to every machine
// that could take them. The Server does so by itself whenever something it
// sees changes, such as a machine connecting or approving a bundle; call
// Reschedule after a change in the service that makes more machines
// eligible (Job.Eligible), such as a user sharing a meeting with another.
func (s *Server) Reschedule() {
	_ = s.call(func(sc *scheduler) { sc.dirty = true })
}

// Bundles returns the bundles added so far, in the order they were added.
func (s *Server) Bundles() []*Bundle {
	var out []*Bundle
	_ = s.call(func(sc *scheduler) { out = append(out, sc.bundles...) })
	return out
}

// Machines returns the machines paired by owner, oldest first, with the
// state of those that are connected.
func (s *Server) Machines(ctx context.Context, owner string) ([]Machine, error) {
	records, err := s.store.Machines(ctx, owner)
	if err != nil {
		return nil, err
	}
	out := make([]Machine, len(records))
	for i, r := range records {
		out[i] = r.machine()
	}
	s.withLive(out)
	return out, nil
}

// Machine returns one machine, or ErrNotFound. Before acting on a machine
// for a user, check that it's theirs: moil doesn't know the service's
// users.
func (s *Server) Machine(ctx context.Context, id string) (Machine, error) {
	r, err := s.store.Machine(ctx, id)
	if err != nil {
		return Machine{}, err
	}
	out := []Machine{r.machine()}
	s.withLive(out)
	return out[0], nil
}

// withLive overlays what connected machines reported since the Store last
// saw them.
func (s *Server) withLive(ms []Machine) {
	_ = s.call(func(sc *scheduler) {
		for i := range ms {
			if lm := sc.machines[ms[i].ID]; lm != nil {
				ms[i].MachineReport = lm.m.MachineReport.clone()
				ms[i].State = lm.m.State
			}
		}
	})
}

// RemoveMachine unpairs a machine: its token stops working, its control
// channel closes with 4401, and an attempt it was running fails with
// CodeLost, so its job can move on. It returns ErrNotFound for an unknown
// machine.
func (s *Server) RemoveMachine(ctx context.Context, id string) error {
	if err := s.store.RemoveMachine(ctx, id); err != nil {
		return err
	}
	s.log.Info("moil: machine removed", "machine", id)
	_ = s.call(func(sc *scheduler) { sc.machineRemoved(id) })
	return nil
}

// Submit queues a job and returns its Run. The job waits, without a time
// limit, until a connected, idle, eligible machine that approved its bundle
// bids for it.
//
// If a job with the same ID hasn't finished, Submit returns its Run and
// ignores job: the first submission's definition wins, so a service can
// resubmit what must run without doubling the work. Once a job has
// finished, its ID runs again, as a new Run: that's how to retry a job
// that failed or was cancelled.
func (s *Server) Submit(ctx context.Context, job Job) (*Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	j, err := s.newJob(job)
	if err != nil {
		return nil, err
	}
	var run *Run
	var submitErr error
	if err := s.call(func(sc *scheduler) { run, submitErr = sc.submit(j) }); err != nil {
		return nil, err
	}
	return run, submitErr
}

// newJob checks a Job and converts it to the scheduler's form.
func (s *Server) newJob(job Job) (*jobState, error) {
	if job.Bundle == nil {
		return nil, errors.New("moil: Job.Bundle is required")
	}
	if job.Eligible == nil {
		return nil, errors.New("moil: Job.Eligible is required; use moil.OwnedBy(owner) or, if any machine owner may see the job's inputs, moil.AnyMachine")
	}
	if job.ID == "" {
		job.ID = "j_" + randomID(16)
	} else if !wire.IsJobID(job.ID) {
		return nil, fmt.Errorf("moil: job ID %q must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$", job.ID)
	}
	if wire.Chars(job.Title) > 200 {
		return nil, errors.New("moil: Job.Title must be at most 200 characters")
	}
	if job.Timeout < 0 {
		return nil, errors.New("moil: Job.Timeout must not be negative")
	}
	if job.Prepare != nil && (len(job.Inputs) > 0 || len(job.Outputs) > 0) {
		return nil, errors.New("moil: set Job.Inputs and Job.Outputs, or Job.Prepare, not both")
	}
	params, err := json.Marshal(job.Params)
	if err != nil {
		return nil, fmt.Errorf("moil: Job.Params: %w", err)
	}
	if err := wire.CheckValue(params); err != nil {
		return nil, fmt.Errorf("moil: Job.Params %v; machines can't read it", err)
	}
	j := &jobState{
		id:          job.ID,
		bundle:      job.Bundle,
		title:       job.Title,
		params:      params,
		eligible:    job.Eligible,
		prefer:      job.Prefer,
		prepare:     job.Prepare,
		timeout:     job.Timeout,
		maxAttempts: job.MaxAttempts,
		skip:        map[string]uint64{},
		failedOn:    map[string]bool{},
	}
	if j.maxAttempts <= 0 {
		j.maxAttempts = s.cfg.MaxAttempts
	}
	if j.inputs, j.outputs, err = wireFiles(s.cfg.urlPolicy(), job.Inputs, job.Outputs); err != nil {
		return nil, err
	}
	if err := checkSize(j.assign(1, s.cfg.LeaseTTL, j.inputs, j.outputs)); err != nil {
		return nil, err
	}
	return j, nil
}

// wireFiles checks a job's inputs and outputs and converts them for
// assign.
func wireFiles(policy wire.URLPolicy, inputs map[string]Download, outputs map[string]Upload) (map[string]wire.Download, map[string]wire.Upload, error) {
	ins := make(map[string]wire.Download, len(inputs))
	outs := make(map[string]wire.Upload, len(outputs))
	for name, in := range inputs {
		if err := checkFile(policy, "input", name, in.URL); err != nil {
			return nil, nil, err
		}
		ins[name] = wire.Download{URL: in.URL, Headers: in.Headers}
	}
	for name, out := range outputs {
		if err := checkFile(policy, "output", name, out.URL); err != nil {
			return nil, nil, err
		}
		method := out.Method
		switch method {
		case "":
			method = http.MethodPut
		case http.MethodPut, http.MethodPost:
		default:
			return nil, nil, fmt.Errorf("moil: output %q: method must be PUT or POST, not %q", name, method)
		}
		outs[name] = wire.Upload{URL: out.URL, Method: method, Headers: out.Headers}
	}
	return ins, outs, nil
}

func checkFile(policy wire.URLPolicy, kind, name, rawURL string) error {
	if !wire.IsFileName(name) {
		return fmt.Errorf("moil: %s name %q must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$", kind, name)
	}
	if err := policy.Check(rawURL); err != nil {
		return fmt.Errorf("moil: %s %q: machines would refuse it: the URL %w", kind, name, err)
	}
	return nil
}

// checkSize makes sure an assign message fits in a control message (spec
// §10).
func checkSize(msg wire.Assign) error {
	data, err := wire.Encode(msg)
	if err != nil {
		return fmt.Errorf("moil: encoding the job: %w", err)
	}
	if len(data) > wire.MaxMessageBytes {
		return fmt.Errorf("moil: the job's params, inputs and outputs take %d bytes; a control message is limited to 1 MiB", len(data))
	}
	return nil
}

// Run returns the job with the given ID, if the Server knows it: it's
// unfinished, or finished less than Config.KeepFinished ago. After a
// resubmission, that's the latest Run.
func (s *Server) Run(id string) (*Run, bool) {
	var run *Run
	_ = s.call(func(sc *scheduler) {
		if j := sc.jobs[id]; j != nil {
			run = j.run
		}
	})
	return run, run != nil
}

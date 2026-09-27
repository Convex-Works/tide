// Package moiltest provides a fake moil machine for testing services built
// on the moil SDK, without the moil app, uv or Python.
//
// A Machine speaks the machine side of the protocol over real HTTP and a
// real WebSocket, and behaves like the app by default: it bids on offers
// for bundles it approved while idle, reports busy while running an
// attempt, renews leases, answers cancel, and keeps an attempt's messages
// until the service acknowledges them. Tests drive attempts by hand:
//
//	m := moiltest.Pair(t, srv, "alice")
//	m.Approve(bundle)
//	m.Connect()
//	run, _ := srv.Submit(ctx, job)
//	a := m.NextAttempt()
//	a.Progress(0.5, "halfway")
//	a.Output("transcript.json", []byte("{}"))
//	a.Succeed(map[string]any{"speakers": 2})
//
// Every message the service sends is checked against the protocol; a
// violation fails the test.
//
// Machine methods that wait or fail the test must be called from the test
// goroutine.
package moiltest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"git.convex.works/ConvexWorks/moil/sdk/go/internal/wire"
	"git.convex.works/ConvexWorks/moil/sdk/go/moil"
)

// DefaultTimeout is how long a Machine waits for the service before
// failing the test, unless WithTimeout says otherwise.
const DefaultTimeout = 10 * time.Second

// ErrUnauthorized is returned by TryConnect when the service rejects the
// machine's token: with 401, or by closing the channel with 4401.
var ErrUnauthorized = errors.New("moiltest: the service rejected the machine's token")

// An Option configures a Machine.
type Option func(*Machine)

// WithName sets the name the machine pairs and connects with.
func WithName(name string) Option { return func(m *Machine) { m.info.Name = name } }

// WithHardware sets the CPU count and memory the machine reports.
func WithHardware(cpus int, memoryBytes int64) Option {
	return func(m *Machine) { m.info.CPUs, m.info.MemoryBytes = cpus, memoryBytes }
}

// WithGPU adds a GPU to what the machine reports. memoryBytes may be 0.
func WithGPU(name string, memoryBytes int64) Option {
	return func(m *Machine) { m.info.GPUs = append(m.info.GPUs, wire.GPU{Name: name, MemoryBytes: memoryBytes}) }
}

// WithTimeout sets how long the machine waits for the service before
// failing the test.
func WithTimeout(d time.Duration) Option { return func(m *Machine) { m.timeout = d } }

// WithInsecureHTTP makes the machine accept plain http input and output
// URLs on any host, as a machine whose owner allowed insecure http in its
// settings does. Use it with moil.Config.AllowInsecureHTTP. Without it
// the machine accepts https, and http to loopback hosts only while the
// service is on loopback, as it is in tests (spec §3).
func WithInsecureHTTP() Option { return func(m *Machine) { m.insecureHTTP = true } }

// At makes Pair reach the service at baseURL, such as the moil prefix of
// the service's own router, instead of serving the Server's handler on a
// new test server. Pair then checks the service's confirmation page is on
// that URL's origin, as StartPairing does.
func At(baseURL string) Option {
	return func(m *Machine) { m.baseURL = strings.TrimSuffix(baseURL, "/") }
}

// A Machine is a fake moil machine paired with one service.
type Machine struct {
	t            testing.TB
	baseURL      string
	client       *http.Client
	timeout      time.Duration
	info         wire.MachineInfo
	insecureHTTP bool
	// ownServer is set when Pair serves the Server on a test server of
	// its own, whose origin no confirmation page can share.
	ownServer bool

	mu         sync.Mutex
	id, token  string
	deviceCode string
	approved   []string
	state      moil.MachineState // the state set with SetState; Busy while an attempt runs
	reported   string            // the state last sent
	conn       *channel          // the open control channel, if any
	last       *channel          // the most recent control channel, open or not
	onOffer    func(Offer) Answer
	offers     []Offer // answered offers, oldest first
	nextOffer  int     // how many of them NextOffer has returned
	bundles    []BundleInfo
	attempts   map[wire.AttemptRef]*Attempt // held: running, or done but not acknowledged
	assigned   []*Attempt                   // accepted attempts NextAttempt hasn't returned
	refuse     int                          // how many assignments to refuse
	refuseCode moil.ErrorCode
	syncs      int
	synced     map[int]chan struct{} // Sync barriers waiting for their ack
	changed    chan struct{}         // closed and replaced on every change
	stopped    chan struct{}         // closed at cleanup
	goroutines sync.WaitGroup

	pings       atomic.Int64
	ignorePings atomic.Bool
}

// A channel is one control-channel connection.
type channel struct {
	conn      *websocket.Conn
	done      chan struct{} // closed when the connection ends
	closeCode int           // the close code the service sent, or -1
}

// Serve starts an HTTP server for srv's endpoints and returns its base URL.
// The server stops when the test ends.
func Serve(t testing.TB, srv *moil.Server) string {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}

// New returns an unpaired machine for the service whose moil endpoints are
// at baseURL. Pair it with StartPairing and FinishPairing.
func New(t testing.TB, baseURL string, opts ...Option) *Machine {
	t.Helper()
	m := &Machine{
		t:        t,
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		client:   &http.Client{Timeout: DefaultTimeout},
		timeout:  DefaultTimeout,
		info:     wire.MachineInfo{Name: "test machine", OS: "linux", Arch: "x86_64", CPUs: 8, MemoryBytes: 16 << 30},
		state:    moil.Idle,
		attempts: map[wire.AttemptRef]*Attempt{},
		synced:   map[int]chan struct{}{},
		changed:  make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	for _, opt := range opts {
		opt(m)
	}
	t.Cleanup(m.stop)
	return m
}

// Pair returns a machine paired with srv for owner. It reaches srv on a new
// test HTTP server, unless given At. It isn't connected yet: approve
// bundles, then call Connect.
//
// On a test server of its own, whose origin no confirmation page can
// share, Pair doesn't check the page's origin as StartPairing does; give
// it At to have it checked.
func Pair(t testing.TB, srv *moil.Server, owner string, opts ...Option) *Machine {
	t.Helper()
	m := New(t, "", append([]Option{WithName(owner + "'s machine")}, opts...)...)
	if m.baseURL == "" {
		m.baseURL = Serve(t, srv)
		m.ownServer = true
	}
	code := m.StartPairing()
	if _, err := srv.ConfirmPairing(context.Background(), code, owner); err != nil {
		t.Fatalf("moiltest: confirming the pairing: %v", err)
	}
	m.FinishPairing()
	return m
}

func (m *Machine) stop() {
	m.mu.Lock()
	select {
	case <-m.stopped:
	default:
		close(m.stopped)
	}
	c := m.conn
	m.mu.Unlock()
	if c != nil {
		c.conn.CloseNow()
	}
	m.goroutines.Wait()
}

// ID is the machine ID the service assigned at pairing.
func (m *Machine) ID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.id
}

// Token is the machine's token, for tests of the service's own handling.
func (m *Machine) Token() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.token
}

// Twin returns a second machine with the same identity and approvals, as if
// the app were started twice with the same token. Connecting it replaces
// this machine's control channel.
func (m *Machine) Twin() *Machine {
	m.t.Helper()
	twin := New(m.t, m.baseURL, WithTimeout(m.timeout))
	m.mu.Lock()
	defer m.mu.Unlock()
	twin.info = m.info
	twin.info.GPUs = slices.Clone(m.info.GPUs)
	twin.id, twin.token = m.id, m.token
	twin.approved = slices.Clone(m.approved)
	return twin
}

// Pairing.

// StartPairing asks the service to pair (POST /v1/pair) and returns the
// user code a person would confirm on the service's page. Like a real
// machine, it refuses, failing the test, unless the page is on the origin
// of the base URL it reaches the service at (spec §4): set
// moil.Config.VerificationURL to a page there.
func (m *Machine) StartPairing() string {
	m.t.Helper()
	var resp wire.PairResponse
	if status, body := m.do(http.MethodPost, "/v1/pair", false, wire.PairRequest{
		Name: m.info.Name, OS: m.info.OS, Arch: m.info.Arch, AppVersion: "moiltest",
	}, &resp); status != http.StatusOK {
		m.t.Fatalf("moiltest: POST /v1/pair: %d %s", status, body)
	}
	if !m.ownServer {
		for _, page := range []string{resp.VerificationURI, resp.VerificationURIComplete} {
			if err := wire.CheckPairingPage(m.baseURL, page); err != nil {
				m.t.Fatalf("moiltest: machines refuse to pair: the confirmation page %v (spec §4)", err)
			}
		}
	}
	m.mu.Lock()
	m.deviceCode = resp.DeviceCode
	m.mu.Unlock()
	return resp.UserCode
}

// A PairingError is the service's answer to a poll for the token that
// isn't the token, such as "authorization_pending" or "access_denied".
type PairingError struct {
	Code string
}

// Error names the RFC 8628 error code.
func (e *PairingError) Error() string { return "moiltest: pairing: " + e.Code }

// FinishPairing polls for the token once (POST /v1/pair/token) and fails
// the test unless the service issues it. Call it after the pairing was
// confirmed.
func (m *Machine) FinishPairing() {
	m.t.Helper()
	if err := m.TryFinishPairing(); err != nil {
		m.t.Fatal(err)
	}
}

// TryFinishPairing polls for the token once. It returns a *PairingError
// when the service answers with an RFC 8628 error.
func (m *Machine) TryFinishPairing() error {
	m.mu.Lock()
	deviceCode := m.deviceCode
	m.mu.Unlock()
	var resp wire.TokenResponse
	status, body := m.do(http.MethodPost, "/v1/pair/token", false, wire.TokenRequest{DeviceCode: deviceCode}, &resp)
	if status != http.StatusOK {
		var e wire.ErrorBody
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return &PairingError{Code: e.Error}
		}
		return fmt.Errorf("moiltest: POST /v1/pair/token: %d %s", status, body)
	}
	if !wire.IsMachineID(resp.MachineID) || resp.Token == "" {
		return fmt.Errorf("moiltest: the service issued an invalid machine ID %q or an empty token", resp.MachineID)
	}
	m.mu.Lock()
	m.id, m.token = resp.MachineID, resp.Token
	m.mu.Unlock()
	return nil
}

// Unpair tells the service to forget the machine (DELETE /v1/machine) and
// drops the control channel, as the app does when its owner removes the
// service.
func (m *Machine) Unpair() {
	m.t.Helper()
	if status, body := m.do(http.MethodDelete, "/v1/machine", true, nil, nil); status != http.StatusNoContent {
		m.t.Fatalf("moiltest: DELETE /v1/machine: %d %s", status, body)
	}
	m.Disconnect()
}

// Bundles.

// A BundleInfo is a bundle as the service advertises it.
type BundleInfo struct {
	Hash, Name, Version, Description string
}

// ListBundles fetches the service's bundle list (GET /v1/bundles).
func (m *Machine) ListBundles() []BundleInfo {
	m.t.Helper()
	var resp wire.BundlesResponse
	if status, body := m.do(http.MethodGet, "/v1/bundles", true, nil, &resp); status != http.StatusOK {
		m.t.Fatalf("moiltest: GET /v1/bundles: %d %s", status, body)
	}
	return bundleInfos(resp.Bundles)
}

// FetchBundle downloads a bundle (GET /v1/bundles/{hash}) and checks it the
// way the app does: the files must make a valid bundle with that hash.
func (m *Machine) FetchBundle(hash string) *moil.Bundle {
	m.t.Helper()
	var resp wire.BundleResponse
	if status, body := m.do(http.MethodGet, "/v1/bundles/"+hash, true, nil, &resp); status != http.StatusOK {
		m.t.Fatalf("moiltest: GET /v1/bundles/%s: %d %s", hash, status, body)
	}
	files := make(map[string][]byte, len(resp.Files))
	for name, text := range resp.Files {
		files[name] = []byte(text)
	}
	b, err := moil.NewBundle(files)
	if err != nil {
		m.t.Fatalf("moiltest: the service sent an invalid bundle for %s: %v", hash, err)
	}
	if b.Hash() != hash {
		m.t.Fatalf("moiltest: asked for bundle %s, got files that hash to %s", hash, b.Hash())
	}
	return b
}

// Bundles returns the bundles the service advertised on the control
// channel, in welcome or since.
func (m *Machine) Bundles() []BundleInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.bundles)
}

// WaitForBundle waits until the service advertises the bundle hash on the
// control channel.
func (m *Machine) WaitForBundle(hash string) {
	m.t.Helper()
	m.waitFor("bundle "+hash+" to be advertised", func() bool {
		return slices.ContainsFunc(m.bundles, func(b BundleInfo) bool { return b.Hash == hash })
	})
}

func bundleInfos(bundles []wire.BundleInfo) []BundleInfo {
	out := make([]BundleInfo, len(bundles))
	for i, b := range bundles {
		out[i] = BundleInfo{Hash: b.Hash, Name: b.Name, Version: b.Version, Description: b.Description}
	}
	return out
}

// Approvals and state.

// Approve approves bundles for this service, as the owner does after
// reviewing them, and tells the service if connected.
func (m *Machine) Approve(bundles ...*moil.Bundle) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range bundles {
		if !slices.Contains(m.approved, b.Hash()) {
			m.approved = append(m.approved, b.Hash())
		}
	}
	m.sendLocked(wire.Approved{Hashes: slices.Clone(m.approved)})
}

// Revoke withdraws approval for bundles, and tells the service if
// connected.
func (m *Machine) Revoke(bundles ...*moil.Bundle) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range bundles {
		m.approved = slices.DeleteFunc(m.approved, func(h string) bool { return h == b.Hash() })
	}
	m.sendLocked(wire.Approved{Hashes: slices.Clone(m.approved)})
}

// SetState sets what the machine does when it isn't running an attempt for
// this service: moil.Idle, moil.Paused, or moil.Busy (running a job for
// another service). It reports its state to the service, if connected,
// even if it didn't change.
func (m *Machine) SetState(s moil.MachineState) {
	m.t.Helper()
	if s != moil.Idle && s != moil.Busy && s != moil.Paused {
		m.t.Fatalf("moiltest: SetState(%q): a machine reports idle, busy or paused", s)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = s
	m.reported = ""
	m.reportStateLocked()
}

// RefuseNext makes the machine refuse its next n assignments with code:
// moil.CodeBusy or moil.CodeNotApproved, as the app does when it became
// busy or lost the bundle between bidding and being assigned, or
// moil.CodeRejected, as it does with an assignment it can't read. The
// refusal is retryable as code.Retryable says.
func (m *Machine) RefuseNext(n int, code moil.ErrorCode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refuse, m.refuseCode = n, code
}

// effectiveState is Busy while an attempt runs, else the state set with
// SetState.
func (m *Machine) effectiveState() string {
	for _, a := range m.attempts {
		if a.running() {
			return wire.StateBusy
		}
	}
	return string(m.state)
}

func (m *Machine) reportStateLocked() {
	s := m.effectiveState()
	if m.conn != nil && s != m.reported {
		m.reported = s
		m.sendLocked(wire.State{State: s})
	}
}

// Offers.

// An Offer is an offer the machine received, and how it answered.
type Offer struct {
	JobID      string
	BundleHash string
	Title      string
	Expires    time.Duration
	Answer     Answer
}

// An Answer is how a machine answers an offer.
type Answer struct {
	bid    bool
	reason string // decline reason; empty for Ignore
}

// Bid takes the offered job.
var Bid = Answer{bid: true}

// Ignore leaves the offer unanswered, as a machine that's slow or gone
// would.
var Ignore = Answer{}

// Decline refuses the offered job with a reason: "busy", "paused" or
// "not_approved".
func Decline(reason string) Answer { return Answer{reason: reason} }

// OnOffer sets how the machine answers offers. By default it follows the
// app's rule: bid if idle and the bundle is approved, else decline with the
// reason. f runs on the machine's reader goroutine; it may block to delay
// the answer, but must return by the end of the test, and must not call
// methods that fail the test.
func (m *Machine) OnOffer(f func(Offer) Answer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onOffer = f
}

// Offers returns every offer the machine has answered (or ignored), oldest
// first. Call Sync first to be sure it includes every offer the service
// sent.
func (m *Machine) Offers() []Offer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.offers)
}

// NextOffer waits for the next offer the machine answered, and returns it.
// Each offer is returned once, in order. When it returns, the answer has
// been sent.
func (m *Machine) NextOffer() Offer {
	m.t.Helper()
	m.waitFor("an offer", func() bool { return len(m.offers) > m.nextOffer })
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextOffer++
	return m.offers[m.nextOffer-1]
}

// Sync waits until the service has processed every message the machine
// sent before, and the machine has handled every message the service sent
// before that. Use it before asserting that something didn't happen, such
// as an offer. It relies on the moil SDK acknowledging every done.
func (m *Machine) Sync() {
	m.t.Helper()
	m.mu.Lock()
	if m.conn == nil {
		m.mu.Unlock()
		m.t.Fatal("moiltest: Sync: not connected")
	}
	m.syncs++
	n, acked := m.syncs, make(chan struct{})
	m.synced[n] = acked
	m.sendLocked(wire.Done{JobID: syncJobID, Attempt: n, Seq: 1, Outcome: wire.OutcomeCancelled})
	m.mu.Unlock()
	select {
	case <-acked:
	case <-time.After(m.timeout):
		m.t.Fatalf("moiltest: machine %s: the service didn't acknowledge a sync within %v", m.ID(), m.timeout)
	}
}

// syncJobID names the pretend attempts Sync ends; no job has this ID
// unless a test picks it.
const syncJobID = "moiltest.sync"

// defaultAnswer is the app's rule for answering an offer (spec §7.1).
func (m *Machine) defaultAnswer(o Offer) Answer {
	switch {
	case m.effectiveState() == wire.StatePaused:
		return Decline("paused")
	case m.effectiveState() != wire.StateIdle:
		return Decline("busy")
	case !slices.Contains(m.approved, o.BundleHash):
		return Decline("not_approved")
	}
	return Bid
}

// The control channel.

// Connect opens the control channel, says hello and waits for welcome,
// then replays what the service missed of held attempts, and abandons
// those the service no longer assigns to this machine. It fails the test
// if the service refuses.
func (m *Machine) Connect() {
	m.t.Helper()
	if err := m.TryConnect(); err != nil {
		m.t.Fatalf("moiltest: connecting: %v", err)
	}
}

// TryConnect is Connect, returning an error instead of failing the test.
// It returns ErrUnauthorized if the service rejects the token.
func (m *Machine) TryConnect() error {
	m.mu.Lock()
	if m.conn != nil {
		m.mu.Unlock()
		return errors.New("moiltest: already connected")
	}
	token := m.token
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, m.baseURL+"/v1/connect", &websocket.DialOptions{
		HTTPClient: m.client,
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
		OnPingReceived: func(context.Context, []byte) bool {
			m.pings.Add(1)
			return !m.ignorePings.Load()
		},
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return ErrUnauthorized
		}
		return err
	}
	conn.SetReadLimit(1 << 20)

	m.mu.Lock()
	hello := wire.Hello{
		Protocol:   wire.Protocol,
		AppVersion: "moiltest",
		Machine:    m.info,
		State:      m.effectiveState(),
		Approved:   slices.Clone(m.approved),
	}
	for ref := range m.attempts {
		hello.Attempts = append(hello.Attempts, ref)
	}
	data, _ := wire.Encode(hello)
	err = conn.Write(ctx, websocket.MessageText, data)
	m.mu.Unlock()
	if err != nil {
		conn.CloseNow()
		return err
	}

	_, data, err = conn.Read(ctx)
	if err != nil {
		conn.CloseNow()
		if websocket.CloseStatus(err) == wire.CloseUnauthorized {
			return ErrUnauthorized
		}
		return err
	}
	msg, err := wire.Decode(data)
	welcome, ok := msg.(wire.Welcome)
	if err != nil || !ok {
		conn.CloseNow()
		return fmt.Errorf("moiltest: the service answered hello with %s instead of a valid welcome: %v", data, err)
	}
	if welcome.MachineID != m.ID() {
		m.t.Errorf("moiltest: welcome says machine %q; the machine is %q", welcome.MachineID, m.ID())
	}

	c := &channel{conn: conn, done: make(chan struct{}), closeCode: -1}
	m.mu.Lock()
	m.conn, m.last = c, c
	m.reported = hello.State
	m.bundles = bundleInfos(welcome.Bundles)
	m.resumeLocked(welcome.Resume)
	m.reportStateLocked()
	m.changedLocked()
	m.mu.Unlock()

	m.goroutines.Add(1)
	go m.read(c)
	return nil
}

// resumeLocked replays each held attempt's messages the service hasn't
// processed, and abandons the attempts it didn't list (spec §7.5).
func (m *Machine) resumeLocked(resume []wire.Resume) {
	seen := map[wire.AttemptRef]int64{}
	for _, r := range resume {
		seen[wire.AttemptRef{JobID: r.JobID, Attempt: r.Attempt}] = r.Seq
	}
	for ref, a := range m.attempts {
		last, ok := seen[ref]
		if !ok {
			a.abandonLocked()
			delete(m.attempts, ref)
			continue
		}
		for _, k := range a.kept {
			if k.seq > last {
				m.writeLocked(k.data)
			}
		}
	}
}

// Disconnect drops the control channel without a close handshake, as a
// network failure would. The machine keeps its attempts; call Connect to
// come back.
func (m *Machine) Disconnect() {
	m.mu.Lock()
	c := m.conn
	m.conn = nil
	m.mu.Unlock()
	if c != nil {
		c.conn.CloseNow()
		<-c.done
	}
}

// Crash drops the control channel and forgets every attempt, as if the app
// were killed and restarted. The next Connect lists no attempts.
func (m *Machine) Crash() {
	m.Disconnect()
	m.mu.Lock()
	defer m.mu.Unlock()
	for ref, a := range m.attempts {
		a.abandonLocked()
		delete(m.attempts, ref)
	}
	m.changedLocked()
}

// Connected reports whether the control channel is open.
func (m *Machine) Connected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conn != nil
}

// WaitClosed waits until the service closes the latest control channel,
// if it hasn't already, and returns the close code it sent (4401, 4409…),
// or -1 if the connection ended without one.
func (m *Machine) WaitClosed() int {
	m.t.Helper()
	m.mu.Lock()
	c := m.last
	m.mu.Unlock()
	if c == nil {
		m.t.Fatal("moiltest: WaitClosed: the machine never connected")
	}
	select {
	case <-c.done:
		return c.closeCode
	case <-time.After(m.timeout):
		m.t.Fatalf("moiltest: the service didn't close the channel within %v", m.timeout)
		return 0
	}
}

// IgnorePings makes the machine stop answering the service's pings, as if
// the app or its network hung. The service should disconnect it.
func (m *Machine) IgnorePings() { m.ignorePings.Store(true) }

// Pings reports how many pings the service has sent the machine.
func (m *Machine) Pings() int { return int(m.pings.Load()) }

// Send sends msg on the control channel as JSON, verbatim, for testing how
// a service handles unusual or invalid messages.
func (m *Machine) Send(msg any) {
	m.t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		m.t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil {
		m.t.Fatal("moiltest: Send: not connected")
	}
	m.writeLocked(data)
}

func (m *Machine) read(c *channel) {
	defer m.goroutines.Done()
	for {
		typ, data, err := c.conn.Read(context.Background())
		if err != nil {
			m.mu.Lock()
			c.closeCode = int(websocket.CloseStatus(err))
			if m.conn == c {
				m.conn = nil
			}
			close(c.done)
			m.changedLocked()
			m.mu.Unlock()
			return
		}
		if typ != websocket.MessageText {
			m.t.Errorf("moiltest: the service sent a binary frame")
			continue
		}
		msg, err := wire.Decode(data)
		if err != nil {
			m.t.Errorf("moiltest: the service sent an invalid message: %v\n%s", err, data)
			continue
		}
		m.handle(msg)
	}
}

func (m *Machine) handle(msg wire.Message) {
	switch msg := msg.(type) {
	case wire.Bundles:
		m.mu.Lock()
		m.bundles = bundleInfos(msg.Bundles)
		m.changedLocked()
		m.mu.Unlock()
	case wire.Offer:
		o := Offer{JobID: msg.JobID, BundleHash: msg.BundleHash, Title: msg.Title, Expires: time.Duration(msg.ExpiresMS) * time.Millisecond}
		m.mu.Lock()
		hook := m.onOffer
		m.mu.Unlock()
		if hook != nil {
			o.Answer = hook(o)
		} else {
			m.mu.Lock()
			o.Answer = m.defaultAnswer(o)
			m.mu.Unlock()
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		switch {
		case o.Answer.bid:
			m.sendLocked(wire.Bid{JobID: o.JobID})
		case o.Answer.reason != "":
			m.sendLocked(wire.Decline{JobID: o.JobID, Reason: o.Answer.reason})
		}
		m.offers = append(m.offers, o)
		m.changedLocked()
	case wire.Assign:
		m.assign(msg)
	case wire.Ack:
		m.mu.Lock()
		defer m.mu.Unlock()
		if msg.JobID == syncJobID {
			if acked := m.synced[msg.Attempt]; acked != nil {
				delete(m.synced, msg.Attempt)
				close(acked)
			}
			return
		}
		ref := wire.AttemptRef{JobID: msg.JobID, Attempt: msg.Attempt}
		if a := m.attempts[ref]; a != nil && a.finished {
			delete(m.attempts, ref)
			close(a.acked)
			m.changedLocked()
		}
	case wire.Cancel:
		m.mu.Lock()
		defer m.mu.Unlock()
		a := m.attempts[wire.AttemptRef{JobID: msg.JobID, Attempt: msg.Attempt}]
		if a == nil || a.finished {
			return
		}
		a.cancelOnce.Do(func() { close(a.cancelled) })
		if !a.silent {
			a.doneLocked(wire.Done{Outcome: wire.OutcomeCancelled})
		}
	case wire.Welcome:
		m.t.Errorf("moiltest: the service sent a second welcome")
	case wire.Unknown:
	default:
		m.t.Errorf("moiltest: the service sent a %s message, which only machines send", msg.Type())
	}
}

// assign starts an attempt, or refuses it the way the app does (spec §7.2).
func (m *Machine) assign(msg wire.Assign) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ref := wire.AttemptRef{JobID: msg.JobID, Attempt: msg.Attempt}
	if m.attempts[ref] != nil {
		m.t.Errorf("moiltest: the service assigned attempt %d of %s twice", msg.Attempt, msg.JobID)
		return
	}
	a := newAttempt(m, msg)
	m.attempts[ref] = a
	params, unreadable := reencode(msg.Params)
	switch {
	case unreadable != nil:
		a.doneLocked(wire.Done{Outcome: wire.OutcomeFailed, Error: &wire.JobError{
			Code: string(moil.CodeRejected), Message: "params " + unreadable.Error(),
		}})
		return
	case m.refuse > 0:
		m.refuse--
		a.doneLocked(wire.Done{Outcome: wire.OutcomeFailed, Error: &wire.JobError{
			Code: string(m.refuseCode), Message: "refused, as the test asked", Retryable: m.refuseCode.Retryable(),
		}})
		return
	case m.effectiveState() != wire.StateIdle:
		a.doneLocked(wire.Done{Outcome: wire.OutcomeFailed, Error: &wire.JobError{
			Code: string(moil.CodeBusy), Message: "the machine is " + m.effectiveState(), Retryable: true,
		}})
		return
	case !slices.Contains(m.approved, msg.BundleHash):
		a.doneLocked(wire.Done{Outcome: wire.OutcomeFailed, Error: &wire.JobError{
			Code: string(moil.CodeNotApproved), Message: "the bundle isn't approved", Retryable: true,
		}})
		return
	}
	a.Params = params
	a.started = true
	m.assigned = append(m.assigned, a)
	m.reportStateLocked()
	m.changedLocked()
	m.goroutines.Add(1)
	go a.renewLoop()
}

// NextAttempt waits for the next attempt the machine accepted, and returns
// it.
func (m *Machine) NextAttempt() *Attempt {
	m.t.Helper()
	m.waitFor("an attempt to be assigned", func() bool { return len(m.assigned) > 0 })
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.assigned[0]
	m.assigned = m.assigned[1:]
	return a
}

// waitFor waits until cond, checked under the lock, holds.
func (m *Machine) waitFor(what string, cond func() bool) {
	m.t.Helper()
	deadline := time.After(m.timeout)
	for {
		m.mu.Lock()
		ok, changed := cond(), m.changed
		m.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-changed:
		case <-deadline:
			m.t.Fatalf("moiltest: machine %s: waited %v for %s", m.ID(), m.timeout, what)
		}
	}
}

func (m *Machine) changedLocked() {
	close(m.changed)
	m.changed = make(chan struct{})
}

// sendLocked sends msg if the control channel is open.
func (m *Machine) sendLocked(msg wire.Message) {
	if m.conn == nil {
		return
	}
	data, err := wire.Encode(msg)
	if err != nil {
		m.t.Errorf("moiltest: encoding %s: %v", msg.Type(), err)
		return
	}
	m.writeLocked(data)
}

// writeLocked writes data if the control channel is open. A failed write
// means the channel is going away; the reader notices.
func (m *Machine) writeLocked(data []byte) {
	if m.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	defer cancel()
	_ = m.conn.conn.Write(ctx, websocket.MessageText, data)
}

// do makes an HTTP request to the service and decodes a 2xx JSON answer
// into out. It returns the status and the raw body.
func (m *Machine) do(method, path string, auth bool, in, out any) (int, []byte) {
	m.t.Helper()
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			m.t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, m.baseURL+path, body)
	if err != nil {
		m.t.Fatal(err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+m.Token())
	}
	resp, err := m.client.Do(req)
	if err != nil {
		m.t.Fatalf("moiltest: %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		m.t.Fatalf("moiltest: %s %s: %v", method, path, err)
	}
	if out != nil && resp.StatusCode/100 == 2 {
		if err := json.Unmarshal(data, out); err != nil {
			m.t.Fatalf("moiltest: %s %s answered %s: %v", method, path, data, err)
		}
	}
	return resp.StatusCode, data
}

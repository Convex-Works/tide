package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"tide/internal/api"
)

// A machine is a computer its owner lends tide with the moil command
// line, as moil's own end-to-end tests drive it: a moil home of its own,
// the moil binary and, while it runs, `moil agent`.
type machine struct {
	t    *testing.T
	name string
	home string
	// stub is where the stub bundle keeps its attempts.log and looks for
	// its hold gate. uv is the uv the machine runs: a wrapper around the
	// real one that tells the stub where that is.
	stub string
	uv   string
	log  *testLog
	// agentLog holds what every `moil agent` run wrote to stderr.
	agentLog lines

	// Set by pairing.
	id        string // tide's ID for the machine
	serviceID string // the machine's ID for tide

	agent *agentProcess // the running agent, if any
}

type agentProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error // how it exited, once done is closed
}

func newMachine(t *testing.T, name string) *machine {
	t.Helper()
	root := t.TempDir()
	m := &machine{
		t: t, name: name, log: logFor(t),
		home: filepath.Join(root, "home"), stub: filepath.Join(root, "stub"), uv: filepath.Join(root, "uv"),
	}
	if err := os.Mkdir(m.stub, 0o755); err != nil {
		t.Fatal(err)
	}
	// The runtime passes uv only a minimal environment, which uv passes on
	// to the script: the wrapper adds the suite's uv cache, and where the
	// stub should look.
	script := fmt.Sprintf("#!/bin/sh\nexport UV_CACHE_DIR=%s\nexport TRANSCRIBE_STUB_DIR=%s\nexec %s \"$@\"\n",
		shellQuote(uvCache), shellQuote(m.stub), shellQuote(uvBin))
	if err := os.WriteFile(m.uv, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if m.agent != nil {
			m.Stop()
		}
	})
	return m
}

// lentMachine sets up what most tests start from: a machine the host
// paired, that approved the bundle tide publishes, with its agent
// running, and that tide shows as idle and approved.
func lentMachine(t *testing.T, h *host, name string) *machine {
	t.Helper()
	m := newMachine(t, name)
	m.Pair(h)
	m.Approve(h.k.bundle.Hash())
	m.Start()
	h.waitForMachine(m.id, "idle and approved", func(info api.MachineInfo) bool {
		return info.State == "idle" && info.Approved
	})
	return m
}

// env is the environment every moil command runs with: only what it
// needs, so the developer's own settings and keychain stay out of it.
func (m *machine) env() []string {
	return []string{
		"MOIL_HOME=" + m.home,
		"MOIL_SECRETS=file",
		"MOIL_UV=" + m.uv,
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + os.TempDir(),
		"LANG=C.UTF-8",
		"NO_COLOR=1",
	}
}

func (m *machine) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, moilBin, args...)
	cmd.Env = m.env()
	cmd.WaitDelay = 5 * time.Second
	inReaperGroup(cmd)
	return cmd
}

// try runs a moil command to its end.
func (m *machine) try(args ...string) (stdout, stderr string, err error) {
	m.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := m.command(ctx, args...)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	m.log.print(m.name, "$ moil "+strings.Join(args, " "))
	if stderr := strings.TrimSpace(errOut.String()); stderr != "" {
		for _, line := range strings.Split(stderr, "\n") {
			m.log.print(m.name, line)
		}
	}
	return out.String(), errOut.String(), err
}

// moil runs a moil command that must succeed, and returns its stdout.
func (m *machine) moil(args ...string) string {
	m.t.Helper()
	stdout, stderr, err := m.try(args...)
	if err != nil {
		m.t.Fatalf("moil %s: %v\n%s", strings.Join(args, " "), err, stderr)
	}
	return stdout
}

// Pair pairs the machine with tide for h, the way a person does: `moil
// pair` shows a code, and h finds the machine under it on /machines, checks
// it's theirs, and confirms.
func (m *machine) Pair(h *host) {
	m.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()
	cmd := m.command(ctx, "pair", h.k.moilURL(), "--no-browser", "--name", m.name)
	cmd.Stderr = m.log.writer(m.name)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		m.t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		m.t.Fatal(err)
	}
	var shown []string
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		shown = append(shown, line)
		if code, ok := strings.CutPrefix(line, "Code: "); ok {
			m.confirm(h, code)
		}
		if id, ok := strings.CutPrefix(line, "Paired: "); ok {
			m.serviceID = id
		}
	}
	if err := cmd.Wait(); err != nil || m.serviceID == "" || m.id == "" {
		m.t.Fatalf("moil pair %s: %v; it printed:\n%s", h.k.moilURL(), err, strings.Join(shown, "\n"))
	}
}

// confirm is h confirming the pairing code on /machines?code=…
func (m *machine) confirm(h *host, code string) {
	m.t.Helper()
	var pairing api.PairingInfo
	h.call(http.MethodGet, fill(api.PairingPath, code), nil, http.StatusOK, &pairing)
	if pairing.Code != code || pairing.Name != m.name || pairing.OS != moilOS() ||
		pairing.Arch != moilArch() || pairing.AppVersion != moilVersion || pairing.MoilURL != h.k.moilURL() {
		m.t.Fatalf("pairing %s = %+v, want %s on %s %s, moil %s, pairing with %s",
			code, pairing, m.name, moilOS(), moilArch(), moilVersion, h.k.moilURL())
	}
	var confirmed api.MachineInfo
	h.call(http.MethodPost, fill(api.PairingConfirmPath, code), nil, http.StatusCreated, &confirmed)
	if confirmed.Name != m.name || confirmed.State != "offline" || confirmed.Approved {
		m.t.Fatalf("confirmed = %+v, want %s, offline, not approved", confirmed, m.name)
	}
	m.id = confirmed.ID
}

// moilOS and moilArch are how moil names this computer's platform.
func moilOS() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return runtime.GOOS
}

func moilArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "aarch64"
	case "amd64":
		return "x86_64"
	}
	return runtime.GOARCH
}

// Review returns what `moil review` says of the bundle with hash: its
// status, such as "not approved".
func (m *machine) Review(hash string) string {
	m.t.Helper()
	out := m.moil("review", m.serviceID)
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, hash); ok {
			// Columns: HASH  STATUS  BUNDLE, the bundle being its name and
			// version.
			status, _, _ := strings.Cut(strings.TrimSpace(rest), "  ")
			return status
		}
	}
	m.t.Fatalf("moil review doesn't list %s:\n%s", hash, out)
	return ""
}

// Approve approves the bundle with hash for tide, as its owner would after
// reading it: `moil approve`.
func (m *machine) Approve(hash string) {
	m.t.Helper()
	if out := m.moil("approve", m.serviceID, hash, "--yes"); !strings.Contains(out, "Approved: "+hash) {
		m.t.Fatalf("moil approve didn't approve %s:\n%s", hash, out)
	}
}

// Start runs `moil agent` and waits until it has connected to tide.
func (m *machine) Start() {
	m.t.Helper()
	if m.agent != nil {
		m.t.Fatalf("%s's agent is already running", m.name)
	}
	from := m.agentLog.len()
	a := &agentProcess{done: make(chan struct{})}
	a.cmd = m.command(context.Background(), "agent")
	a.cmd.Stderr = m.log.writer(m.name, m.agentLog.add)
	if err := a.cmd.Start(); err != nil {
		m.t.Fatal(err)
	}
	go func() {
		a.err = a.cmd.Wait()
		close(a.done)
	}()
	m.agent = a
	eventually(m.t, m.name+"'s agent to connect", func() bool {
		select {
		case <-a.done:
			m.agent = nil
			m.t.Fatalf("%s's agent exited: %v", m.name, a.err)
		default:
		}
		_, connected := m.agentLog.find(from, "connected to tide")
		return connected
	})
}

// Stop stops the agent as its owner would: SIGTERM, which makes it
// interrupt the running job, report it, and exit.
func (m *machine) Stop() {
	m.t.Helper()
	a := m.agent
	if a == nil {
		m.t.Fatalf("%s's agent isn't running", m.name)
	}
	m.agent = nil
	a.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-a.done:
	case <-time.After(patience):
		a.cmd.Process.Kill()
		<-a.done
		m.t.Fatalf("%s's agent was still running %v after SIGTERM", m.name, patience)
	}
	if a.err != nil {
		m.t.Errorf("%s's agent exited with %v after SIGTERM", m.name, a.err)
	}
}

// Logged waits until the agent has logged a line containing s since the
// from-th line, and returns the line.
func (m *machine) Logged(from int, s string) string {
	m.t.Helper()
	var line string
	eventually(m.t, m.name+"'s agent to log "+s, func() bool {
		var ok bool
		line, ok = m.agentLog.find(from, s)
		return ok
	})
	return line
}

// Hold makes the stub wait, before it writes the transcript, until
// Release.
func (m *machine) Hold() {
	m.t.Helper()
	if err := os.WriteFile(filepath.Join(m.stub, "hold"), nil, 0o644); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) Release() {
	m.t.Helper()
	if err := os.Remove(filepath.Join(m.stub, "hold")); err != nil {
		m.t.Fatal(err)
	}
}

// A stubAttempt is an attempt the stub started on the machine, as its
// attempts.log records it.
type stubAttempt struct {
	JobID   string `json:"job_id"`
	Attempt int    `json:"attempt"`
	PID     int    `json:"pid"`
}

// Attempts returns the attempts the stub started on the machine, in order.
func (m *machine) Attempts() []stubAttempt {
	m.t.Helper()
	data, err := os.ReadFile(filepath.Join(m.stub, "attempts.log"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		m.t.Fatal(err)
	}
	var attempts []stubAttempt
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var attempt stubAttempt
		if err := json.Unmarshal([]byte(line), &attempt); err != nil {
			m.t.Fatalf("%s's attempts.log: %q: %v", m.name, line, err)
		}
		attempts = append(attempts, attempt)
	}
	return attempts
}

// attemptKey is how the agent names an attempt at transcribing r.
func (m *machine) attemptKey(r *recording, attempt int) string {
	return fmt.Sprintf("%s attempt %d from %s", r.jobID(), attempt, m.serviceID)
}

// jobID is the moil job tide submits to transcribe the recording.
func (r *recording) jobID() string { return "recording-" + r.id }

// awaitGone waits until the process is gone.
func awaitGone(t *testing.T, what string, pid int) {
	t.Helper()
	eventually(t, what+" to end", func() bool { return !alive(pid) })
}

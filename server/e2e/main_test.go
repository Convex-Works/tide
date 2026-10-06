package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// What every test shares, set up once by TestMain.
var (
	// skipped says why the suite doesn't run, when it doesn't.
	skipped string
	// moilBin is the moil binary machines run: $MOIL_BIN.
	moilBin string
	// moilVersion is the version it reports, which machines pair as.
	moilVersion string
	// uvBin is the real uv, which each machine runs through a wrapper of
	// its own (see newMachine).
	uvBin string
	// uvCache is the uv cache every machine shares, in the suite's scratch
	// directory rather than the developer's.
	uvCache string
	// reaperGroup is the process group every process the tests start
	// joins (see startReaper).
	reaperGroup int
	// objects is the S3 storage tide keeps recordings in: MinIO in a
	// container, or the store $TIDE_E2E_S3_ENDPOINT names.
	objects *objectStore
)

// patience is how long a test waits for something that should happen
// before it gives up. Things happen much sooner when all is well.
const patience = 30 * time.Second

func TestMain(m *testing.M) {
	if moilBin = os.Getenv("MOIL_BIN"); moilBin == "" {
		skipped = "these tests run the real moil binary: set MOIL_BIN to it, or run `make moil-e2e`"
		os.Exit(m.Run())
	}
	cleanup, err := setUp()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// needSuite skips t unless TestMain set the suite up.
func needSuite(t *testing.T) {
	t.Helper()
	if skipped != "" {
		t.Skip(skipped)
	}
}

func setUp() (cleanup func(), err error) {
	var cleanups []func()
	cleanup = func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()

	if moilBin, err = filepath.Abs(moilBin); err != nil {
		return nil, err
	}
	out, err := exec.Command(moilBin, "--version").Output()
	if err != nil {
		return nil, fmt.Errorf("MOIL_BIN=%s doesn't run: %w", moilBin, err)
	}
	moilVersion = strings.TrimPrefix(strings.TrimSpace(string(out)), "moil ")
	if uvBin, err = findUV(); err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp("", "tide-e2e-")
	if err != nil {
		return nil, err
	}
	cleanups = append(cleanups, func() { os.RemoveAll(scratch) })
	uvCache = filepath.Join(scratch, "uv-cache")

	external, err := s3FromEnv()
	if err != nil {
		return nil, err
	}
	// Named for this process, so the reaper can remove it too.
	container := ""
	if external == nil {
		container = fmt.Sprintf("tide-e2e-minio-%d", os.Getpid())
	}
	stopReaper, err := startReaper(container)
	if err != nil {
		return nil, err
	}
	cleanups = append(cleanups, stopReaper)
	if objects = external; objects == nil {
		if objects, err = startMinIO(container); err != nil {
			return nil, err
		}
		cleanups = append(cleanups, func() { removeContainer(container) })
	}
	if err := objects.createBucket(); err != nil {
		return nil, err
	}
	return cleanup, nil
}

// startReaper starts a process that, should this test binary die before
// its cleanups run, as when go test's -timeout fires, removes the MinIO
// container, if there is one, and kills every process the tests started.
// The processes join its process group (see inReaperGroup), and it acts
// when its end of a pipe from this process closes without a word. stop
// says the word first: by then the cleanups have done its work.
func startReaper(container string) (stop func(), err error) {
	script := `read word; [ "$word" = done ] && exit 0; `
	if container != "" {
		script += "docker rm --force " + shellQuote(container) + " >/dev/null 2>&1; "
	}
	script += "kill -KILL 0"
	reaper := exec.Command("/bin/sh", "-c", script)
	reaper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pipe, err := reaper.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := reaper.Start(); err != nil {
		return nil, fmt.Errorf("starting the reaper: %w", err)
	}
	reaperGroup = reaper.Process.Pid
	return func() {
		io.WriteString(pipe, "done\n")
		pipe.Close()
		reaper.Wait()
	}, nil
}

// inReaperGroup makes cmd's process join the reaper's process group.
func inReaperGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: reaperGroup}
}

// findUV finds the real uv: $MOIL_UV, as the moil agent would, or on PATH,
// or where its installer puts it.
func findUV() (string, error) {
	if uv := os.Getenv("MOIL_UV"); uv != "" {
		return filepath.Abs(uv)
	}
	if path, err := exec.LookPath("uv"); err == nil {
		return filepath.Abs(path)
	}
	home, _ := os.UserHomeDir()
	installed := filepath.Join(home, ".local", "bin", "uv")
	if _, err := os.Stat(installed); err == nil {
		return installed, nil
	}
	return "", fmt.Errorf("machines run bundles with real uv, which isn't $MOIL_UV, on PATH or in ~/.local/bin; install it: https://docs.astral.sh/uv/")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// eventually waits until cond holds, failing the test with what it waited
// for if it doesn't within patience.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// await waits until ok holds of what get returns, and returns it, failing
// the test with what it waited for, and what it got last, if it doesn't
// within patience.
func await[T any](t *testing.T, what string, get func() T, ok func(T) bool) T {
	t.Helper()
	deadline := time.Now().Add(patience)
	for {
		value := get()
		if ok(value) {
			return value
		}
		if time.Now().After(deadline) {
			last, _ := json.Marshal(value)
			t.Fatalf("gave up waiting for %s; last: %s", what, last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// alive reports whether a process runs. A zombie counts: it's gone once
// its parent reaps it.
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

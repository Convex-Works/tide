package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"tide/internal/api"
	"tide/internal/auth"
	"tide/internal/media"
	"tide/internal/store"
)

// These tests run tide's main in a process of its own, as a container
// runtime does, and stop it with signals.

func TestMain(m *testing.M) {
	if os.Getenv("TIDE_TEST_MAIN") == "1" {
		if os.Getenv("TIDE_TEST_MEDIA_STARTS_SLOWLY") == "1" {
			// A media server whose start lasts until it is given up on, as
			// discovering the node address over STUN can take a minute.
			startMediaServer = func(ctx context.Context, _ media.Options) (*media.Server, error) {
				fmt.Fprintln(os.Stderr, "test: the media server is starting")
				<-ctx.Done()
				return nil, fmt.Errorf("media: start: %w", ctx.Err())
			}
		}
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestASignalStopsTideOnceItsRequestsFinish(t *testing.T) {
	k := startMain(t)
	retro := k.startRequest(`{"name":"Retro"}`)
	k.signal(syscall.SIGTERM)
	k.waitForLog("tide: stopping")

	// The request that was in flight is answered...
	status, body := retro()
	var created api.RoomInfo
	if err := json.Unmarshal([]byte(body), &created); status != http.StatusCreated || err != nil {
		t.Fatalf("the request in flight = %d %s, want the room created", status, body)
	}
	// ...and tide exits cleanly, with what it did in the database.
	if state := k.wait(15 * time.Second); !state.Success() {
		t.Fatalf("tide exited with %v", state)
	}
	db, err := store.Open(k.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if room, err := db.RoomBySlug(context.Background(), created.Slug); err != nil || room.Name != "Retro" {
		t.Fatalf("the room = %+v, %v", room, err)
	}
}

func TestASecondSignalStopsTideAtOnce(t *testing.T) {
	k := startMain(t)
	// A request that will never finish would hold tide for its whole grace.
	k.startRequest(`{"name":"Retro"}`)
	k.signal(syscall.SIGTERM)
	k.waitForLog("tide: stopping")
	k.signal(syscall.SIGTERM)
	state := k.wait(shutdownGrace / 2)
	if status, ok := state.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("tide exited with %v, want it killed by the second SIGTERM", state)
	}
}

// A tideProcess is tide's main running in a process of its own.
type tideProcess struct {
	t      *testing.T
	cmd    *exec.Cmd
	addr   string
	dbPath string
	cookie *http.Cookie
	logs   chan string
	exited chan struct{}
	// history is every line waitForLog has read so far.
	history []string
}

// logged reports whether tide has logged a line containing text, among
// those waitForLog has read.
func (k *tideProcess) logged(text string) bool {
	for _, line := range k.history {
		if strings.Contains(line, text) {
			return true
		}
	}
	return false
}

const testSessionSecret, testBaseURL = "test-session-secret-long-enough-for-tide", "http://127.0.0.1"

// startMain starts main in a new process, signed in as in development,
// with its own address and database, and waits until it listens. No
// identity provider or media server runs: these tests reach neither.
func startMain(t *testing.T) *tideProcess {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "tide.db")
	k := startMainWith(t,
		"TIDE_DEV_MODE=true", "TIDE_DB_PATH="+dbPath, "TIDE_SESSION_SECRET="+testSessionSecret,
		"TIDE_OIDC_ISSUER=http://127.0.0.1:1/dex",
		"TIDE_MEDIA_URL=ws://127.0.0.1:1", "TIDE_MEDIA_PUBLIC_URL=ws://127.0.0.1:1")
	k.dbPath = dbPath
	sessions := auth.NewSessions(testSessionSecret, testBaseURL, nil)
	recorder := httptest.NewRecorder()
	if err := sessions.Set(recorder, auth.Session{Sub: "alice", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	k.cookie = recorder.Result().Cookies()[0]
	return k
}

// startMainWith starts main in a new process on its own address, with env
// as its only TIDE_ variables besides, and waits until it listens.
func startMainWith(t *testing.T, env ...string) *tideProcess {
	t.Helper()
	k := launchMain(t, env...)
	listening := k.waitForLog("tide listening on ")
	k.addr = strings.TrimPrefix(listening[strings.Index(listening, "tide listening on "):], "tide listening on ")
	return k
}

// launchMain starts main in a new process as startMainWith does, and
// waits for nothing.
func launchMain(t *testing.T, env ...string) *tideProcess {
	t.Helper()
	k := &tideProcess{t: t, logs: make(chan string, 1000), exited: make(chan struct{})}
	k.cmd = exec.Command(os.Args[0], "-test.run=^$")
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "TIDE_") {
			k.cmd.Env = append(k.cmd.Env, variable)
		}
	}
	k.cmd.Env = append(k.cmd.Env, "TIDE_TEST_MAIN=1", "TIDE_ADDR=127.0.0.1:0", "TIDE_BASE_URL="+testBaseURL)
	k.cmd.Env = append(k.cmd.Env, env...)
	stderr, err := k.cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := k.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = k.cmd.Process.Kill()
		<-k.exited
	})
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			t.Log(scanner.Text())
			select {
			case k.logs <- scanner.Text():
			default:
			}
		}
		_ = k.cmd.Wait()
		close(k.exited)
	}()
	return k
}

// waitForLog waits for a line tide logs that contains text, and returns it.
func (k *tideProcess) waitForLog(text string) string {
	k.t.Helper()
	timeout := time.After(30 * time.Second)
	for {
		select {
		case line := <-k.logs:
			k.history = append(k.history, line)
			if strings.Contains(line, text) {
				return line
			}
		case <-k.exited:
			// Every line it logged is buffered by now: read them first.
			for {
				select {
				case line := <-k.logs:
					k.history = append(k.history, line)
					if strings.Contains(line, text) {
						return line
					}
					continue
				default:
				}
				break
			}
			k.t.Fatalf("tide exited before logging %q", text)
		case <-timeout:
			k.t.Fatalf("tide never logged %q", text)
		}
	}
}

func (k *tideProcess) signal(signal os.Signal) {
	k.t.Helper()
	if err := k.cmd.Process.Signal(signal); err != nil {
		k.t.Fatal(err)
	}
}

// wait waits up to within for tide to exit, and returns how it did.
func (k *tideProcess) wait(within time.Duration) *os.ProcessState {
	k.t.Helper()
	select {
	case <-k.exited:
		return k.cmd.ProcessState
	case <-time.After(within):
		k.t.Fatalf("tide still ran %s later", within)
		return nil
	}
}

// startRequest sends alice's browser's request to create a room, and waits
// until tide reads its body, which it holds back: the request is in flight
// until finish sends the body and returns tide's answer.
func (k *tideProcess) startRequest(body string) (finish func() (int, string)) {
	k.t.Helper()
	conn, err := net.Dial("tcp", k.addr)
	if err != nil {
		k.t.Fatal(err)
	}
	k.t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: tide\r\nCookie: %s=%s\r\nX-Tide-Csrf: 1\r\n"+
		"Content-Type: application/json\r\nContent-Length: %d\r\nExpect: 100-continue\r\n\r\n",
		api.RoomsPath, k.cookie.Name, k.cookie.Value, len(body))
	reader := bufio.NewReader(conn)
	head := textproto.NewReader(reader)
	for _, want := range []string{"HTTP/1.1 100 Continue", ""} {
		if line, err := head.ReadLine(); err != nil || line != want {
			k.t.Fatalf("POST %s: %q (%v), want %q", api.RoomsPath, line, err, want)
		}
	}
	return func() (int, string) {
		k.t.Helper()
		if _, err := io.WriteString(conn, body); err != nil {
			k.t.Fatal(err)
		}
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			k.t.Fatalf("POST %s: %v", api.RoomsPath, err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil && !errors.Is(err, io.EOF) {
			k.t.Fatal(err)
		}
		return response.StatusCode, string(data)
	}
}

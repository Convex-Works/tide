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

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/store"
)

// These tests run klisi's main in a process of its own, as a container
// runtime does, and stop it with signals.

func TestMain(m *testing.M) {
	if os.Getenv("KLISI_TEST_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestASignalStopsKlisiOnceItsRequestsFinish(t *testing.T) {
	k := startMain(t)
	retro := k.startRequest(`{"name":"Retro"}`)
	k.signal(syscall.SIGTERM)
	k.waitForLog("klisi: stopping")

	// The request that was in flight is answered...
	status, body := retro()
	var created api.RoomInfo
	if err := json.Unmarshal([]byte(body), &created); status != http.StatusCreated || err != nil {
		t.Fatalf("the request in flight = %d %s, want the room created", status, body)
	}
	// ...and klisi exits cleanly, with what it did in the database.
	if state := k.wait(15 * time.Second); !state.Success() {
		t.Fatalf("klisi exited with %v", state)
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

func TestASecondSignalStopsKlisiAtOnce(t *testing.T) {
	k := startMain(t)
	// A request that will never finish would hold klisi for its whole grace.
	k.startRequest(`{"name":"Retro"}`)
	k.signal(syscall.SIGTERM)
	k.waitForLog("klisi: stopping")
	k.signal(syscall.SIGTERM)
	state := k.wait(shutdownGrace / 2)
	if status, ok := state.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("klisi exited with %v, want it killed by the second SIGTERM", state)
	}
}

// A klisiProcess is klisi's main running in a process of its own.
type klisiProcess struct {
	t      *testing.T
	cmd    *exec.Cmd
	addr   string
	dbPath string
	cookie *http.Cookie
	logs   chan string
	exited chan struct{}
}

// startMain starts main in a new process, configured as in development
// except for its address and database, and waits until it listens.
func startMain(t *testing.T) *klisiProcess {
	t.Helper()
	const secret, baseURL = "test-session-secret-long-enough-for-klisi", "http://127.0.0.1"
	k := &klisiProcess{
		t: t, dbPath: filepath.Join(t.TempDir(), "klisi.db"),
		logs: make(chan string, 1000), exited: make(chan struct{}),
	}
	k.cmd = exec.Command(os.Args[0], "-test.run=^$")
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "KLISI_") {
			k.cmd.Env = append(k.cmd.Env, variable)
		}
	}
	k.cmd.Env = append(k.cmd.Env,
		"KLISI_TEST_MAIN=1", "KLISI_DEV_MODE=true", "KLISI_ADDR=127.0.0.1:0",
		"KLISI_DB_PATH="+k.dbPath, "KLISI_BASE_URL="+baseURL, "KLISI_SESSION_SECRET="+secret)
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
	listening := k.waitForLog("klisi listening on ")
	k.addr = strings.TrimPrefix(listening[strings.Index(listening, "klisi listening on "):], "klisi listening on ")

	sessions := auth.NewSessions(secret, baseURL, nil)
	recorder := httptest.NewRecorder()
	if err := sessions.Set(recorder, auth.Session{Sub: "alice", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	k.cookie = recorder.Result().Cookies()[0]
	return k
}

// waitForLog waits for a line klisi logs that contains text, and returns it.
func (k *klisiProcess) waitForLog(text string) string {
	k.t.Helper()
	timeout := time.After(30 * time.Second)
	for {
		select {
		case line := <-k.logs:
			if strings.Contains(line, text) {
				return line
			}
		case <-k.exited:
			k.t.Fatalf("klisi exited before logging %q", text)
		case <-timeout:
			k.t.Fatalf("klisi never logged %q", text)
		}
	}
}

func (k *klisiProcess) signal(signal os.Signal) {
	k.t.Helper()
	if err := k.cmd.Process.Signal(signal); err != nil {
		k.t.Fatal(err)
	}
}

// wait waits up to within for klisi to exit, and returns how it did.
func (k *klisiProcess) wait(within time.Duration) *os.ProcessState {
	k.t.Helper()
	select {
	case <-k.exited:
		return k.cmd.ProcessState
	case <-time.After(within):
		k.t.Fatalf("klisi still ran %s later", within)
		return nil
	}
}

// startRequest sends alice's browser's request to create a room, and waits
// until klisi reads its body, which it holds back: the request is in flight
// until finish sends the body and returns klisi's answer.
func (k *klisiProcess) startRequest(body string) (finish func() (int, string)) {
	k.t.Helper()
	conn, err := net.Dial("tcp", k.addr)
	if err != nil {
		k.t.Fatal(err)
	}
	k.t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: klisi\r\nCookie: %s=%s\r\nX-Klisi-Csrf: 1\r\n"+
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

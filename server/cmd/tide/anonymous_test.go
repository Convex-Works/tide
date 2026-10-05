package main

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"tide/internal/api"
)

// An anonymous tide (ARCHITECTURE.md §4.1) keeps nothing: what one start
// made, the next doesn't know. These start the real binary with an
// external media server named, since none needs to run for them; the
// embedded one has tests of its own.
func startAnonymous(t *testing.T, dbPath string) *tideProcess {
	t.Helper()
	return startMainWith(t,
		"TIDE_MEDIA_URL=ws://127.0.0.1:1", "TIDE_MEDIA_PUBLIC_URL=ws://127.0.0.1:1",
		"TIDE_MEDIA_API_KEY=anonymous-test", "TIDE_MEDIA_API_SECRET="+strings.Repeat("m", 40),
		// Ignored: an anonymous tide's rooms live in memory.
		"TIDE_DB_PATH="+dbPath)
}

// call sends a request as the SPA does, and returns the answer and its body.
func (k *tideProcess) call(method, path, body string, cookie *http.Cookie) (*http.Response, string) {
	k.t.Helper()
	request, err := http.NewRequest(method, "http://"+k.addr+path, strings.NewReader(body))
	if err != nil {
		k.t.Fatal(err)
	}
	request.Header.Set("X-Tide-Csrf", "1")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		k.t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		k.t.Fatal(err)
	}
	return response, string(data)
}

func (k *tideProcess) stop() {
	k.t.Helper()
	k.signal(syscall.SIGTERM)
	if state := k.wait(15 * time.Second); !state.Success() {
		k.t.Fatalf("tide exited with %v", state)
	}
}

func TestAnonymousTideForgetsEverythingAtRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ignored.db")
	k := startAnonymous(t, dbPath)
	if !k.logged("tide: anonymous, media server external, recording off") {
		t.Fatalf("tide didn't log its mode; it logged %q", k.history)
	}

	// A browser without a session gets one from /api/me...
	response, body := k.call(http.MethodGet, api.MePath, "", nil)
	var me api.Me
	if err := json.Unmarshal([]byte(body), &me); response.StatusCode != http.StatusOK || err != nil {
		t.Fatalf("/api/me = %d %s", response.StatusCode, body)
	}
	if !me.Anonymous || me.Recording || me.Transcripts || !strings.HasPrefix(me.Sub, "anon:") {
		t.Fatalf("/api/me = %+v", me)
	}
	var cookie *http.Cookie
	for _, set := range response.Cookies() {
		if set.Name == "tide_session" {
			cookie = set
		}
	}
	if cookie == nil {
		t.Fatalf("/api/me set no session: %v", response.Header["Set-Cookie"])
	}
	// ...and owns the room it creates.
	response, body = k.call(http.MethodPost, api.RoomsPath, `{"name":"Standup"}`, cookie)
	var room api.RoomInfo
	if err := json.Unmarshal([]byte(body), &room); response.StatusCode != http.StatusCreated || err != nil {
		t.Fatalf("create = %d %s", response.StatusCode, body)
	}
	response, body = k.call(http.MethodGet, "/api/rooms/"+room.Slug, "", cookie)
	var public api.PublicRoomInfo
	if err := json.Unmarshal([]byte(body), &public); response.StatusCode != http.StatusOK || err != nil || !public.CanManage {
		t.Fatalf("lookup = %d %s, want the owner's", response.StatusCode, body)
	}
	k.stop()
	if _, err := os.Stat(dbPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("an anonymous tide wrote %s: %v", dbPath, err)
	}

	// The next start has neither the room nor the session.
	k = startAnonymous(t, dbPath)
	if response, body := k.call(http.MethodGet, "/api/rooms/"+room.Slug, "", cookie); response.StatusCode != http.StatusNotFound {
		t.Fatalf("the room after a restart = %d %s, want 404", response.StatusCode, body)
	}
	response, body = k.call(http.MethodGet, api.MePath, "", cookie)
	var again api.Me
	if err := json.Unmarshal([]byte(body), &again); response.StatusCode != http.StatusOK || err != nil {
		t.Fatalf("/api/me = %d %s", response.StatusCode, body)
	}
	if again.Sub == me.Sub || !strings.HasPrefix(again.Sub, "anon:") {
		t.Fatalf("after a restart the old cookie is %q again, want a new anonymous session", again.Sub)
	}
	k.stop()
}

// The embedded media server posts its webhooks to tide over loopback, or to
// the one address tide listens on.
func TestWebhookURL(t *testing.T) {
	for _, test := range []struct {
		addr net.Addr
		want string
	}{
		{&net.TCPAddr{IP: net.IPv6unspecified, Port: 8080}, "http://127.0.0.1:8080/api/webhooks/media"},
		{&net.TCPAddr{IP: net.IPv4zero, Port: 80}, "http://127.0.0.1:80/api/webhooks/media"},
		{&net.TCPAddr{Port: 8080}, "http://127.0.0.1:8080/api/webhooks/media"},
		{&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 41234}, "http://127.0.0.1:41234/api/webhooks/media"},
		{&net.TCPAddr{IP: net.ParseIP("10.0.0.5"), Port: 8080}, "http://10.0.0.5:8080/api/webhooks/media"},
		{&net.TCPAddr{IP: net.ParseIP("::1"), Port: 8080}, "http://[::1]:8080/api/webhooks/media"},
	} {
		if got := webhookURL(test.addr); got != test.want {
			t.Errorf("webhookURL(%v) = %q, want %q", test.addr, got, test.want)
		}
	}
}

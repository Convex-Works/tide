package httpapi

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
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/moil"
	"git.convex.works/ConvexWorks/moil/sdk/go/moiltest"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/config"
	"klisi/internal/store"
	"klisi/internal/transcripts"
)

// These tests run klisi as main does: the real handler over real HTTP, with a
// real SQLite store. Fake moil machines speak the real protocol to /moil, over
// HTTP and a WebSocket, while hosts use the /machines page's JSON API with
// real session cookies, as the SPA does.

func TestPairingAMachineEndToEnd(t *testing.T) {
	k := startKlisi(t, nil)
	alice := k.signIn(auth.Session{Sub: "alice"})
	bundle := transcribeBundle(t)

	// Before pairing, the page has no machines but knows what to tell the
	// host: which bundle to approve, and where the moil app pairs.
	page := alice.machines()
	if page.Machines == nil || len(page.Machines) != 0 {
		t.Fatalf("machines = %#v, want an empty list", page.Machines)
	}
	if want := (api.BundleInfo{Name: bundle.Name(), Version: bundle.Version(), Hash: bundle.Hash()}); page.Bundle != want {
		t.Fatalf("bundle = %#v, want %#v", page.Bundle, want)
	}
	if page.MoilURL != k.url+"/moil" {
		t.Fatalf("moil_url = %q, want %q", page.MoilURL, k.url+"/moil")
	}

	// The moil app pairs with that URL and opens the page with its code; the
	// host checks it's their machine.
	m := moiltest.New(t, page.MoilURL, moiltest.WithName("Studio"))
	code := m.StartPairing()
	var pairing api.PairingInfo
	alice.call(http.MethodGet, fill(api.PairingPath, code), http.StatusOK, &pairing)
	if pairing.Code != code || pairing.Name != "Studio" || pairing.OS != "linux" ||
		pairing.Arch != "x86_64" || pairing.AppVersion != "moiltest" {
		t.Fatalf("pairing = %#v", pairing)
	}
	if pairing.ExpiresAt <= time.Now().Unix() {
		t.Fatalf("expires_at = %d, want a time to come", pairing.ExpiresAt)
	}

	// Confirming pairs it with the host. It has never connected, and the
	// code is spent.
	var confirmed api.MachineInfo
	alice.call(http.MethodPost, fill(api.PairingConfirmPath, code), http.StatusCreated, &confirmed)
	if confirmed.ID == "" || confirmed.Name != "Studio" || confirmed.State != "offline" ||
		confirmed.Approved || confirmed.LastSeenAt != nil || time.Since(time.Unix(confirmed.PairedAt, 0)) > time.Minute {
		t.Fatalf("confirmed = %#v", confirmed)
	}
	if listed := alice.machine(confirmed.ID); listed != confirmed {
		t.Fatalf("listed = %#v, want %#v", listed, confirmed)
	}
	alice.call(http.MethodGet, fill(api.PairingPath, code), http.StatusNotFound, nil)

	// The app collects its token and connects: the machine is online, and
	// klisi offers it the very bundle the page names, for its owner to review.
	m.FinishPairing()
	if m.ID() != confirmed.ID {
		t.Fatalf("the machine was issued %q, the page confirmed %q", m.ID(), confirmed.ID)
	}
	m.Connect()
	m.WaitForBundle(page.Bundle.Hash)
	m.FetchBundle(page.Bundle.Hash)
	if machine := alice.machine(m.ID()); machine.State != "idle" || machine.Approved || machine.LastSeenAt == nil {
		t.Fatalf("connected machine = %#v, want idle, not approved, seen", machine)
	}

	// Its owner approves the bundle in the app; once klisi has taken the
	// approval in, the page says so.
	m.Approve(bundle)
	m.Sync()
	if machine := alice.machine(m.ID()); !machine.Approved {
		t.Fatalf("approved machine = %#v", machine)
	}
	m.SetState(moil.Paused)
	m.Sync()
	if machine := alice.machine(m.ID()); machine.State != "paused" {
		t.Fatalf("paused machine = %#v", machine)
	}

	// Gone offline, it keeps what it last reported, from the database.
	m.Disconnect()
	alice.waitForMachine(m.ID(), "offline and still approved", func(machine api.MachineInfo) bool {
		return machine.State == "offline" && machine.Approved && machine.LastSeenAt != nil
	})
}

func TestMachinesBelongToTheHostWhoConfirmed(t *testing.T) {
	k := startKlisi(t, nil)
	alice := k.signIn(auth.Session{Sub: "alice"})
	bob := k.signIn(auth.Session{Sub: "bob"})
	admin := k.signIn(auth.Session{Sub: "root", IsAdmin: true})

	// A confirmation that names another owner still pairs the machine with
	// the session that sent it.
	studio := moiltest.New(t, k.moilURL(), moiltest.WithName("Studio"))
	code := studio.StartPairing()
	confirm := fill(api.PairingConfirmPath, code)
	if status, _, body := k.request(http.MethodPost, confirm, `{"owner":"bob","owner_sub":"bob","sub":"bob"}`, alice.cookie, true); status != http.StatusCreated {
		t.Fatalf("confirm = %d %s", status, body)
	}
	studio.FinishPairing()
	studio.Connect()
	// Once confirmed, the code can't be confirmed again, by anyone.
	bob.call(http.MethodPost, confirm, http.StatusNotFound, nil)
	alice.pair(moiltest.WithName("Laptop"))
	bob.pair(moiltest.WithName("Bob's desktop"))

	// Each host sees their own machines, oldest first. Nobody else does, not
	// even an administrator: machines are personal.
	if names := machineNames(alice.machines()); !slices.Equal(names, []string{"Studio", "Laptop"}) {
		t.Fatalf("alice's machines = %q", names)
	}
	if names := machineNames(bob.machines()); !slices.Equal(names, []string{"Bob's desktop"}) {
		t.Fatalf("bob's machines = %q", names)
	}
	if names := machineNames(admin.machines()); len(names) != 0 {
		t.Fatalf("the administrator sees %q", names)
	}

	// Nor can they unpair hers: klisi answers exactly as it does for a
	// machine that doesn't exist, and the machine stays paired and online.
	unknown := bob.call(http.MethodDelete, fill(api.MachinePath, "m_nosuchmachine"), http.StatusNotFound, nil)
	for _, other := range []*host{bob, admin} {
		if body := other.call(http.MethodDelete, fill(api.MachinePath, studio.ID()), http.StatusNotFound, nil); body != unknown {
			t.Fatalf("%s removing alice's machine = %s, unknown machine = %s", other.sub, body, unknown)
		}
	}
	studio.Sync()
	if machine := alice.machine(studio.ID()); machine.State != "idle" {
		t.Fatalf("studio = %#v, want online", machine)
	}

	// Alice unpairs it: klisi closes its channel, its token stops working,
	// and it leaves her list.
	alice.call(http.MethodDelete, fill(api.MachinePath, studio.ID()), http.StatusNoContent, nil)
	if closeCode := studio.WaitClosed(); closeCode != 4401 {
		t.Fatalf("channel closed with %d, want 4401", closeCode)
	}
	if err := studio.TryConnect(); !errors.Is(err, moiltest.ErrUnauthorized) {
		t.Fatalf("reconnecting after removal: %v, want ErrUnauthorized", err)
	}
	if names := machineNames(alice.machines()); !slices.Equal(names, []string{"Laptop"}) {
		t.Fatalf("alice's machines after removal = %q", names)
	}
	alice.call(http.MethodDelete, fill(api.MachinePath, studio.ID()), http.StatusNotFound, nil)
}

func TestDeniedMachineIsTurnedAway(t *testing.T) {
	k := startKlisi(t, nil)
	alice := k.signIn(auth.Session{Sub: "alice"})
	m := moiltest.New(t, k.moilURL())
	code := m.StartPairing()

	alice.call(http.MethodPost, fill(api.PairingDenyPath, code), http.StatusNoContent, nil)
	var pairingErr *moiltest.PairingError
	if err := m.TryFinishPairing(); !errors.As(err, &pairingErr) || pairingErr.Code != "access_denied" {
		t.Fatalf("finishing a denied pairing: %v, want access_denied", err)
	}
	// The code is gone: it can't be looked up, confirmed or denied again.
	alice.call(http.MethodGet, fill(api.PairingPath, code), http.StatusNotFound, nil)
	alice.call(http.MethodPost, fill(api.PairingConfirmPath, code), http.StatusNotFound, nil)
	alice.call(http.MethodPost, fill(api.PairingDenyPath, code), http.StatusNotFound, nil)
	if names := machineNames(alice.machines()); len(names) != 0 {
		t.Fatalf("machines after denying = %q", names)
	}
}

func TestMachineRoutesNeedASignedInBrowser(t *testing.T) {
	k := startKlisi(t, nil)
	alice := k.signIn(auth.Session{Sub: "alice"})
	paired := alice.pair()
	waiting := moiltest.New(t, k.moilURL())
	code := waiting.StartPairing()
	forged := makeSessionCookie(t, config.Config{BaseURL: k.url, SessionSecret: "another-secret"}, auth.Session{Sub: "alice"})

	routes := []struct{ method, path string }{
		{http.MethodGet, api.MachinesPath},
		{http.MethodDelete, fill(api.MachinePath, paired.ID())},
		{http.MethodGet, fill(api.PairingPath, code)},
		{http.MethodPost, fill(api.PairingConfirmPath, code)},
		{http.MethodPost, fill(api.PairingDenyPath, code)},
	}
	for _, route := range routes {
		for _, cookie := range []*http.Cookie{nil, forged} {
			if status, _, body := k.request(route.method, route.path, "", cookie, true); status != http.StatusUnauthorized {
				t.Errorf("%s %s with cookie %v = %d %s, want 401", route.method, route.path, cookie != nil, status, body)
			}
		}
		if route.method == http.MethodGet {
			continue
		}
		if status, _, body := k.request(route.method, route.path, "", alice.cookie, false); status != http.StatusForbidden {
			t.Errorf("%s %s without the CSRF header = %d %s, want 403", route.method, route.path, status, body)
		}
	}

	// None of it did anything: the machine is still paired, and the code
	// still waits for its host.
	alice.machine(paired.ID())
	alice.call(http.MethodGet, fill(api.PairingPath, code), http.StatusOK, nil)
}

func TestStartingAPairingIsRateLimited(t *testing.T) {
	k := startKlisi(t, func(cfg *config.Config, _ *http.Server) { cfg.PairRateLimit = 2 })
	alice := k.signIn(auth.Session{Sub: "alice"})

	first := moiltest.New(t, k.moilURL())
	code := first.StartPairing()
	moiltest.New(t, k.moilURL()).StartPairing()
	response, err := k.client.Post(k.moilURL()+"/v1/pair", "application/json",
		strings.NewReader(`{"name":"One too many","os":"linux","arch":"x86_64","app_version":"0.1.0"}`))
	if err != nil {
		t.Fatal(err)
	}
	// The refusal is in moil's error format (moil spec §3), which the moil
	// app reads and shows its owner.
	var refusal struct{ Error, Message string }
	decodeErr := json.NewDecoder(response.Body).Decode(&refusal)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third pairing from one address = %d, want 429", response.StatusCode)
	}
	if decodeErr != nil || refusal.Error != "rate_limited" || refusal.Message == "" ||
		response.Header.Get("Retry-After") == "" {
		t.Fatalf("refusal = %+v (%v), Retry-After %q; want moil's error format",
			refusal, decodeErr, response.Header.Get("Retry-After"))
	}

	// The limit is on starting pairings only: from the same address, the
	// host still confirms, and the machine still collects its token and
	// connects.
	alice.call(http.MethodGet, api.MachinesPath, http.StatusOK, nil)
	alice.call(http.MethodPost, fill(api.PairingConfirmPath, code), http.StatusCreated, nil)
	first.FinishPairing()
	first.Connect()
	alice.machine(first.ID())
}

// Machines keep one WebSocket open for as long as they run. main serves klisi
// with read and write timeouts; hijacking the connection for the WebSocket
// must lift them, or every machine would drop off after 30 seconds.
func TestMachineChannelOutlivesServerTimeouts(t *testing.T) {
	const timeout = 200 * time.Millisecond
	k := startKlisi(t, func(_ *config.Config, server *http.Server) {
		server.ReadHeaderTimeout = timeout
		server.ReadTimeout = timeout
		server.WriteTimeout = timeout
	})
	alice := k.signIn(auth.Session{Sub: "alice"})
	bundle := transcribeBundle(t)

	// The timeouts are in force: an ordinary connection that outstays them
	// is closed.
	conn, err := net.Dial("tcp", strings.TrimPrefix(k.url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET /healthz HTTP/1.1\r\nHost: klisi\r\n\r\n")
	reader := bufio.NewReader(conn)
	health, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, health.Body)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("an idle connection: %v, want klisi to close it", err)
	}

	m := alice.pair()
	m.Connect()
	time.Sleep(5 * timeout)

	// Well past them, the channel carries messages both ways: the machine's
	// approval reaches klisi, and klisi offers it a job, assigns it and hears
	// how it ended.
	m.Approve(bundle)
	m.Sync()
	if machine := alice.machine(m.ID()); machine.State != "idle" || !machine.Approved {
		t.Fatalf("machine = %#v, want online and approved", machine)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run, err := k.moil.Submit(ctx, moil.Job{ID: "after-the-timeouts", Bundle: bundle, Eligible: moil.OwnedBy("alice")})
	if err != nil {
		t.Fatal(err)
	}
	attempt := m.NextAttempt()
	m.Sync()
	if machine := alice.machine(m.ID()); machine.State != "busy" {
		t.Fatalf("machine running a job = %#v, want busy", machine)
	}
	attempt.Succeed(nil)
	if _, err := run.Wait(ctx); err != nil {
		t.Fatalf("job: %v", err)
	}
}

// klisiServer is klisi served as main serves it, on a loopback port.
type klisiServer struct {
	t      *testing.T
	cfg    config.Config
	url    string
	client *http.Client
	moil   *moil.Server
}

// startKlisi serves klisi with a fresh database. configure, if set, adjusts
// the configuration and the HTTP server before klisi starts.
func startKlisi(t *testing.T, configure func(*config.Config, *http.Server)) *klisiServer {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "klisi.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// The listener comes first, so that klisi knows the base URL it's
	// served at, and tells machines the moil URL they can reach.
	server := httptest.NewUnstartedServer(nil)
	cfg := config.Config{
		BaseURL: "http://" + server.Listener.Addr().String(), SessionSecret: "test-session-secret",
		LiveKitURL: "ws://livekit.example", LiveKitAPIKey: "devkey",
		LiveKitAPISecret: "test-livekit-secret-with-enough-bytes",
	}
	if configure != nil {
		configure(&cfg, server.Config)
	}
	handler, background, err := New(cfg, nil, db, transcribeBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(server.Close)
	// Machines' WebSockets are hijacked, so the server doesn't wait for them:
	// closing moil ends them, as in main.
	t.Cleanup(func() { _ = background.Close() })
	client := &http.Client{Transport: &http.Transport{}, Timeout: 10 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	return &klisiServer{t: t, cfg: cfg, url: server.URL, client: client, moil: background.moil}
}

// transcribeBundle is the bundle main hands klisi.
func transcribeBundle(t *testing.T) *moil.Bundle {
	t.Helper()
	bundle, err := transcripts.Bundle()
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func (k *klisiServer) moilURL() string { return k.url + api.MoilBasePath }

// request sends a browser's request: with the session cookie, if any, and
// the CSRF header the SPA adds, if csrf.
func (k *klisiServer) request(method, path, body string, cookie *http.Cookie, csrf bool) (int, http.Header, string) {
	k.t.Helper()
	request, err := http.NewRequest(method, k.url+path, strings.NewReader(body))
	if err != nil {
		k.t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if csrf {
		request.Header.Set("X-Klisi-Csrf", "1")
	}
	response, err := k.client.Do(request)
	if err != nil {
		k.t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		k.t.Fatal(err)
	}
	return response.StatusCode, response.Header, string(data)
}

// A host is a signed-in browser on the /machines page.
type host struct {
	k      *klisiServer
	sub    string
	cookie *http.Cookie
}

func (k *klisiServer) signIn(session auth.Session) *host {
	return &host{k: k, sub: session.Sub, cookie: makeSessionCookie(k.t, k.cfg, session)}
}

// call makes a request as the SPA does, fails the test unless klisi answers
// want without letting the answer be cached, decodes the answer into out if
// set, and returns it.
func (h *host) call(method, path string, want int, out any) string {
	h.k.t.Helper()
	status, header, body := h.k.request(method, path, "", h.cookie, true)
	if status != want {
		h.k.t.Fatalf("%s %s as %s = %d %s, want %d", method, path, h.sub, status, body, want)
	}
	if cacheControl := header.Get("Cache-Control"); cacheControl != "no-store" {
		h.k.t.Fatalf("%s %s: Cache-Control = %q, want no-store", method, path, cacheControl)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(body), out); err != nil {
			h.k.t.Fatalf("%s %s = %s: %v", method, path, body, err)
		}
	}
	return body
}

func (h *host) machines() api.MachinesResponse {
	h.k.t.Helper()
	var page api.MachinesResponse
	h.call(http.MethodGet, api.MachinesPath, http.StatusOK, &page)
	return page
}

// machine returns one of the host's machines, as the page lists it.
func (h *host) machine(id string) api.MachineInfo {
	h.k.t.Helper()
	for _, machine := range h.machines().Machines {
		if machine.ID == id {
			return machine
		}
	}
	h.k.t.Fatalf("%s's machines don't include %s", h.sub, id)
	return api.MachineInfo{}
}

// waitForMachine waits until the page shows one of the host's machines as
// ready says, for what klisi learns without the machine's word, such as its
// connection dropping.
func (h *host) waitForMachine(id, what string, ready func(api.MachineInfo) bool) {
	h.k.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ready(h.machine(id)) {
		if time.Now().After(deadline) {
			h.k.t.Fatalf("machine %s never became %s: %#v", id, what, h.machine(id))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// pair pairs a new machine with the host, the way a person does: the moil
// app starts pairing, and the host confirms its code on the page.
func (h *host) pair(opts ...moiltest.Option) *moiltest.Machine {
	h.k.t.Helper()
	m := moiltest.New(h.k.t, h.k.moilURL(), opts...)
	h.call(http.MethodPost, fill(api.PairingConfirmPath, m.StartPairing()), http.StatusCreated, nil)
	m.FinishPairing()
	return m
}

// fill fills in a route's one wildcard.
func fill(route, value string) string {
	start, end := strings.Index(route, "{"), strings.Index(route, "}")
	return route[:start] + value + route[end+1:]
}

func machineNames(page api.MachinesResponse) []string {
	names := make([]string, 0, len(page.Machines))
	for _, machine := range page.Machines {
		names = append(names, machine.Name)
	}
	return names
}

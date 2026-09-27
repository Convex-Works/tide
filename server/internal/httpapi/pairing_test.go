package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"

	"git.convex.works/ConvexWorks/moil/sdk/go/moiltest"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/config"
)

// These tests hold klisi to the limits of ARCHITECTURE.md §8.1 and §15, the
// way clients meet them: over HTTP, with machines speaking moil and hosts
// using the /machines page's API with real session cookies. Where a test
// needs clients on different networks, they come through a reverse proxy
// klisi trusts, as in production, which says where each one is.

// behindProxy makes klisi trust the test's own address as its reverse
// proxy, so that requests can say which client they come from.
func behindProxy(cfg *config.Config) {
	_, loopback, _ := net.ParseCIDR("127.0.0.1/32")
	cfg.TrustedProxies = []*net.IPNet{loopback}
}

// moilAnswer is a moil endpoint's answer: a pairing, or an error in moil's
// format (moil spec §3).
type moilAnswer struct {
	status     int
	retryAfter string
	UserCode   string `json:"user_code"`
	Error      string `json:"error"`
	Message    string `json:"message"`
}

// startPairing sends POST /moil/v1/pair from the client at address (through
// the proxy; "" for the test's own), with the given content type and body.
func (k *klisiServer) startPairing(address, contentType, body string, header http.Header) moilAnswer {
	k.t.Helper()
	request, err := http.NewRequest(http.MethodPost, k.moilURL()+"/v1/pair", strings.NewReader(body))
	if err != nil {
		k.t.Fatal(err)
	}
	for name, values := range header {
		request.Header[name] = values
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if address != "" {
		request.Header.Set("X-Forwarded-For", address)
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
	answer := moilAnswer{status: response.StatusCode, retryAfter: response.Header.Get("Retry-After")}
	if err := json.Unmarshal(data, &answer); err != nil {
		k.t.Fatalf("POST /moil/v1/pair = %d %s: %v", response.StatusCode, data, err)
	}
	return answer
}

const pairRequest = `{"name":"Studio","os":"linux","arch":"x86_64","app_version":"0.1.0"}`

func TestStartingAPairingTakesOnlyJSON(t *testing.T) {
	k := startKlisi(t, func(cfg *config.Config, _ *http.Server) { cfg.PairRateLimit = 2 })
	alice := k.signIn(auth.Session{Sub: "alice"})

	// What a web page can send cross-origin without a CORS preflight: a
	// no-cors fetch with a string body, and forms of each encoding. Each
	// carries a pairing request moil itself would accept, and each is
	// refused, in moil's error format, before it reaches moil: no code.
	browser := http.Header{
		"Origin":         {"https://attacker.example"},
		"Sec-Fetch-Mode": {"no-cors"},
		"Sec-Fetch-Site": {"cross-site"},
	}
	for _, attempt := range []struct{ name, contentType, body string }{
		{"a no-cors fetch", "text/plain;charset=UTF-8", pairRequest},
		{"a text/plain form", "text/plain", strings.TrimSuffix(pairRequest, "}") + `,"pad":"=x"}`},
		{"a urlencoded form", "application/x-www-form-urlencoded", pairRequest},
		{"a multipart form", "multipart/form-data; boundary=x", pairRequest},
		{"a body of no declared type", "", pairRequest},
		{"a JSON look-alike", "application/jsonp", pairRequest},
	} {
		answer := k.startPairing("", attempt.contentType, attempt.body, browser)
		if answer.status != http.StatusUnsupportedMediaType || answer.Error != "invalid_request" ||
			answer.Message == "" || answer.UserCode != "" {
			t.Errorf("%s = %+v, want 415 invalid_request and no code", attempt.name, answer)
		}
	}

	// None of them counted against the limit of two: both JSON pairings from
	// the same address start, with parameters in the media type or without,
	// and pair; only a third is refused.
	answer := k.startPairing("", "application/json; charset=utf-8", pairRequest, nil)
	if answer.status != http.StatusOK || answer.UserCode == "" {
		t.Fatalf("a JSON pairing = %+v, want a code", answer)
	}
	var pairing api.PairingInfo
	alice.call(http.MethodGet, fill(api.PairingPath, answer.UserCode), http.StatusOK, &pairing)
	if pairing.Name != "Studio" {
		t.Fatalf("pairing = %+v", pairing)
	}
	m := alice.pair() // moiltest sends application/json, as the moil app does
	m.Connect()
	if answer := k.startPairing("", "application/json", pairRequest, nil); answer.status != http.StatusTooManyRequests {
		t.Fatalf("a third pairing = %+v, want 429", answer)
	}
}

func TestIPv6ClientsAreCountedByTheirSlash64(t *testing.T) {
	k := startKlisi(t, func(cfg *config.Config, _ *http.Server) {
		behindProxy(cfg)
		cfg.PairRateLimit = 2
	})
	for _, step := range []struct {
		from string
		want int
	}{
		// A subscriber's /64 is one client, whichever of its addresses it
		// uses: two pairings, then no more.
		{"2001:db8:5:6::1", http.StatusOK},
		{"2001:db8:5:6:ffff:ffff:ffff:fffe", http.StatusOK},
		{"2001:db8:5:6::abcd", http.StatusTooManyRequests},
		// The next /64 is another subscriber.
		{"2001:db8:5:7::1", http.StatusOK},
		{"2001:db8:5:7::2", http.StatusOK},
		{"2001:db8:5:7::3", http.StatusTooManyRequests},
		// IPv4 clients count by address, even written as IPv6.
		{"192.0.2.10", http.StatusOK},
		{"::ffff:192.0.2.10", http.StatusOK},
		{"192.0.2.10", http.StatusTooManyRequests},
		{"192.0.2.11", http.StatusOK},
	} {
		if answer := k.startPairing(step.from, "application/json", pairRequest, nil); answer.status != step.want {
			t.Fatalf("pairing from %s = %+v, want %d", step.from, answer, step.want)
		}
	}
}

func TestGuessingPairingCodesIsRateLimited(t *testing.T) {
	k := startKlisi(t, func(cfg *config.Config, _ *http.Server) { behindProxy(cfg) })
	const attacker, office = "198.51.100.7", "203.0.113.9"
	alice := k.signIn(auth.Session{Sub: "alice"})
	alice.from = office
	laptop := moiltest.New(t, k.moilURL(), moiltest.WithName("Alice's laptop"))
	code := laptop.StartPairing()

	// Mallory guesses codes with every route that takes one.
	mallory := k.signIn(auth.Session{Sub: "mallory"})
	mallory.from = attacker
	guess := guesser(code)
	routes := []struct{ method, path string }{
		{http.MethodGet, api.PairingPath},
		{http.MethodPost, api.PairingConfirmPath},
		{http.MethodPost, api.PairingDenyPath},
	}
	for i := range 20 {
		route := routes[i%len(routes)]
		mallory.call(route.method, fill(route.path, guess()), http.StatusNotFound, nil)
	}
	// Then klisi stops answering them, even with the right code, which it
	// neither reveals nor lets mallory confirm or deny.
	for _, route := range routes {
		body := mallory.call(route.method, fill(route.path, code), http.StatusTooManyRequests, nil)
		var refusal api.ErrorResponse
		if err := json.Unmarshal([]byte(body), &refusal); err != nil || refusal.Error == "" ||
			strings.Contains(body, "Alice") {
			t.Fatalf("%s as mallory, limited = %s, want klisi's error", route.method, body)
		}
	}

	// Alice, on her own network, finds her laptop under its code and pairs it.
	var pairing api.PairingInfo
	alice.call(http.MethodGet, fill(api.PairingPath, code), http.StatusOK, &pairing)
	if pairing.Name != "Alice's laptop" {
		t.Fatalf("pairing = %+v", pairing)
	}
	var confirmed api.MachineInfo
	alice.call(http.MethodPost, fill(api.PairingConfirmPath, code), http.StatusCreated, &confirmed)
	laptop.FinishPairing()
	if laptop.ID() != confirmed.ID || !slices.Equal(machineNames(alice.machines()), []string{"Alice's laptop"}) {
		t.Fatalf("alice paired %+v; the laptop is %s", confirmed, laptop.ID())
	}

	// Signing in as others doesn't buy mallory more guesses from one
	// address: it gets 60 a minute, whoever asks. Mallory's refused guesses
	// didn't count toward them; her 20 answered ones did.
	for n := 2; n <= 3; n++ {
		accomplice := k.signIn(auth.Session{Sub: fmt.Sprintf("mallory-%d", n)})
		accomplice.from = attacker
		for range 20 {
			accomplice.call(http.MethodGet, fill(api.PairingPath, guess()), http.StatusNotFound, nil)
		}
	}
	fresh := k.signIn(auth.Session{Sub: "mallory-4"})
	fresh.from = attacker
	fresh.call(http.MethodGet, fill(api.PairingPath, guess()), http.StatusTooManyRequests, nil)

	// Nobody else is held back: alice looks up another code.
	studio := moiltest.New(t, k.moilURL(), moiltest.WithName("Studio"))
	alice.call(http.MethodGet, fill(api.PairingPath, studio.StartPairing()), http.StatusOK, nil)
}

// guesser returns a function that makes up pairing codes other than code.
func guesser(code string) func() string {
	const alphabet = "BCDFGHJKLMNPQRSTVWXZ"
	return func() string {
		for {
			var letters [8]byte
			for i := range letters {
				letters[i] = alphabet[rand.IntN(len(alphabet))]
			}
			if guess := string(letters[:4]) + "-" + string(letters[4:]); guess != code {
				return guess
			}
		}
	}
}

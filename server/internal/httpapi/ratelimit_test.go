package httpapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"klisi/internal/api"
	"klisi/internal/auth"
	"klisi/internal/config"
	"klisi/internal/store"
)

// Guests may look rooms up as often as they may join; each lookup counts,
// found or not, so that 404s can't be used to test slugs faster than that.
// Signed-in hosts aren't limited.
func TestRoomLookupLimitsGuestsPerAddressAndHostsPerSub(t *testing.T) {
	stopLimiterClock(t)
	db, err := store.Open("file:lookup-limit?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.CreateRoom(context.Background(), store.Room{
		ID: "room", Slug: "abc-defg-hij", Name: "Weekly", OwnerSub: "owner", LobbyEnabled: true, CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	const limit = 3
	cfg := config.Config{
		BaseURL: "http://localhost:8080", SessionSecret: "test-session-secret",
		LiveKitAPIKey: "devkey", LiveKitAPISecret: "test-livekit-secret-with-enough-bytes",
		LiveKitPublicURL: "ws://public.example", JoinRateLimit: limit,
	}
	handler, background, err := New(cfg, nil, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = background.Close() })
	owner := makeSessionCookie(t, cfg, auth.Session{Sub: "owner", Email: "owner@example.com", Name: "Owner"})

	lookup := func(address, slug string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/rooms/"+slug, nil)
		request.RemoteAddr = address + ":1234"
		if cookie != nil {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	for i := range limit {
		slug, want := "abc-defg-hij", http.StatusOK
		if i%2 == 1 {
			slug, want = "zzz-zzzz-zzz", http.StatusNotFound
		}
		if response := lookup("192.0.2.1", slug, nil); response.Code != want {
			t.Fatalf("lookup %d: status = %d %s, want %d", i+1, response.Code, response.Body.String(), want)
		}
	}
	refused := lookup("192.0.2.1", "abc-defg-hij", nil)
	if refused.Code != http.StatusTooManyRequests {
		t.Fatalf("lookup over the limit: status = %d %s", refused.Code, refused.Body.String())
	}
	var body api.ErrorResponse
	if err := json.NewDecoder(refused.Body).Decode(&body); err != nil || body.Error == "" {
		t.Fatalf("429 body = %+v, %v; want the JSON error format", body, err)
	}
	if response := lookup("192.0.2.2", "abc-defg-hij", nil); response.Code != http.StatusOK {
		t.Fatalf("another address: status = %d %s", response.Code, response.Body.String())
	}
	// A signed-in host has a bucket of their own, keyed by sub: the guests'
	// limited address doesn't refuse them, but they have a limit too.
	for i := range limit {
		if response := lookup("192.0.2.1", "abc-defg-hij", owner); response.Code != http.StatusOK {
			t.Fatalf("owner lookup %d from a limited address: status = %d %s", i+1, response.Code, response.Body.String())
		}
	}
	if response := lookup("192.0.2.9", "zzz-zzzz-zzz", owner); response.Code != http.StatusTooManyRequests {
		t.Fatalf("owner lookup over the limit, from another address: status = %d %s", response.Code, response.Body.String())
	}
	other := makeSessionCookie(t, cfg, auth.Session{Sub: "other", Email: "other@example.com", Name: "Other"})
	if response := lookup("192.0.2.1", "abc-defg-hij", other); response.Code != http.StatusOK {
		t.Fatalf("another host on the same address: status = %d %s", response.Code, response.Body.String())
	}
	// Joins have their own bucket: looking rooms up didn't use it.
	join := httptest.NewRequest(http.MethodPost, "/api/rooms/abc-defg-hij/join", strings.NewReader(`{"name":"Guest"}`))
	join.RemoteAddr = "192.0.2.1:1234"
	join.Header.Set("Content-Type", "application/json")
	join.Header.Set("X-Klisi-Csrf", "1")
	joined := httptest.NewRecorder()
	handler.ServeHTTP(joined, join)
	if joined.Code != http.StatusOK {
		t.Fatalf("join after the lookups: status = %d %s", joined.Code, joined.Body.String())
	}
}

func TestIPRateLimiterBurstRefillAndIsolation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newRateLimiterWithClock(2, time.Minute, func() time.Time { return now })

	if !limiter.allow("192.0.2.1") || !limiter.allow("192.0.2.1") {
		t.Fatal("initial burst should be allowed")
	}
	if limiter.allow("192.0.2.1") {
		t.Fatal("request over burst capacity should be rejected")
	}
	if !limiter.allow("192.0.2.2") {
		t.Fatal("a separate IP should have a separate bucket")
	}

	now = now.Add(30 * time.Second)
	if !limiter.allow("192.0.2.1") {
		t.Fatal("one token should refill after half the window")
	}
	if limiter.allow("192.0.2.1") {
		t.Fatal("refill should not exceed the elapsed allowance")
	}
}

func TestIPRateLimiterCleansStaleBuckets(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newRateLimiterWithClock(1, time.Minute, func() time.Time { return now })
	if !limiter.allow("192.0.2.1") {
		t.Fatal("initial request should be allowed")
	}

	now = now.Add(3 * time.Minute)
	if !limiter.allow("192.0.2.2") {
		t.Fatal("new IP should be allowed")
	}
	limiter.mu.Lock()
	bucketCount := len(limiter.buckets)
	_, stalePresent := limiter.buckets["192.0.2.1"]
	limiter.mu.Unlock()
	if stalePresent || bucketCount != 1 {
		t.Fatalf("stale buckets were not cleaned: count = %d", bucketCount)
	}
}

func TestRateLimitMiddlewareReturnsJSON(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newRateLimiterWithClock(1, time.Minute, func() time.Time { return now })
	handler := withRateLimit(limiter, newClientIPResolver(nil), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	first := httptest.NewRequest(http.MethodGet, "/", nil)
	first.RemoteAddr = "192.0.2.1:1234"
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, first)
	if firstResponse.Code != http.StatusNoContent {
		t.Fatalf("first status = %d", firstResponse.Code)
	}

	second := httptest.NewRequest(http.MethodGet, "/", nil)
	second.RemoteAddr = "192.0.2.1:5678"
	secondResponse := httptest.NewRecorder()
	handler.ServeHTTP(secondResponse, second)
	if secondResponse.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d", secondResponse.Code)
	}
	var response api.ErrorResponse
	if err := json.NewDecoder(secondResponse.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error == "" {
		t.Fatal("rate-limit response should include an error")
	}
}

func TestClientIPResolver(t *testing.T) {
	newRequest := func(remote, forwarded string) *http.Request {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.RemoteAddr = remote
		if forwarded != "" {
			request.Header.Set("X-Forwarded-For", forwarded)
		}
		return request
	}
	// No trusted proxies: the TCP peer is always the client; a forged
	// X-Forwarded-For must never pick the bucket.
	open := newClientIPResolver(nil)
	if got := open.resolve(newRequest("203.0.113.7:1234", "10.0.0.1")); got != "203.0.113.7" {
		t.Fatalf("untrusted peer XFF must be ignored, got %q", got)
	}

	_, ingress, _ := net.ParseCIDR("10.0.0.0/8")
	behindProxy := newClientIPResolver([]*net.IPNet{ingress})

	// Trusted peer: rightmost untrusted XFF hop is the client.
	if got := behindProxy.resolve(newRequest("10.0.0.1:1234", "198.51.100.9, 10.0.0.2")); got != "198.51.100.9" {
		t.Fatalf("expected forwarded client, got %q", got)
	}
	// Client-forged prefix hops are ignored: only the hop the edge saw counts.
	if got := behindProxy.resolve(newRequest("10.0.0.1:1234", "1.2.3.4, 198.51.100.9")); got != "198.51.100.9" {
		t.Fatalf("expected edge-observed client, got %q", got)
	}
	// Malformed header falls back to the proxy address.
	if got := behindProxy.resolve(newRequest("10.0.0.1:1234", "not-an-ip")); got != "10.0.0.1" {
		t.Fatalf("malformed XFF should fall back to peer, got %q", got)
	}
	// Trusted peer with no header: peer address.
	if got := behindProxy.resolve(newRequest("10.0.0.1:1234", "")); got != "10.0.0.1" {
		t.Fatalf("expected peer, got %q", got)
	}
}

func TestRateLimitKeyCountsIPv6ByItsSlash64(t *testing.T) {
	resolver := newClientIPResolver(nil)
	for _, test := range []struct{ remote, want string }{
		{"192.0.2.1:1234", "192.0.2.1"},
		{"[::ffff:192.0.2.1]:1234", "192.0.2.1"},
		{"[2001:db8:1:2:3:4:5:6]:1234", "2001:db8:1:2::/64"},
		{"[2001:db8:1:2::9]:1234", "2001:db8:1:2::/64"},
		{"[fe80::1%en0]:1234", "fe80::/64"},
		{"not-an-address", "not-an-address"},
	} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.RemoteAddr = test.remote
		if got := resolver.key(request); got != test.want {
			t.Errorf("key(%s) = %q, want %q", test.remote, got, test.want)
		}
	}
}

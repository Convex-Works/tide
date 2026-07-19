package httpapi

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"klisi/internal/api"
)

func TestIPRateLimiterBurstRefillAndIsolation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newIPRateLimiterWithClock(2, time.Minute, func() time.Time { return now })

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
	limiter := newIPRateLimiterWithClock(1, time.Minute, func() time.Time { return now })
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
	limiter := newIPRateLimiterWithClock(1, time.Minute, func() time.Time { return now })
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

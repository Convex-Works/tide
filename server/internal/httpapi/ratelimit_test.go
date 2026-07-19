package httpapi

import (
	"encoding/json"
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
	handler := withRateLimit(limiter, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

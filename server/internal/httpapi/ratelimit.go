package httpapi

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"klisi/internal/httpx"
)

type tokenBucket struct {
	tokens    float64
	updatedAt time.Time
	lastSeen  time.Time
}

type ipRateLimiter struct {
	mu              sync.Mutex
	buckets         map[string]*tokenBucket
	capacity        float64
	refillPerSecond float64
	now             func() time.Time
	cleanupEvery    time.Duration
	staleAfter      time.Duration
	lastCleanup     time.Time
}

// orDefault keeps a zero-valued Config (tests, embedders) from denying every
// request: an unset ceiling means the published default, never zero.
func orDefault(configured, fallback int) int {
	if configured <= 0 {
		return fallback
	}
	return configured
}

func newIPRateLimiter(limit int, window time.Duration) *ipRateLimiter {
	return newIPRateLimiterWithClock(limit, window, time.Now)
}

func newIPRateLimiterWithClock(
	limit int,
	window time.Duration,
	now func() time.Time,
) *ipRateLimiter {
	if limit < 1 {
		limit = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	startedAt := now()
	return &ipRateLimiter{
		buckets:         make(map[string]*tokenBucket),
		capacity:        float64(limit),
		refillPerSecond: float64(limit) / window.Seconds(),
		now:             now,
		cleanupEvery:    window,
		staleAfter:      2 * window,
		lastCleanup:     startedAt,
	}
}

func (l *ipRateLimiter) allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastCleanup) >= l.cleanupEvery {
		for bucketKey, bucket := range l.buckets {
			if now.Sub(bucket.lastSeen) >= l.staleAfter {
				delete(l.buckets, bucketKey)
			}
		}
		l.lastCleanup = now
	}

	bucket, ok := l.buckets[key]
	if !ok {
		bucket = &tokenBucket{tokens: l.capacity, updatedAt: now, lastSeen: now}
		l.buckets[key] = bucket
	} else {
		elapsed := now.Sub(bucket.updatedAt).Seconds()
		if elapsed > 0 {
			bucket.tokens = min(l.capacity, bucket.tokens+elapsed*l.refillPerSecond)
			bucket.updatedAt = now
		}
		bucket.lastSeen = now
	}

	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

func withRateLimit(limiter *ipRateLimiter, ips *clientIPResolver, next http.Handler) http.Handler {
	return limited(limiter, ips, next, func(w http.ResponseWriter) {
		httpx.WriteError(w, http.StatusTooManyRequests, "Too many requests. Try again later.")
	})
}

// withMoilRateLimit limits a moil endpoint. Machines read errors in moil's
// own format, {"error": code, "message": text} (moil spec §3), and show the
// message to their owner.
func withMoilRateLimit(limiter *ipRateLimiter, ips *clientIPResolver, next http.Handler) http.Handler {
	return limited(limiter, ips, next, func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "60")
		httpx.WriteJSON(w, http.StatusTooManyRequests, map[string]string{
			"error":   "rate_limited",
			"message": "Too many machines started pairing from this network. Try again in a minute.",
		})
	})
}

func limited(limiter *ipRateLimiter, ips *clientIPResolver, next http.Handler, refuse func(http.ResponseWriter)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow(ips.resolve(r)) {
			w.Header().Set("Cache-Control", "no-store")
			refuse(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIPResolver resolves the real client address behind the configured
// trusted reverse proxies. With no trusted proxies the TCP peer is the
// client and X-Forwarded-For is ignored entirely — an untrusted peer must
// never be able to choose its own rate-limit bucket.
type clientIPResolver struct {
	trusted []*net.IPNet
}

func newClientIPResolver(trusted []*net.IPNet) *clientIPResolver {
	return &clientIPResolver{trusted: trusted}
}

func (c *clientIPResolver) resolve(r *http.Request) string {
	peer := remoteIP(r)
	if !c.isTrusted(peer) {
		return peer
	}
	// Walk X-Forwarded-For right to left, skipping our own trusted hops;
	// the first untrusted address is the client as seen by the edge proxy.
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop := strings.TrimSpace(hops[i])
		if hop == "" {
			continue
		}
		if net.ParseIP(hop) == nil {
			return peer // malformed header — fall back to the proxy address
		}
		if !c.isTrusted(hop) {
			return hop
		}
	}
	return peer
}

func (c *clientIPResolver) isTrusted(address string) bool {
	ip := net.ParseIP(address)
	if ip == nil {
		return false
	}
	for _, network := range c.trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func remoteIP(r *http.Request) string {
	remote := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		return host
	}
	return strings.Trim(remote, "[]")
}

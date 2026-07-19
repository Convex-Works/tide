package httpapi

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"klisi/internal/api"
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

func withRateLimit(limiter *ipRateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow(clientIP(r)) {
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusTooManyRequests, api.ErrorResponse{
				Error: "Too many requests. Try again later.",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	remote := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		return host
	}
	return strings.Trim(remote, "[]")
}

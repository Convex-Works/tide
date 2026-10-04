package httpapi

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"klisi/internal/auth"
	"klisi/internal/httpx"
)

type tokenBucket struct {
	tokens    float64
	updatedAt time.Time
	lastSeen  time.Time
}

// A rateLimiter keeps a token bucket per key: a client's address, as
// clientIPResolver.key gives it, or a signed-in host's sub.
type rateLimiter struct {
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

// limiterClock is the time New's rate limiters go by. Tests stop it, so that
// a slow machine can't refill a bucket in the middle of one.
var limiterClock = time.Now

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return newRateLimiterWithClock(limit, window, func() time.Time { return limiterClock() })
}

func newRateLimiterWithClock(
	limit int,
	window time.Duration,
	now func() time.Time,
) *rateLimiter {
	if limit < 1 {
		limit = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	startedAt := now()
	return &rateLimiter{
		buckets:         make(map[string]*tokenBucket),
		capacity:        float64(limit),
		refillPerSecond: float64(limit) / window.Seconds(),
		now:             now,
		cleanupEvery:    window,
		staleAfter:      2 * window,
		lastCleanup:     startedAt,
	}
}

func (l *rateLimiter) allow(key string) bool {
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

func withRateLimit(limiter *rateLimiter, ips *clientIPResolver, next http.Handler) http.Handler {
	return limited(limiter, ips, next, func(w http.ResponseWriter) {
		httpx.WriteError(w, http.StatusTooManyRequests, "Too many requests. Try again later.")
	})
}

// withLookupRateLimit limits a route that answers anyone, such as the room
// lookup, whose 404s would otherwise let a client test slugs as fast as it
// can ask (ARCHITECTURE.md §15). A guest is counted by client address; a
// signed-in host by their sub, in a bucket of their own, so colleagues behind
// one NAT don't share it and an account from a broad issuer can't test slugs
// without a limit either.
func withLookupRateLimit(limiter *rateLimiter, ips *clientIPResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := ips.key(r)
		if session, ok := auth.SessionFromContext(r.Context()); ok {
			key = "host:" + session.Sub
		}
		if !limiter.allow(key) {
			w.Header().Set("Cache-Control", "no-store")
			httpx.WriteError(w, http.StatusTooManyRequests, "Too many requests. Try again later.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withMoilRateLimit limits a moil endpoint. Machines read errors in moil's
// own format (see writeMoilError) and show the message to their owner. The
// code is the one the SDK refuses a pairing with when it has too many.
func withMoilRateLimit(limiter *rateLimiter, ips *clientIPResolver, next http.Handler) http.Handler {
	return limited(limiter, ips, next, func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "60")
		writeMoilError(w, http.StatusTooManyRequests, "temporarily_unavailable",
			"Too many machines started pairing from this network. Try again in a minute.")
	})
}

func limited(limiter *rateLimiter, ips *clientIPResolver, next http.Handler, refuse func(http.ResponseWriter)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow(ips.key(r)) {
			w.Header().Set("Cache-Control", "no-store")
			refuse(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Pairing codes are eight letters from twenty, so a signed-in host may look
// up, confirm and deny this many a minute (RFC 8628 §5.1): a person pairing
// a machine uses two. The per-address ceiling is a backstop against one
// client signed in as many hosts.
const (
	pairingCodesPerHost    = 20
	pairingCodesPerAddress = 60
)

// withPairingCodeLimit limits the pairing-code routes per signed-in host and
// per client address. It runs behind requireAuth, which guarantees the
// session. The host's bucket comes first, so that a host who is refused
// doesn't also use up the address's, which colleagues behind the same NAT
// share.
func withPairingCodeLimit(hosts, addresses *rateLimiter, ips *clientIPResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, _ := auth.SessionFromContext(r.Context())
		if !hosts.allow(session.Sub) || !addresses.allow(ips.key(r)) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Retry-After", "60")
			httpx.WriteError(w, http.StatusTooManyRequests, "Too many pairing codes tried. Wait a minute, then try again.")
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

// key is the client's rate-limit bucket: its IPv4 address, or the /64 its
// IPv6 address is in. A /64 is the smallest network an ISP assigns one
// subscriber, who can use any address in it, so counting addresses would
// give every client 2^64 buckets.
func (c *clientIPResolver) key(r *http.Request) string {
	address := c.resolve(r)
	ip, err := netip.ParseAddr(address)
	if err != nil {
		return address
	}
	ip = ip.WithZone("").Unmap()
	if ip.Is4() {
		return ip.String()
	}
	network, err := ip.Prefix(64)
	if err != nil {
		return address
	}
	return network.String()
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

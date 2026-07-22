package httpapi

import "net/http"

// The embedded SvelteKit shell contains its hydration bootstrap inline, and
// the bundled UI fonts are emitted as data URLs. Restrict scripts and fonts to
// those two production sources while keeping all external script origins
// blocked.
const contentSecurityPolicy = "default-src 'self'; script-src 'self' 'unsafe-inline'; font-src 'self' data:; img-src 'self' data:; media-src 'self' blob:; connect-src 'self' ws: wss:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

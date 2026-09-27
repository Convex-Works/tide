package httpapi

import (
	"mime"
	"net/http"

	"klisi/internal/httpx"
)

// writeMoilError answers a machine in moil's error format,
// {"error": code, "message": text} (moil spec §3), which the moil app reads
// and shows its owner.
func writeMoilError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, status, map[string]string{"error": code, "message": message})
}

// requireMoilJSON refuses a request to a moil endpoint whose body isn't
// declared JSON. The moil app always sends JSON; a web page can send a
// cross-origin POST without a CORS preflight only as text/plain or a form,
// so this keeps pages from starting pairings from their visitors' browsers.
// It runs before the rate limit, so such requests don't count against it.
func requireMoilJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeMoilError(w, http.StatusUnsupportedMediaType, "invalid_request",
				"Send the request as JSON, with Content-Type: application/json.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

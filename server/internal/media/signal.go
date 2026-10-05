package media

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"
)

// newSignalHandler forwards SignalPath and the paths under it to target, the
// media server's loopback HTTP address. Everything else answers 404, so the
// media server's API (/twirp/...) is never reachable through tide.
func newSignalHandler(target string) http.Handler {
	upstream, err := url.Parse(target)
	if err != nil {
		panic("media: signal target " + target + ": " + err.Error())
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.SetXForwarded()
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Judge the cleaned path, and forward only that, so that a path like
		// /rtc/../twirp/... can't reach past /rtc.
		cleaned := path.Clean(r.URL.Path)
		if cleaned != SignalPath && !strings.HasPrefix(cleaned, SignalPath+"/") {
			http.NotFound(w, r)
			return
		}
		if isUpgrade(r) {
			// tide's server gives every request read and write deadlines,
			// and a signaling WebSocket lives as long as the meeting. Go's
			// server drops them when the proxy hijacks the connection;
			// dropping them now also covers the wait for the media server
			// to accept the upgrade, during which an expired read deadline
			// would cancel the request.
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Time{})
			_ = rc.SetWriteDeadline(time.Time{})
		}
		r = r.Clone(r.Context())
		r.URL.Path = cleaned
		r.URL.RawPath = ""
		proxy.ServeHTTP(w, r)
	})
}

// isUpgrade reports whether r asks to switch protocols, as a WebSocket does.
func isUpgrade(r *http.Request) bool {
	if r.Header.Get("Upgrade") == "" {
		return false
	}
	for _, value := range r.Header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}

package media

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
)

// newSignalHandler forwards SignalPath and the paths under it to target, the
// media server's loopback HTTP address. Everything else answers 404, so the
// media server's API (/twirp/...) is never reachable through tide.
//
// Only GET requests without a body go through (ARCHITECTURE.md §15): every
// signaling request is a GET (the WebSocket and its validate endpoints), and
// a body nobody means to send would otherwise hold a connection open.
//
// tide's read and write deadlines stand until the media server switches the
// connection to a WebSocket. They need no clearing here: the proxy hijacks
// the connection on a 101, and hijacking clears both deadlines (Go 1.26,
// net/http/server.go:325, conn.hijackLocked: rwc.SetDeadline(time.Time{})).
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
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			refuse(w, http.StatusMethodNotAllowed)
			return
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			refuse(w, http.StatusBadRequest)
			return
		}
		r = r.Clone(r.Context())
		r.Body = http.NoBody
		r.URL.Path = cleaned
		r.URL.RawPath = ""
		proxy.ServeHTTP(w, r)
	})
}

// refuse answers a request the media server never sees and closes its
// connection, without waiting for a body the client may never send.
func refuse(w http.ResponseWriter, status int) {
	w.Header().Set("Connection", "close")
	http.Error(w, http.StatusText(status), status)
}

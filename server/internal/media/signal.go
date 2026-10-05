package media

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
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
//
// Each request counts in flight until the proxy returns, which for a
// WebSocket is when its copying has ended; drained tells Close when the
// media server's last words (its leave on shutdown) have been handed to the
// browsers.
func newSignalHandler(target string, flight *inFlight) http.Handler {
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
		flight.enter()
		defer flight.leave()
		proxy.ServeHTTP(w, r)
	})
}

// inFlight counts the signaling requests being forwarded.
type inFlight struct {
	mu sync.Mutex
	n  int
	// drained is made by a waiter and closed when n reaches zero.
	drained chan struct{}
}

func (f *inFlight) enter() {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
}

func (f *inFlight) leave() {
	f.mu.Lock()
	f.n--
	if f.n == 0 && f.drained != nil {
		close(f.drained)
		f.drained = nil
	}
	f.mu.Unlock()
}

// wait returns once nothing is in flight, or after timeout, and says which.
func (f *inFlight) wait(timeout time.Duration) bool {
	f.mu.Lock()
	if f.n == 0 {
		f.mu.Unlock()
		return true
	}
	if f.drained == nil {
		f.drained = make(chan struct{})
	}
	drained := f.drained
	f.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-drained:
		return true
	case <-timer.C:
		return false
	}
}

// refuse answers a request the media server never sees and closes its
// connection, without waiting for a body the client may never send.
func refuse(w http.ResponseWriter, status int) {
	w.Header().Set("Connection", "close")
	http.Error(w, http.StatusText(status), status)
}

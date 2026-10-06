package media

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A request /rtc refuses is answered at once, and its connection ends within
// tide's deadlines even when the client never sends the body it announced:
// nothing it does can hold a connection open.
func TestSignalHandlerTakesOnlyGETsWithoutABody(t *testing.T) {
	// Nothing listens upstream: every request here is refused before it
	// would be forwarded.
	signal := newSignalHandler("http://127.0.0.1:1", &inFlight{})
	mux := http.NewServeMux()
	mux.Handle(SignalPath, signal)
	mux.Handle(SignalPath+"/", signal)
	front := httptest.NewUnstartedServer(mux)
	const deadline = 2 * time.Second
	front.Config.ReadTimeout = deadline
	front.Config.WriteTimeout = deadline
	front.Start()
	t.Cleanup(front.Close)
	addr := strings.TrimPrefix(front.URL, "http://")

	for _, test := range []struct {
		name, request string
		status        int
		allow         string
	}{
		{
			"a POST that never sends its body",
			"POST /rtc HTTP/1.1\r\nHost: tide\r\nContent-Length: 1000\r\n\r\n",
			http.StatusMethodNotAllowed, "GET",
		},
		{
			"a WebSocket upgrade that announces a body it never sends",
			"GET /rtc HTTP/1.1\r\nHost: tide\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n" +
				"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
				"Content-Length: 1000\r\n\r\n",
			http.StatusBadRequest, "",
		},
		{
			"a chunked GET that never sends a chunk",
			"GET /rtc/validate HTTP/1.1\r\nHost: tide\r\nTransfer-Encoding: chunked\r\n\r\n",
			http.StatusBadRequest, "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			start := time.Now()
			if _, err := io.WriteString(conn, test.request); err != nil {
				t.Fatal(err)
			}
			// Long enough to see a connection outlive tide's deadlines.
			_ = conn.SetReadDeadline(start.Add(deadline + 3*time.Second))
			reader := bufio.NewReader(conn)
			response, err := http.ReadResponse(reader, nil)
			if err != nil {
				t.Fatalf("no answer: %v", err)
			}
			if waited := time.Since(start); waited > time.Second {
				t.Fatalf("answered only after %s", waited)
			}
			if response.StatusCode != test.status || response.Header.Get("Allow") != test.allow {
				t.Fatalf("answered %d (Allow %q), want %d (Allow %q)",
					response.StatusCode, response.Header.Get("Allow"), test.status, test.allow)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if _, err := reader.ReadByte(); err != io.EOF {
				t.Fatalf("the connection was still open %s after the request (%v)", time.Since(start), err)
			}
			if open := time.Since(start); open > deadline+time.Second {
				t.Fatalf("the connection stayed open %s, past tide's %s deadlines", open, deadline)
			}
		})
	}
}

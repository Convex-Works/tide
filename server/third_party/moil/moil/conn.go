package moil

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"git.convex.works/ConvexWorks/moil/sdk/go/internal/wire"
)

// A session is one control channel, from hello until it closes.
type session struct {
	machineID string
	conn      *websocket.Conn
	out       *queue[outbound]
	log       *slog.Logger
	// abort stops the writer, which then closes the connection.
	abort    context.CancelFunc
	overflow sync.Once
}

// maxOutbound bounds the messages waiting to be sent to a machine. A
// machine that lets more pile up isn't reading them, and is disconnected
// rather than let the service hold them.
const maxOutbound = 1024

// outbound is a message to send, or, if closeCode is set, a request to
// close the channel after the messages queued before it.
type outbound struct {
	msg       wire.Message
	closeCode websocket.StatusCode
	reason    string
}

// send queues a message without waiting for the network.
func (se *session) send(m wire.Message) { se.push(outbound{msg: m}) }

// close queues the close handshake after the messages already queued.
func (se *session) close(code websocket.StatusCode, reason string) {
	se.push(outbound{closeCode: code, reason: reason})
}

// push queues ob, or, if the machine lets too much pile up, disconnects
// it. It never waits, so the scheduler can call it.
func (se *session) push(ob outbound) {
	if !se.out.push(ob) {
		se.overflow.Do(func() {
			se.log.Warn("moil: a machine isn't reading what the service sends; disconnecting it", "machine", se.machineID)
			se.abort()
		})
	}
}

// handleConnect serves GET /v1/connect: it authenticates the machine,
// upgrades to a WebSocket and runs the control channel until it closes.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	s.connMu.Lock()
	if s.closing {
		s.connMu.Unlock()
		writeError(w, http.StatusServiceUnavailable, wire.ErrTemporarilyUnavailable, "The service is shutting down.")
		return
	}
	s.conns.Add(1)
	s.connMu.Unlock()
	defer s.conns.Done()

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept has answered the request
	}
	conn.SetReadLimit(wire.MaxMessageBytes)
	s.serveChannel(conn, rec)
}

func (s *Server) serveChannel(conn *websocket.Conn, rec MachineRecord) {
	defer conn.CloseNow()
	helloCtx, cancel := context.WithTimeout(s.baseCtx, s.cfg.PingInterval)
	hello, err := readHello(helloCtx, conn)
	cancel()
	if err != nil {
		var pe protocolError
		if errors.As(err, &pe) {
			s.log.Warn("moil: closing a control channel after a protocol error", "machine", rec.ID, "err", err)
			conn.Close(wire.CloseProtocolError, pe.reason)
		}
		return
	}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	sess := &session{machineID: rec.ID, conn: conn, out: newQueue[outbound](maxOutbound), log: s.log, abort: stop}
	var regErr error
	if err := s.call(func(sc *scheduler) { regErr = sc.register(sess, rec, hello) }); err != nil {
		regErr = err
	}
	switch {
	case errors.Is(regErr, errRemoved):
		conn.Close(wire.CloseUnauthorized, "machine removed")
		return
	case regErr != nil:
		conn.Close(websocket.StatusGoingAway, "the service is shutting down")
		return
	}

	writerDone, pingerDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(writerDone)
		sess.write(ctx, s.cfg.PingInterval)
	}()
	go func() {
		defer close(pingerDone)
		if sess.ping(ctx, s.cfg.PingInterval) {
			s.log.Info("moil: machine missed a pong; disconnecting it", "machine", rec.ID)
		}
	}()
	err = s.read(sess)
	s.post(func(sc *scheduler) { sc.disconnected(sess) })
	var pe protocolError
	if errors.As(err, &pe) {
		s.log.Warn("moil: closing a control channel after a protocol error", "machine", rec.ID, "err", err)
		// The writer sends what's queued, then closes.
		sess.close(wire.CloseProtocolError, pe.reason)
		<-writerDone
	}
	stop()
	conn.CloseNow()
	<-writerDone
	<-pingerDone
}

// protocolError is a machine breaking the protocol; the channel closes with
// 4400 and the reason.
type protocolError struct {
	reason string
	err    error
}

func (e protocolError) Error() string {
	if e.err != nil {
		return e.reason + ": " + e.err.Error()
	}
	return e.reason
}

func readHello(ctx context.Context, conn *websocket.Conn) (wire.Hello, error) {
	msg, err := readMessage(ctx, conn)
	if err != nil {
		return wire.Hello{}, err
	}
	hello, ok := msg.(wire.Hello)
	if !ok {
		return wire.Hello{}, protocolError{reason: "the first message must be hello"}
	}
	return hello, nil
}

func readMessage(ctx context.Context, conn *websocket.Conn) (wire.Message, error) {
	typ, data, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageText {
		return nil, protocolError{reason: "messages must be text frames"}
	}
	msg, err := wire.Decode(data)
	if err != nil {
		return nil, protocolError{reason: "invalid message", err: err}
	}
	return msg, nil
}

// read hands the machine's messages to the scheduler until the channel
// fails or the machine breaks the protocol.
func (s *Server) read(sess *session) error {
	for {
		msg, err := readMessage(context.Background(), sess.conn)
		if err != nil {
			return err
		}
		switch msg.(type) {
		case wire.Unknown:
			continue // from a newer machine
		case wire.State, wire.Approved, wire.Bid, wire.Decline, wire.Event, wire.Renew, wire.Done:
			if !s.post(func(sc *scheduler) { sc.handle(sess, msg) }) {
				return ErrClosed
			}
		default:
			return protocolError{reason: "unexpected " + msg.Type() + " message"}
		}
	}
}

// write sends queued messages until it's asked to close the channel, a
// write fails or ctx is done, and closes the connection.
func (se *session) write(ctx context.Context, timeout time.Duration) {
	defer se.conn.CloseNow()
	for {
		ob, ok := se.out.pop(ctx.Done())
		if !ok {
			return
		}
		if ob.closeCode != 0 {
			se.conn.Close(ob.closeCode, ob.reason)
			return
		}
		data, err := wire.Encode(ob.msg)
		if err != nil {
			se.log.Error("moil: encoding a control message", "type", ob.msg.Type(), "err", err)
			continue
		}
		wctx, cancel := context.WithTimeout(ctx, timeout)
		err = se.conn.Write(wctx, websocket.MessageText, data)
		cancel()
		if err != nil {
			return
		}
	}
}

// ping pings the machine every interval until ctx is done or the channel
// closes. It closes the channel, and reports true, when a pong doesn't
// arrive before the next ping is due.
func (se *session) ping(ctx context.Context, interval time.Duration) (missed bool) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, interval)
		err := se.conn.Ping(pctx)
		cancel()
		if err != nil {
			missed = errors.Is(err, context.DeadlineExceeded)
			se.conn.CloseNow()
			return missed
		}
	}
}

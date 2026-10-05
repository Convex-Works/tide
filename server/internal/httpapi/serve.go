package httpapi

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"time"
)

// Serve runs tide: server, whose handler New built, on listener, and
// background beside it, until ctx is done or the server fails. Then it stops
// them in the order that lets each finish what it started:
//
//  1. The HTTP server stops accepting connections and waits up to grace for
//     the requests in flight. Lobby streams end at once rather than hold it
//     for all of grace. Requests still running after grace are cut off.
//  2. The reconcilers, and in anonymous mode the room sweep, stop starting
//     new work.
//  3. With transcripts on, moil disconnects the machines, whose WebSockets
//     the HTTP server doesn't wait for, since they are hijacked; it ends
//     every transcript job with moil.ErrClosed and saves what machines last
//     reported. With them off there is no moil to close.
//  4. Serve waits for the reconcilers and the jobs' followers to return.
//
// When Serve returns, nothing uses the store New was given, and the caller
// may stop the media server and close the store (ARCHITECTURE.md §2). Serve returns nil when ctx ended it, or the error the server
// failed with.
func Serve(ctx context.Context, server *http.Server, listener net.Listener, background *Background, grace time.Duration) error {
	server.RegisterOnShutdown(background.lobby.EndStreams)
	// The reconcilers outlive ctx: they keep recording what machines finish
	// while the requests in flight complete.
	runCtx, stopRun := context.WithCancel(context.WithoutCancel(ctx))
	defer stopRun()
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		background.Run(runCtx)
	}()
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()

	// served sends one value, when server.Serve returns; drained says
	// whether it has been received.
	var err error
	drained := false
	select {
	case <-ctx.Done():
	case err = <-served:
		drained = true
		if errors.Is(err, http.ErrServerClosed) {
			err = nil // someone else shut it down
		} else {
			log.Printf("tide: serving failed: %v", err)
		}
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.WithoutCancel(ctx), grace)
	if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
		log.Printf("tide: cutting off requests still running after %s: %v", grace, shutdownErr)
		_ = server.Close()
	}
	cancelShutdown()
	if !drained {
		if serveErr := <-served; !errors.Is(serveErr, http.ErrServerClosed) {
			err = serveErr
		}
	}

	stopRun()
	if closeErr := background.Close(); closeErr != nil {
		log.Printf("tide: close moil: %v", closeErr)
	}
	<-ran
	return err
}

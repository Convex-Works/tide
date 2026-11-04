package speakers

import (
	"context"
	"testing"
	"time"
)

// The SDK calls OnTrackUnpublished holding the room's lock, and refresh asks
// for that lock holding mu. So the callback must return without mu, or the two
// wait on each other, and everything waiting to end a recording waits too.
func TestAnUnpublishDoesntWaitForTheObserversLock(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := &observer{cancel: cancel, stopped: true, activity: NewActivity()}
	callback := o.callback().ParticipantCallback.OnTrackUnpublished

	o.mu.Lock()
	returned := make(chan struct{})
	go func() {
		callback(nil, nil)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Error("the callback waited for the observer's lock")
	}
	o.mu.Unlock()
	o.work.Wait()
}

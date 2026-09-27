package recording

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"time"
)

const (
	// removeTimeout bounds how long deleting a recording or a room waits for
	// storage to remove its files before answering: the reconciler retries
	// whatever is left.
	removeTimeout = 10 * time.Second
	// removeDueTimeout bounds how long a reconciler pass spends removing the
	// files due for removal, so that storage being out of reach doesn't hold
	// up the next pass, which also heals lost egress webhooks.
	removeDueTimeout = 30 * time.Second
	// removalBatch is the most due removals one reconciler pass attempts.
	removalBatch = 500
)

// A Remover removes objects from storage. Removing an object that isn't
// there succeeds.
type Remover interface {
	Remove(ctx context.Context, key string) error
}

// A RemovalQueue lists the objects to remove from storage, each from a due
// time (store.Store's object_removals).
type RemovalQueue interface {
	DueRemovals(ctx context.Context, now int64, limit int) ([]string, error)
	RemovalDone(ctx context.Context, key string, now int64) error
}

// RemoveQueued removes keys, each queued for removal by now, from storage,
// and takes each one storage removed off the queue. It carries on past
// storage refusing a key, and stops at the first failure to reach storage
// at all, as the keys after it would fail the same way, each after the
// storage client's retries. It returns what went wrong: the keys storage
// didn't remove stay queued, and the reconciler retries them every minute.
func RemoveQueued(ctx context.Context, objects Remover, queue RemovalQueue, keys []string, now int64) error {
	var errs []error
	for i, key := range keys {
		if err := objects.Remove(ctx, key); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", key, err))
			if unreachable(err) {
				if left := len(keys) - i - 1; left > 0 {
					errs = append(errs, fmt.Errorf("left %d more for later: storage is out of reach", left))
				}
				break
			}
			continue
		}
		if err := queue.RemovalDone(ctx, key, now); err != nil {
			errs = append(errs, fmt.Errorf("unqueue %s: %w", key, err))
		}
	}
	return errors.Join(errs...)
}

// RemoveDeleted removes the files a deletion just queued, as its request
// ends, and logs those left for the reconciler. It outlives the request, for
// up to removeTimeout, so that a client hanging up doesn't stop it.
func RemoveDeleted(ctx context.Context, objects Remover, queue RemovalQueue, keys []string, now int64) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeTimeout)
	defer cancel()
	if err := RemoveQueued(ctx, objects, queue, keys, now); err != nil {
		log.Printf("recordings: storage kept deleted files; the reconciler will retry: %v", err)
	}
}

// unreachable reports whether err says storage couldn't be reached, or
// didn't answer in time, rather than that it refused one object.
func unreachable(err error) bool {
	var netErr net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.As(err, &netErr)
}

// removeDue removes the objects due for removal, for up to
// h.removeDueTimeout, and keeps those storage didn't remove for the next
// pass.
func (h *Handler) removeDue(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, h.removeDueTimeout)
	defer cancel()
	now := h.now().Unix()
	keys, err := h.store.DueRemovals(ctx, now, removalBatch)
	if err != nil {
		log.Printf("recording reconciler: list due removals: %v", err)
		return
	}
	if err := RemoveQueued(ctx, h.objects, h.store, keys, now); err != nil {
		log.Printf("recording reconciler: storage kept files due for removal; retrying next pass: %v", err)
	}
}

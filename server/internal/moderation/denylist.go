package moderation

import (
	"sync"
	"time"
)

// Denylist tracks kicked participants for as long as their cached admission
// token could still be valid. Self-hosted LiveKit does not revoke JWTs on
// RemoveParticipant, so a kicked guest can reconnect directly to the SFU;
// tide closes that hole by re-removing any denylisted identity the moment
// it rejoins (see Handler.EnforceOnJoin, driven by the participant_joined
// webhook).
type Denylist struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]time.Time // room + "\x00" + identity → expiry
	now     func() time.Time
}

// NewDenylist creates a denylist whose entries last for ttl, which must be at
// least the admission-token TTL so a ban outlives every cached token.
func NewDenylist(ttl time.Duration) *Denylist {
	return &Denylist{ttl: ttl, entries: map[string]time.Time{}, now: time.Now}
}

func (d *Denylist) Ban(room, identity string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune()
	d.entries[room+"\x00"+identity] = d.now().Add(d.ttl)
}

func (d *Denylist) Banned(room, identity string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prune()
	expiry, ok := d.entries[room+"\x00"+identity]
	return ok && d.now().Before(expiry)
}

// prune drops expired entries; called under d.mu on every access so the map
// stays bounded by the number of kicks within one TTL window.
func (d *Denylist) prune() {
	now := d.now()
	for key, expiry := range d.entries {
		if !now.Before(expiry) {
			delete(d.entries, key)
		}
	}
}

package lobby

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

const DefaultRequestTTL = 10 * time.Minute

var (
	ErrRequestNotFound = errors.New("lobby request not found")
	ErrRequestResolved = errors.New("lobby request already resolved")
)

type Status string

const (
	StatusWaiting  Status = "waiting"
	StatusAdmitted Status = "admitted"
	StatusDenied   Status = "denied"
	StatusExpired  Status = "expired"
)

type Request struct {
	ID       string
	RoomSlug string
	Name     string
	Created  int64
	Status   Status
	Token    string
	WSURL    string
	Decision <-chan struct{}
}

type entry struct {
	id       string
	roomSlug string
	name     string
	created  int64
	status   Status
	token    string
	wsURL    string
	decision chan struct{}
	timer    *time.Timer
}

type Registry struct {
	mu       sync.Mutex
	requests map[string]*entry
	watchers map[string]map[chan struct{}]struct{}
	ttl      time.Duration
	now      func() time.Time
}

func NewRegistry(ttl time.Duration) *Registry {
	if ttl <= 0 {
		ttl = DefaultRequestTTL
	}
	return &Registry{
		requests: make(map[string]*entry),
		watchers: make(map[string]map[chan struct{}]struct{}),
		ttl:      ttl,
		now:      time.Now,
	}
}

func (r *Registry) Add(roomSlug, name string) (Request, error) {
	for range 8 {
		id, err := randomHex(16)
		if err != nil {
			return Request{}, err
		}
		r.mu.Lock()
		if _, exists := r.requests[id]; exists {
			r.mu.Unlock()
			continue
		}
		item := &entry{
			id: id, roomSlug: roomSlug, name: name, created: r.now().Unix(),
			status: StatusWaiting, decision: make(chan struct{}),
		}
		r.requests[id] = item
		item.timer = time.AfterFunc(r.ttl, func() { _ = r.Expire(id) })
		r.notifyLocked(roomSlug)
		view := requestView(item)
		r.mu.Unlock()
		return view, nil
	}
	return Request{}, errors.New("could not allocate a lobby request id")
}

func (r *Registry) Get(id string) (Request, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.requests[id]
	if !ok {
		return Request{}, false
	}
	return requestView(item), true
}

func (r *Registry) Approve(id, token, wsURL string) error {
	return r.resolve(id, StatusAdmitted, token, wsURL)
}

func (r *Registry) Deny(id string) error {
	return r.resolve(id, StatusDenied, "", "")
}

func (r *Registry) Expire(id string) error {
	return r.resolve(id, StatusExpired, "", "")
}

func (r *Registry) resolve(id string, status Status, token, wsURL string) error {
	r.mu.Lock()
	item, ok := r.requests[id]
	if !ok {
		r.mu.Unlock()
		return ErrRequestNotFound
	}
	if item.status != StatusWaiting {
		r.mu.Unlock()
		return ErrRequestResolved
	}
	item.status = status
	item.token = token
	item.wsURL = wsURL
	if item.timer != nil {
		item.timer.Stop()
	}
	close(item.decision)
	r.notifyLocked(item.roomSlug)
	r.mu.Unlock()

	time.AfterFunc(r.ttl, func() { r.remove(id) })
	return nil
}

func (r *Registry) Pending(roomSlug string) []Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	requests := make([]Request, 0)
	for _, item := range r.requests {
		if item.roomSlug == roomSlug && item.status == StatusWaiting {
			requests = append(requests, requestView(item))
		}
	}
	return requests
}

// HasPending reports whether changing this room's address would strand a
// waiting guest whose admission request still references the old slug.
func (r *Registry) HasPending(roomSlug string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, item := range r.requests {
		if item.roomSlug == roomSlug && item.status == StatusWaiting {
			return true
		}
	}
	return false
}

func (r *Registry) Subscribe(roomSlug string) (<-chan struct{}, func()) {
	updates := make(chan struct{}, 1)
	r.mu.Lock()
	if r.watchers[roomSlug] == nil {
		r.watchers[roomSlug] = make(map[chan struct{}]struct{})
	}
	r.watchers[roomSlug][updates] = struct{}{}
	r.mu.Unlock()
	return updates, func() {
		r.mu.Lock()
		delete(r.watchers[roomSlug], updates)
		if len(r.watchers[roomSlug]) == 0 {
			delete(r.watchers, roomSlug)
		}
		r.mu.Unlock()
	}
}

func (r *Registry) notifyLocked(roomSlug string) {
	for updates := range r.watchers[roomSlug] {
		select {
		case updates <- struct{}{}:
		default:
		}
	}
}

func (r *Registry) remove(id string) {
	r.mu.Lock()
	delete(r.requests, id)
	r.mu.Unlock()
}

func requestView(item *entry) Request {
	return Request{
		ID: item.id, RoomSlug: item.roomSlug, Name: item.name, Created: item.created,
		Status: item.status, Token: item.token, WSURL: item.wsURL, Decision: item.decision,
	}
}

func randomHex(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

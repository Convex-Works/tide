package moil

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"git.convex.works/ConvexWorks/moil/sdk/go/internal/wire"
)

// A Pairing is a machine waiting for a signed-in user to confirm its code.
// Show it on the page at Config.VerificationURL, so the user can check
// it's their machine before confirming.
type Pairing struct {
	// Code is the user code, formatted XXXX-XXXX.
	Code       string
	Name       string // what the machine's owner calls it
	OS         string // e.g. macos, linux, windows
	Arch       string // e.g. aarch64, x86_64
	AppVersion string
	Expires    time.Time
}

// ErrUnknownCode is returned for a pairing code that isn't pending: it was
// mistyped, already confirmed or denied, or it expired.
var ErrUnknownCode = errors.New("moil: no machine is waiting with that code; it may have expired")

type pairingStatus int

const (
	pairingPending    pairingStatus = iota
	pairingConfirming               // ConfirmPairing is writing to the Store
	pairingConfirmed                // the token waits for the machine to fetch it
	pairingDenied
)

type pairing struct {
	deviceKey string // SHA-256 of the device code
	userKey   string // the user code without its hyphen
	info      Pairing
	status    pairingStatus
	lastPoll  time.Time
	machineID string
	token     string
}

// pairings holds the pairings in progress. They're short-lived and never
// persisted: a service restart asks machines to pair again.
type pairings struct {
	mu       sync.Mutex
	byDevice map[string]*pairing
	byUser   map[string]*pairing
}

func newPairings() *pairings {
	return &pairings{byDevice: map[string]*pairing{}, byUser: map[string]*pairing{}}
}

// userCodeAlphabet has no vowels, so codes don't spell words, and no
// letters that look like digits (RFC 8628 §6.1).
const userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"

// normalizeUserCode returns the code's eight letters, upper case, or "" if
// it can't be a user code. Users may type it in any case, with or without
// the hyphen.
func normalizeUserCode(code string) string {
	code = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))
	if len(code) != 8 {
		return ""
	}
	for i := 0; i < len(code); i++ {
		if !strings.ContainsRune(userCodeAlphabet, rune(code[i])) {
			return ""
		}
	}
	return code
}

// start registers a new pairing and returns its device code and user code.
// When limit pairings are in progress, it makes room by forgetting the
// oldest one nobody confirmed, so that anonymous requests can't lock
// everyone out of pairing; it fails only if every one was confirmed.
func (p *pairings) start(req wire.PairRequest, ttl time.Duration, limit int) (deviceCode string, info Pairing, ok bool) {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.purge(now)
	if len(p.byDevice) >= limit && !p.evictOldest() {
		return "", Pairing{}, false
	}
	deviceCode = randomToken(32)
	var userKey string
	for userKey == "" || p.byUser[userKey] != nil {
		userKey = randomString(userCodeAlphabet, 8)
	}
	pr := &pairing{
		deviceKey: hashSecret(deviceCode),
		userKey:   userKey,
		info: Pairing{
			Code:       userKey[:4] + "-" + userKey[4:],
			Name:       req.Name,
			OS:         req.OS,
			Arch:       req.Arch,
			AppVersion: req.AppVersion,
			Expires:    now.Add(ttl),
		},
	}
	p.byDevice[pr.deviceKey] = pr
	p.byUser[userKey] = pr
	return deviceCode, pr.info, true
}

// purge forgets expired pairings.
func (p *pairings) purge(now time.Time) {
	for _, pr := range p.byDevice {
		if pr.expired(now) {
			p.remove(pr)
		}
	}
}

// evictOldest forgets the oldest pairing that is pending or denied. It
// reports false if there's none.
func (p *pairings) evictOldest() bool {
	var oldest *pairing
	for _, pr := range p.byDevice {
		if (pr.status == pairingPending || pr.status == pairingDenied) && (oldest == nil || pr.info.Expires.Before(oldest.info.Expires)) {
			oldest = pr
		}
	}
	if oldest != nil {
		p.remove(oldest)
	}
	return oldest != nil
}

// expired reports whether the pairing's code has expired. One being
// confirmed doesn't expire until the Store has the machine.
func (pr *pairing) expired(now time.Time) bool {
	return pr.status != pairingConfirming && !now.Before(pr.info.Expires)
}

func (p *pairings) remove(pr *pairing) {
	delete(p.byDevice, pr.deviceKey)
	delete(p.byUser, pr.userKey)
}

// pending returns the pending pairing with this user code.
func (p *pairings) pending(code string) (*pairing, bool) {
	pr := p.byUser[normalizeUserCode(code)]
	if pr == nil || pr.status != pairingPending || !time.Now().Before(pr.info.Expires) {
		return nil, false
	}
	return pr, true
}

// poll answers a machine polling for its token (spec §4): the token once,
// or the RFC 8628 error to send.
func (p *pairings) poll(deviceCode string, interval time.Duration) (machineID, token, errCode string) {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	pr := p.byDevice[hashSecret(deviceCode)]
	if pr == nil || pr.expired(now) {
		if pr != nil {
			p.remove(pr)
		}
		return "", "", wire.ErrExpiredToken
	}
	// Allow some jitter: a poll that was delayed makes the next one early.
	early := !pr.lastPoll.IsZero() && now.Sub(pr.lastPoll) < interval*3/4
	pr.lastPoll = now
	if early {
		return "", "", wire.ErrSlowDown
	}
	switch pr.status {
	case pairingDenied:
		return "", "", wire.ErrAccessDenied
	case pairingConfirmed:
		p.remove(pr) // a device code yields its token once
		return pr.machineID, pr.token, ""
	default:
		return "", "", wire.ErrAuthorizationPending
	}
}

// PendingPairing returns the machine waiting with this user code, for the
// confirmation page to show. The code may be typed in any case, with or
// without its hyphen.
func (s *Server) PendingPairing(code string) (Pairing, bool) {
	s.pairings.mu.Lock()
	defer s.pairings.mu.Unlock()
	pr, ok := s.pairings.pending(code)
	if !ok {
		return Pairing{}, false
	}
	return pr.info, true
}

// ConfirmPairing pairs the machine waiting with this user code, with owner
// as its owner. Call it only for a signed-in user who confirmed the code on
// the service's page. The machine is in the Store when ConfirmPairing
// returns, and receives its token the next time it polls, within
// Config.PairingTTL. It returns ErrUnknownCode if no machine is waiting
// with the code.
func (s *Server) ConfirmPairing(ctx context.Context, code, owner string) (Machine, error) {
	if owner == "" {
		return Machine{}, errors.New("moil: ConfirmPairing needs an owner")
	}
	s.pairings.mu.Lock()
	pr, ok := s.pairings.pending(code)
	if ok {
		pr.status = pairingConfirming
	}
	s.pairings.mu.Unlock()
	if !ok {
		return Machine{}, ErrUnknownCode
	}

	token := "moil_" + randomToken(32)
	rec := MachineRecord{
		ID:        "m_" + randomID(12),
		Owner:     owner,
		TokenHash: hashSecret(token),
		PairedAt:  time.Now().UTC(),
		Report: MachineReport{
			Name:       pr.info.Name,
			OS:         pr.info.OS,
			Arch:       pr.info.Arch,
			AppVersion: pr.info.AppVersion,
		},
	}
	err := s.store.AddMachine(ctx, rec)

	s.pairings.mu.Lock()
	defer s.pairings.mu.Unlock()
	if err != nil {
		pr.status = pairingPending
		return Machine{}, err
	}
	// The machine gets a fresh TTL to collect its token, so that one
	// confirmed as its code expires doesn't leave a record behind that no
	// machine can use.
	pr.status, pr.machineID, pr.token = pairingConfirmed, rec.ID, token
	pr.info.Expires = time.Now().Add(s.cfg.PairingTTL)
	s.log.Info("moil: machine paired", "machine", rec.ID, "name", rec.Report.Name, "owner", owner)
	return rec.machine(), nil
}

// DenyPairing rejects the machine waiting with this user code; its next
// poll learns so. It returns ErrUnknownCode if no machine is waiting with
// the code.
func (s *Server) DenyPairing(code string) error {
	s.pairings.mu.Lock()
	defer s.pairings.mu.Unlock()
	pr, ok := s.pairings.pending(code)
	if !ok {
		return ErrUnknownCode
	}
	pr.status = pairingDenied
	return nil
}

// hashSecret returns the hex SHA-256 of a token or device code. Only
// hashes are kept, and lookups by hash don't leak the secret through
// timing.
func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// randomToken returns n random bytes, base64url-encoded.
func randomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

const idAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// randomID returns n random alphanumeric characters.
func randomID(n int) string { return randomString(idAlphabet, n) }

// randomString returns n characters drawn uniformly from alphabet, which
// must be shorter than 256 characters.
func randomString(alphabet string, n int) string {
	// Reject bytes past the largest multiple of len(alphabet), so every
	// character is equally likely.
	limit := 256 - 256%len(alphabet)
	out := make([]byte, 0, n)
	buf := make([]byte, n*2)
	for len(out) < n {
		rand.Read(buf)
		for _, b := range buf {
			if int(b) < limit && len(out) < n {
				out = append(out, alphabet[int(b)%len(alphabet)])
			}
		}
	}
	return string(out)
}

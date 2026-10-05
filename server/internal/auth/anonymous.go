package auth

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"net/http"
	"strings"
	"time"

	"tide/internal/httpx"
)

// Anonymous sessions are how a deployment without sign-in knows who created
// a room (ARCHITECTURE.md §4.1). One is the same signed cookie as a host's,
// with an anon: sub, no email, no name and no administrator capability: it
// owns the rooms its browser created and grants nothing else.
const (
	// AnonymousSubPrefix starts every anonymous session's sub.
	AnonymousSubPrefix = "anon:"
	// AnonymousSessionLifetime is longer than a host's day, because an
	// anonymous session is the only key to the rooms its browser created.
	AnonymousSessionLifetime = 30 * 24 * time.Hour
)

// anonymousSubEncoding writes 16 random bytes as 26 lowercase base32
// characters.
var anonymousSubEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// IsAnonymous reports whether session is an anonymous one.
func IsAnonymous(session Session) bool {
	return strings.HasPrefix(session.Sub, AnonymousSubPrefix)
}

// errNotAnonymous refuses an anonymous session from a deployment with
// sign-in, whose Read would refuse it anyway.
var errNotAnonymous = errors.New("auth: anonymous sessions exist only without sign-in")

// IssueAnonymous sets a new anonymous session on w, and returns it. Only
// sessions from NewAnonymousSessions issue them.
func (s *Sessions) IssueAnonymous(w http.ResponseWriter) (Session, error) {
	if !s.anonymous {
		return Session{}, errNotAnonymous
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return Session{}, err
	}
	session := Session{Sub: AnonymousSubPrefix + anonymousSubEncoding.EncodeToString(random)}
	return s.setFor(w, session, AnonymousSessionLifetime)
}

// AnonymousLogin is GET /api/auth/login without sign-in: it keeps the
// browser's session if it has one, issues an anonymous one if not, and
// redirects to next, as a sign-in would.
func (s *Sessions) AnonymousLogin(w http.ResponseWriter, r *http.Request) {
	if _, ok := SessionFromContext(r.Context()); !ok {
		if _, err := s.IssueAnonymous(w); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "Could not start a session. Try again.")
			return
		}
	}
	http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusFound)
}

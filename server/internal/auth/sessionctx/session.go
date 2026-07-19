// Package sessionctx holds the auth session context value below packages that
// both produce and consume HTTP responses, avoiding a package import cycle.
package sessionctx

import "context"

type Session struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Exp   int64  `json:"exp"`
	// SID identifies this cookie for server-side revocation on logout.
	SID string `json:"sid,omitempty"`
}

type contextKey struct{}

func WithSession(ctx context.Context, session Session) context.Context {
	return context.WithValue(ctx, contextKey{}, session)
}

func FromContext(ctx context.Context) (Session, bool) {
	session, ok := ctx.Value(contextKey{}).(Session)
	return session, ok
}

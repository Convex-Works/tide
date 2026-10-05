package auth

import (
	"context"

	"tide/internal/auth/sessionctx"
)

func WithSession(ctx context.Context, session Session) context.Context {
	return sessionctx.WithSession(ctx, session)
}

func SessionFromContext(ctx context.Context) (Session, bool) {
	return sessionctx.FromContext(ctx)
}

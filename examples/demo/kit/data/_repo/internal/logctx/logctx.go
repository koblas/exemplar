// Package logctx carries the request-scoped logger, which has the request id
// and caller attached. Code on a request path logs through From(ctx).
package logctx

import (
	"context"
	"log/slog"
)

type key struct{}

// With returns ctx carrying l.
func With(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, key{}, l)
}

// From returns the request's logger, or the default one outside a request.
func From(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(key{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

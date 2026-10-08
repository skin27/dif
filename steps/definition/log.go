package definition

import (
	"context"
	"log"
)

type loggerKey struct{}

// WithLogger returns ctx carrying the flow's logger, for processors to log to.
func WithLogger(ctx context.Context, l *log.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// Logger returns the flow's logger from ctx, or the standard logger if it has none.
func Logger(ctx context.Context) *log.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*log.Logger); ok && l != nil {
		return l
	}
	return log.Default()
}

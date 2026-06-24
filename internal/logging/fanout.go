package logging

import (
	"context"
	"errors"
	"log/slog"
)

// fanout is an slog.Handler that dispatches each record to every wrapped
// handler. It lets icebeam write structured JSON to the log file while
// simultaneously mirroring human-friendly text to stderr on a TTY.
type fanout []slog.Handler

// Enabled reports true if any wrapped handler is enabled at the given level.
func (f fanout) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle dispatches the record to every wrapped handler, joining any errors.
func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// WithAttrs returns a fanout whose wrapped handlers each carry the attrs.
func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make(fanout, len(f))
	for i, h := range f {
		next[i] = h.WithAttrs(attrs)
	}
	return next
}

// WithGroup returns a fanout whose wrapped handlers each open the group.
func (f fanout) WithGroup(name string) slog.Handler {
	next := make(fanout, len(f))
	for i, h := range f {
		next[i] = h.WithGroup(name)
	}
	return next
}

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

// newLogger builds the process logger: plain lines on stderr, like the ad-hoc
// prints before it, with levels so a gateway service can filter them.
// TALARIA_LOG_LEVEL sets the level (debug, info, warn, error); info shows
// everything Talaria prints today.
func newLogger(stderr io.Writer, getenv func(string) string) *slog.Logger {
	level := slog.LevelInfo
	if v := getenv("TALARIA_LOG_LEVEL"); v != "" {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			fmt.Fprintf(stderr, "warning: TALARIA_LOG_LEVEL=%q is not a level (debug, info, warn, error); showing info and above\n", v)
			level = slog.LevelInfo
		}
	}
	return slog.New(newPlainHandler(stderr, level))
}

// plainHandler writes one line per record: the message as-is up to info,
// prefixed for warnings and errors. Output matches the prints it replaces, so
// scripts that read the log keep working.
type plainHandler struct {
	w     io.Writer
	level slog.Level
	mu    sync.Mutex
}

func newPlainHandler(w io.Writer, level slog.Level) *plainHandler {
	return &plainHandler{w: w, level: level}
}

func (h *plainHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *plainHandler) Handle(_ context.Context, r slog.Record) error {
	prefix := ""
	switch {
	case r.Level >= slog.LevelError:
		prefix = "error: "
	case r.Level >= slog.LevelWarn:
		prefix = "warning: "
	}
	msg := prefix + r.Message
	r.Attrs(func(a slog.Attr) bool {
		msg += fmt.Sprintf(" %s=%v", a.Key, a.Value)
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := fmt.Fprintln(h.w, msg)
	return err
}

func (h *plainHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &withAttrs{h: h, attrs: attrs}
}
func (h *plainHandler) WithGroup(name string) slog.Handler { return &withGroup{h: h, group: name} }

type withAttrs struct {
	h     *plainHandler
	attrs []slog.Attr
}

func (w *withAttrs) Enabled(ctx context.Context, l slog.Level) bool { return w.h.Enabled(ctx, l) }

func (w *withAttrs) Handle(ctx context.Context, r slog.Record) error {
	for _, a := range w.attrs {
		r.AddAttrs(a)
	}
	return w.h.Handle(ctx, r)
}

func (w *withAttrs) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &withAttrs{h: w.h, attrs: append(w.attrs, attrs...)}
}

func (w *withAttrs) WithGroup(name string) slog.Handler { return &withGroup{h: w.h, group: name} }

type withGroup struct {
	h     *plainHandler
	group string
}

func (w *withGroup) Enabled(ctx context.Context, l slog.Level) bool { return w.h.Enabled(ctx, l) }

func (w *withGroup) Handle(ctx context.Context, r slog.Record) error {
	nr := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(slog.Attr{Key: w.group + "." + a.Key, Value: a.Value})
		return true
	})
	return w.h.Handle(ctx, nr)
}

func (w *withGroup) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &withAttrs{h: w.h, attrs: attrs}
}

func (w *withGroup) WithGroup(name string) slog.Handler {
	return &withGroup{h: w.h, group: w.group + "." + name}
}

package applog

// ─────────────────────────────────────────────────────────────────────────────
// Structured logging — Phase 1 of the fmt.Print* -> real levels migration
// (see logbuffer.go for the pre-existing capture mechanism this builds on).
//
// slogRingBufferHandler is a slog.Handler that writes formatted lines
// directly to the real stdout (bypassing CaptureStdout's pipe — the same
// bypass FprintReal uses) and adds a LogEntry straight to ServerLogs with
// the record's REAL level, instead of going through the pipe +
// classifyLogLevel guessing path every remaining fmt.Print* call site still
// uses. Migrated (slog.Info/Warn/Error/Debug) and unmigrated (fmt.Print*)
// call sites coexist indefinitely — writing to realStdout instead of the
// piped os.Stdout means this path never re-enters processLogChunkSafely, so
// there's no risk of a migrated line being classified (and added to
// ServerLogs) twice.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

type slogRingBufferHandler struct {
	level slog.Level
}

func (h *slogRingBufferHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *slogRingBufferHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(levelTag(r.Level))
	b.WriteByte(' ')
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Any())
		return true
	})
	line := b.String()

	target := os.Stdout
	if realStdout != nil {
		target = realStdout
	}
	fmt.Fprintln(target, line)

	ServerLogs.add(LogEntry{Time: r.Time, Level: levelString(r.Level), Message: line})
	return nil
}

// WithAttrs/WithGroup complete the slog.Handler interface but are unused —
// no call site in this codebase builds a sub-logger via slog.Logger.With,
// so there are no attrs/groups to carry between calls yet.
func (h *slogRingBufferHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *slogRingBufferHandler) WithGroup(_ string) slog.Handler      { return h }

func levelTag(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "DEBUG"
	case l < slog.LevelWarn:
		return "INFO"
	case l < slog.LevelError:
		return "WARN"
	default:
		return "ERROR"
	}
}

func levelString(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "debug"
	case l < slog.LevelWarn:
		return "info"
	case l < slog.LevelError:
		return "warning"
	default:
		return "error"
	}
}

// InitLogger sets the process-wide default slog logger, reading the minimum
// level from LOG_LEVEL (debug|info|warn|error, case-insensitive; defaults
// to info). Debug is opt-in — meant for active troubleshooting, too
// verbose for everyday operation. Must run after CaptureStdout so
// realStdout is already set.
func InitLogger() {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(&slogRingBufferHandler{level: level}))
}

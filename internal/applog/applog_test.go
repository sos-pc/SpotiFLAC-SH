package applog

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestLevelTagAndLevelString(t *testing.T) {
	cases := []struct {
		level   slog.Level
		tag     string
		display string
	}{
		{slog.LevelDebug, "DEBUG", "debug"},
		{slog.LevelInfo, "INFO", "info"},
		{slog.LevelWarn, "WARN", "warning"},
		{slog.LevelError, "ERROR", "error"},
	}
	for _, c := range cases {
		if got := levelTag(c.level); got != c.tag {
			t.Errorf("levelTag(%v) = %q, want %q", c.level, got, c.tag)
		}
		if got := levelString(c.level); got != c.display {
			t.Errorf("levelString(%v) = %q, want %q", c.level, got, c.display)
		}
	}
}

// TestSlogRingBufferHandlerEnabledRespectsMinLevel is the regression test
// for LOG_LEVEL filtering: a handler configured for Warn must report
// Debug/Info as disabled (so slog skips formatting/Handle entirely) while
// still allowing Warn/Error through.
func TestSlogRingBufferHandlerEnabledRespectsMinLevel(t *testing.T) {
	h := &slogRingBufferHandler{level: slog.LevelWarn}
	ctx := context.Background()

	if h.Enabled(ctx, slog.LevelDebug) {
		t.Error("Debug should be disabled when min level is Warn")
	}
	if h.Enabled(ctx, slog.LevelInfo) {
		t.Error("Info should be disabled when min level is Warn")
	}
	if !h.Enabled(ctx, slog.LevelWarn) {
		t.Error("Warn should be enabled when min level is Warn")
	}
	if !h.Enabled(ctx, slog.LevelError) {
		t.Error("Error should be enabled when min level is Warn")
	}
}

// TestSlogRingBufferHandlerAddsRealLevelToServerLogs is the core regression
// test for the whole point of this handler: a migrated log call must land
// in ServerLogs with its ACTUAL level, not a guess from classifyLogLevel —
// verified here by using a message that classifyLogLevel would misclassify
// (contains neither "error"/"warn"/"fatal" nor any of its other trigger
// words) but is logged at Warn.
func TestSlogRingBufferHandlerAddsRealLevelToServerLogs(t *testing.T) {
	origServerLogs := ServerLogs
	origRealStdout := realStdout
	defer func() {
		ServerLogs = origServerLogs
		realStdout = origRealStdout
	}()
	ServerLogs = &logRingBuffer{}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()
	realStdout = w

	h := &slogRingBufferHandler{level: slog.LevelDebug}
	logger := slog.New(h)
	logger.Warn("catalog database unavailable, continuing without it", "reason", "disk full")

	snap := ServerLogs.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("len(snap) = %d, want 1", len(snap))
	}
	if snap[0].Level != "warning" {
		t.Errorf("Level = %q, want %q (classifyLogLevel would have said %q for this message)",
			snap[0].Level, "warning", classifyLogLevel(snap[0].Message))
	}
	if snap[0].Message == "" {
		t.Error("Message is empty")
	}
}

// TestSlogRingBufferHandlerWritesToRealStdoutNotThePipe confirms the
// handler bypasses CaptureStdout's pipe entirely (writes straight to
// realStdout) — the doc comment's claim that a migrated line can never be
// double-classified by processLogChunkSafely depends on this.
func TestSlogRingBufferHandlerWritesToRealStdoutNotThePipe(t *testing.T) {
	origServerLogs := ServerLogs
	origStdout := os.Stdout
	origRealStdout := realStdout
	defer func() {
		ServerLogs = origServerLogs
		os.Stdout = origStdout
		realStdout = origRealStdout
	}()
	ServerLogs = &logRingBuffer{}

	// os.Stdout points at a pipe nothing reads from — if the handler wrote
	// there instead of realStdout, this test would hang once the pipe's
	// kernel buffer filled (it won't, for one short line, but the point is
	// realStdout must be the actual destination, checked below).
	_, unreadPipeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer unreadPipeW.Close()
	os.Stdout = unreadPipeW

	realR, realW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer realR.Close()
	realStdout = realW

	h := &slogRingBufferHandler{level: slog.LevelDebug}
	slog.New(h).Info("hello from the handler")
	realW.Close()

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		buf.ReadFrom(realR)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out reading realStdout — handler did not write there")
	}
	if !bytes.Contains(buf.Bytes(), []byte("hello from the handler")) {
		t.Errorf("realStdout content = %q, want it to contain the logged message", buf.String())
	}
}

func TestInitLoggerReadsLogLevelEnv(t *testing.T) {
	origDefault := slog.Default()
	origEnv, hadEnv := os.LookupEnv("LOG_LEVEL")
	defer func() {
		slog.SetDefault(origDefault)
		if hadEnv {
			os.Setenv("LOG_LEVEL", origEnv)
		} else {
			os.Unsetenv("LOG_LEVEL")
		}
	}()

	os.Setenv("LOG_LEVEL", "error")
	InitLogger()
	h, ok := slog.Default().Handler().(*slogRingBufferHandler)
	if !ok {
		t.Fatalf("slog.Default().Handler() = %T, want *slogRingBufferHandler", slog.Default().Handler())
	}
	if h.level != slog.LevelError {
		t.Errorf("level = %v, want %v for LOG_LEVEL=error", h.level, slog.LevelError)
	}

	os.Unsetenv("LOG_LEVEL")
	InitLogger()
	h, _ = slog.Default().Handler().(*slogRingBufferHandler)
	if h.level != slog.LevelInfo {
		t.Errorf("level = %v, want %v (default) when LOG_LEVEL is unset", h.level, slog.LevelInfo)
	}
}

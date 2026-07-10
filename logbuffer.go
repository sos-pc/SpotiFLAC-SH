package main

// ─────────────────────────────────────────────────────────────────────────────
// Backend log capture — makes fmt.Print* output visible from the web UI
//
// Every fmt.Print* call in the backend (60+ scattered call sites) already
// writes to os.Stdout, which `docker logs` captures but the frontend never
// sees. Rather than threading a logger through every call site, captureStdout
// intercepts the process's os.Stdout itself via an os.Pipe: the real fd keeps
// receiving every byte unchanged (docker logs / terminal output is
// untouched), and complete \n-terminated lines are also parsed into an
// in-memory ring buffer and broadcast over SSE (server_log) so the Debug Logs
// page can show backend output without shelling into the container.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"time"
)

// LogEntry is one backend log line, exposed via GET /api/v1/admin/logs
// (snapshot) and the server_log SSE event (live tail).
type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"` // info | warning | error
	Message string    `json:"message"`
}

// serverLogBufferSize bounds memory use; large enough to survive a burst of
// download activity between two Debug Logs page loads.
const serverLogBufferSize = 1000

type logRingBuffer struct {
	mu      sync.RWMutex
	entries []LogEntry
	hub     *SSEHub
}

// serverLogs is populated as soon as captureStdout runs (before the SSE hub
// exists), so early startup lines are still in the snapshot even though they
// couldn't be broadcast live. attachHub wires up live broadcasting once the
// JobManager (and its hub) is constructed.
var serverLogs = &logRingBuffer{}

func (b *logRingBuffer) attachHub(hub *SSEHub) {
	b.mu.Lock()
	b.hub = hub
	b.mu.Unlock()
}

func (b *logRingBuffer) add(e LogEntry) {
	b.mu.Lock()
	b.entries = append(b.entries, e)
	if len(b.entries) > serverLogBufferSize {
		b.entries = b.entries[len(b.entries)-serverLogBufferSize:]
	}
	hub := b.hub
	b.mu.Unlock()

	if hub != nil {
		hub.publish(JobEvent{Type: "server_log", Data: e})
	}
}

func (b *logRingBuffer) snapshot() []LogEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]LogEntry, len(b.entries))
	copy(out, b.entries)
	return out
}

// classifyLogLevel is a best-effort heuristic: existing fmt.Print* calls
// carry no structured level, so we infer one from common substrings to give
// the frontend something to color-code and filter on.
func classifyLogLevel(line string) string {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "fatal"), strings.Contains(lower, "panic"), strings.Contains(lower, "error"):
		return "error"
	case containsNonZeroFailure(lower):
		return "error"
	case strings.Contains(lower, "warn"):
		return "warning"
	default:
		return "info"
	}
}

// containsNonZeroFailure reports whether line mentions a failure that
// actually happened — a bare "failed", or "failed=N"/"failed: N" for N > 0.
// Structured summary lines like "retag scanned=3 tagged=1 failed=0" are
// common in this codebase (api_admin.go's repair/rebuild logging) and would
// otherwise always render as errors.
func containsNonZeroFailure(lower string) bool {
	idx := strings.Index(lower, "failed")
	if idx < 0 {
		return false
	}
	rest := strings.TrimLeft(lower[idx+len("failed"):], " ")
	return !strings.HasPrefix(rest, "=0") && !strings.HasPrefix(rest, ": 0") && !strings.HasPrefix(rest, ":0")
}

// captureStdout tees the process's stdout so complete log lines are also
// captured into serverLogs, without disturbing anything currently writing to
// os.Stdout — including in-place \r progress updates (Downloaded: X MB),
// which are passed through byte-for-byte unchanged and simply never form a
// \n-terminated token, so they never flood the ring buffer/SSE feed; only the
// final summary line after each progress run does.
//
// Must be called once, as early as possible in main(), before any other
// goroutine starts writing to stdout.
func captureStdout() {
	r, w, err := os.Pipe()
	if err != nil {
		return
	}
	orig := os.Stdout
	os.Stdout = w

	go func() {
		buf := make([]byte, 32*1024)
		var pending []byte
		for {
			n, readErr := r.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				orig.Write(chunk)
				pending = append(pending, chunk...)
				for {
					idx := bytes.IndexByte(pending, '\n')
					if idx < 0 {
						break
					}
					line := strings.TrimRight(string(pending[:idx]), "\r")
					pending = pending[idx+1:]
					if strings.TrimSpace(line) != "" {
						serverLogs.add(LogEntry{Time: time.Now(), Level: classifyLogLevel(line), Message: line})
					}
				}
				// Safety net against a pathological unterminated stream
				// (e.g. a stuck \r progress loop) growing pending forever.
				if len(pending) > 1<<20 {
					pending = pending[len(pending)-64*1024:]
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
}

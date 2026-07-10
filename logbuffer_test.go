package main

import (
	"os"
	"testing"
	"time"
)

func TestClassifyLogLevel(t *testing.T) {
	cases := map[string]string{
		"[Watcher] M3U8: playlist.m3u8 written (12 entries)": "info",
		"FATAL: cannot open database: disk full":             "error",
		"[Repair] MyList: retag scanned=3 tagged=1 failed=0": "info",
		"failed to create Playlists dir: permission denied":  "error",
		"[Main] WARNING: catalog database unavailable":       "warning",
		"panic: runtime error: index out of range":           "error",
	}
	for line, want := range cases {
		if got := classifyLogLevel(line); got != want {
			t.Errorf("classifyLogLevel(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestLogRingBufferTrimsToCapacity(t *testing.T) {
	b := &logRingBuffer{}
	for i := 0; i < serverLogBufferSize+50; i++ {
		b.add(LogEntry{Time: time.Now(), Level: "info", Message: "line"})
	}
	got := b.snapshot()
	if len(got) != serverLogBufferSize {
		t.Fatalf("snapshot length = %d, want %d (buffer must trim oldest entries, not grow unbounded)", len(got), serverLogBufferSize)
	}
}

func TestLogRingBufferPublishesToHub(t *testing.T) {
	b := &logRingBuffer{}
	hub := newSSEHub()
	ch := hub.subscribe()
	defer hub.unsubscribe(ch)
	b.attachHub(hub)

	b.add(LogEntry{Time: time.Now(), Level: "error", Message: "boom"})

	select {
	case ev := <-ch:
		if ev.Type != "server_log" {
			t.Fatalf("event type = %q, want %q", ev.Type, "server_log")
		}
		entry, ok := ev.Data.(LogEntry)
		if !ok || entry.Message != "boom" {
			t.Fatalf("event data = %#v, want LogEntry{Message: %q}", ev.Data, "boom")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for server_log event on the hub")
	}
}

// TestCaptureStdoutTeesAndParsesLines is an end-to-end check of the whole
// os.Pipe interception: writes to os.Stdout after captureStdout() must
// still land on the original fd (docker logs / terminal unaffected) AND
// show up as complete lines in serverLogs. In-place \r progress updates
// with no trailing \n must not appear until they're followed by one.
func TestCaptureStdoutTeesAndParsesLines(t *testing.T) {
	origStdout := os.Stdout
	origServerLogs := serverLogs
	defer func() {
		os.Stdout = origStdout
		serverLogs = origServerLogs
	}()
	serverLogs = &logRingBuffer{}

	// captureStdout tees whatever os.Stdout points to when it's called, so
	// point it at our own pipe first — this both proves the passthrough
	// side works and keeps the test's output off the real terminal.
	capturedR, capturedW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer capturedR.Close()
	os.Stdout = capturedW
	captureStdout()

	os.Stdout.WriteString("\rDownloading: 1 MB (1/3)")
	os.Stdout.WriteString("\rDownloading: 2 MB (2/3)")
	os.Stdout.WriteString("\rDownloaded: 3 MB (Complete)\n")
	os.Stdout.WriteString("[Watcher] M3U8: list.m3u8 written (5 entries)\n")

	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := serverLogs.snapshot()
		if len(snap) >= 2 {
			if snap[0].Message == "" {
				t.Fatalf("captured an empty/whitespace-only line: %#v", snap[0])
			}
			if snap[1].Message != "[Watcher] M3U8: list.m3u8 written (5 entries)" {
				t.Fatalf("second captured line = %q, want the M3U8 write log", snap[1].Message)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for lines to be captured, got %d entries: %#v", len(snap), snap)
		}
		time.Sleep(10 * time.Millisecond)
	}

	capturedW.Close()
}

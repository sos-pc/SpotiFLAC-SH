package util

import (
	"log/slog"
	"runtime/debug"
)

// SafeGo runs fn in a new goroutine, recovering any panic so a bug in one
// background task can't take down the whole process.
//
// Go has no per-goroutine isolation: an unrecovered panic in ANY goroutine
// terminates the entire program, not just that goroutine. net/http's own
// per-connection recover only protects the exact goroutine it invoked a
// handler on — a goroutine a handler spawns with a bare "go" statement (a
// background worker, a fire-and-forget task, a fan-out over a WaitGroup) is
// just as unprotected as a long-running daemon loop is. SafeGo is the
// single place that closes this gap; every goroutine launch in this
// codebase should go through it rather than a bare "go" statement.
//
// name identifies the task in the recovered-panic log line — pick
// something that would let an operator find the responsible code path
// without a stack trace (SafeGo prints one anyway, but the name is what
// shows up first).
func SafeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("[PANIC] recovered in goroutine", "name", name, "recover", r, "stack", string(debug.Stack()))
			}
		}()
		fn()
	}()
}

// SafeGoOrElse is SafeGo for a goroutine whose caller is waiting on
// something (a channel send, a slot in a fixed-size results slice) that fn
// is expected to always produce exactly once. A plain SafeGo's silent
// recovery would satisfy that contract on the happy path but break it on a
// panic, leaving the caller blocked forever waiting for a result that will
// never arrive — worse than the crash SafeGo prevents. onPanic runs in
// fn's place when fn panics; it must perform whatever fallback send/write
// fn's caller is relying on.
func SafeGoOrElse(name string, fn func(), onPanic func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("[PANIC] recovered in goroutine", "name", name, "recover", r, "stack", string(debug.Stack()))
				onPanic()
			}
		}()
		fn()
	}()
}

package meta

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLockTagWriteSerializesSamePath is the regression test for the
// write-write race between a worker's metadata embed and an admin
// retag-legacy run on the same file: concurrent holders of the lock for
// the SAME path must never overlap.
func TestLockTagWriteSerializesSamePath(t *testing.T) {
	const path = "/fake/track.flac"
	const goroutines = 20

	var concurrent int32
	var maxConcurrent int32
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := lockTagWrite(path)
			defer unlock()

			n := atomic.AddInt32(&concurrent, 1)
			for {
				old := atomic.LoadInt32(&maxConcurrent)
				if n <= old {
					break
				}
				if atomic.CompareAndSwapInt32(&maxConcurrent, old, n) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt32(&concurrent, -1)
		}()
	}
	wg.Wait()

	if maxConcurrent != 1 {
		t.Errorf("max concurrent holders of the same path's lock = %d, want 1 (writes to the same file overlapped)", maxConcurrent)
	}
}

// TestLockTagWriteDoesNotSerializeDifferentPaths confirms the lock is
// per-path, not global: unrelated files being tagged at the same time
// (e.g. two different tracks downloading concurrently) should not block
// each other.
func TestLockTagWriteDoesNotSerializeDifferentPaths(t *testing.T) {
	const goroutines = 10

	var concurrent int32
	var maxConcurrent int32
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := "/fake/track-" + string(rune('a'+i)) + ".flac"
			unlock := lockTagWrite(path)
			defer unlock()

			n := atomic.AddInt32(&concurrent, 1)
			for {
				old := atomic.LoadInt32(&maxConcurrent)
				if n <= old {
					break
				}
				if atomic.CompareAndSwapInt32(&maxConcurrent, old, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt32(&concurrent, -1)
		}()
	}
	wg.Wait()

	if maxConcurrent <= 1 {
		t.Errorf("max concurrent holders across DIFFERENT paths = %d, want >1 (lock is unexpectedly serializing unrelated files)", maxConcurrent)
	}
}

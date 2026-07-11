package util

import (
	"sync"
	"testing"
)

func TestSafeGoRecoversPanicWithoutCrashing(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	SafeGo("test-panic", func() {
		defer wg.Done()
		panic("boom")
	})
	wg.Wait()
	// Reaching this line at all is the assertion: an unrecovered panic in
	// the goroutine above would have crashed the whole test binary.
}

func TestSafeGoRunsFnNormally(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	ran := false
	SafeGo("test-normal", func() {
		defer wg.Done()
		ran = true
	})
	wg.Wait()
	if !ran {
		t.Error("fn was not run")
	}
}

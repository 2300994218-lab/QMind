package handler

import (
	"testing"
	"time"
)

// A panic in a detached goroutine used to bring the whole server down, because
// Gin's Recovery middleware only guards the request goroutine. SafeGo has to keep
// the process alive and still let the caller know the work finished.
func TestSafeGoRecoversPanicFromBackgroundGoroutine(t *testing.T) {
	const runs = 32
	done := make(chan int, runs)

	for i := 0; i < runs; i++ {
		index := i
		SafeGo("test.panic", func() {
			defer func() { done <- index }()
			panic("boom")
		})
	}

	for i := 0; i < runs; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("SafeGo did not return from a panicking task")
		}
	}
}

func TestSafeGoRunsTheTaskWhenNothingPanics(t *testing.T) {
	finished := make(chan struct{})
	SafeGo("test.ok", func() { close(finished) })

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("SafeGo never ran the task")
	}
}

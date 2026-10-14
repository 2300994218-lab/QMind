package handler

import (
	"log"
	"runtime/debug"
)

// SafeGo runs fn in a background goroutine that cannot take the process down.
//
// Gin's Recovery middleware only guards the request goroutine, so a panic in a
// detached `go ...` - a scheduler tick, a persist helper, a cache warmer - kills
// the server and every connection with it. SafeGo turns that into a logged stack
// trace and lets the rest of the process keep serving.
//
// Use it for every fire-and-forget goroutine. Request handlers themselves must
// keep running on Gin's goroutine, where Recovery already catches them.
func SafeGo(name string, fn func()) {
	go func() {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("[SafeGo][%s] panic: %v\n%s", name, err, debug.Stack())
			}
		}()
		fn()
	}()
}

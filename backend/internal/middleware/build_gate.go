package middleware

import "sync"

// buildGate is a minimal singleflight over cache keys: the first request for a
// key becomes the leader and runs the downstream handler, while the rest park on
// the leader's channel and share the snapshot it produced. It exists so a slow
// upstream fetch blocks only requests for that exact key, instead of every
// request that used to wait on the same stripe mutex across c.Next().
//
// The mutex is held only while reading or writing the map - never while a
// handler runs - so its critical section stays in the microsecond range.
type buildGate struct {
	mu    sync.Mutex
	leads map[string]*pageBuild
}

// pageBuild is one in-flight snapshot build. entry/ok are written by the leader
// before done is closed, which is what publishes them to the waiters.
type pageBuild struct {
	done  chan struct{}
	entry pageCacheEntry
	ok    bool
}

func newBuildGate() *buildGate {
	return &buildGate{leads: make(map[string]*pageBuild)}
}

// acquire either returns the build already in flight for key (leader=false) or
// registers the caller as the new leader (leader=true).
func (g *buildGate) acquire(key string) (*pageBuild, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if build, running := g.leads[key]; running {
		return build, false
	}
	build := &pageBuild{done: make(chan struct{})}
	g.leads[key] = build
	return build, true
}

// release hands the result to the waiters and frees the slot. It must run even
// when the leader panics, otherwise every parked request would wait forever; the
// deferred call in runAsLeader guarantees that.
func (g *buildGate) release(key string, build *pageBuild, entry pageCacheEntry, ok bool) {
	g.mu.Lock()
	if current, running := g.leads[key]; running && current == build {
		delete(g.leads, key)
	}
	g.mu.Unlock()

	build.entry, build.ok = entry, ok
	close(build.done)
}

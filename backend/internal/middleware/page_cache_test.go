package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestPageCacheHitRefreshAndExpiry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	pc := NewPageCache(root)
	clock := time.Date(2026, 9, 5, 10, 0, 0, 0, time.Local)
	pc.now = func() time.Time { return clock }

	var calls atomic.Int32
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(7))
		c.Next()
	})
	router.Use(pc.Middleware())
	router.GET("/api/market/sample", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"value": calls.Add(1)})
	})

	first := performCacheRequest(router, http.MethodGet, "/api/market/sample?type=day", "dashboard")
	assertCacheValue(t, first, "MISS", 1)
	second := performCacheRequest(router, http.MethodGet, "/api/market/sample?type=day", "dashboard")
	assertCacheValue(t, second, "HIT", 1)

	files, err := filepath.Glob(filepath.Join(root, "dashboard", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one dashboard cache file, files=%v err=%v", files, err)
	}

	refresh := performCacheRequest(router, http.MethodGet, "/api/market/sample?type=day&refresh=true", "dashboard")
	assertCacheValue(t, refresh, "REFRESH", 2)
	afterRefresh := performCacheRequest(router, http.MethodGet, "/api/market/sample?type=day", "dashboard")
	assertCacheValue(t, afterRefresh, "HIT", 2)

	clock = clock.Add(3 * time.Minute)
	afterExpiry := performCacheRequest(router, http.MethodGet, "/api/market/sample?type=day", "dashboard")
	assertCacheValue(t, afterExpiry, "MISS", 3)
}

func TestPageCacheSeparatesUsersAndInvalidatesPageOnWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	pc := NewPageCache(root)

	var userID atomic.Uint32
	userID.Store(1)
	var calls atomic.Int32
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(userID.Load()))
		c.Next()
	})
	router.Use(pc.Middleware())
	router.GET("/api/watchlist", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"value": calls.Add(1), "user": userID.Load()})
	})
	router.POST("/api/watchlist", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	assertCacheValue(t, performCacheRequest(router, http.MethodGet, "/api/watchlist", "watchlist"), "MISS", 1)
	assertCacheValue(t, performCacheRequest(router, http.MethodGet, "/api/watchlist", "watchlist"), "HIT", 1)

	userID.Store(2)
	assertCacheValue(t, performCacheRequest(router, http.MethodGet, "/api/watchlist", "watchlist"), "MISS", 2)
	performCacheRequest(router, http.MethodPost, "/api/watchlist", "watchlist")
	assertCacheValue(t, performCacheRequest(router, http.MethodGet, "/api/watchlist", "watchlist"), "MISS", 3)

	entries, err := os.ReadDir(filepath.Join(root, "watchlist"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected invalidation to leave only the newly generated entry, got %d", len(entries))
	}
}

func TestPageCacheFreezesClosedSessionSnapshotsUntilReopen(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	clock := time.Date(2026, 10, 5, 8, 0, 0, 0, time.Local)
	pc := NewPageCache(root).WithMarketFreeze(func(time.Time) (time.Duration, bool) {
		return 30 * time.Minute, true
	})
	pc.now = func() time.Time { return clock }

	var marketCalls, newsCalls atomic.Int32
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(7))
		c.Next()
	})
	router.Use(pc.Middleware())
	router.GET("/api/market/sample", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"value": marketCalls.Add(1)})
	})
	router.GET("/api/news/sample", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"value": newsCalls.Add(1)})
	})

	assertCacheValue(t, performCacheRequest(router, http.MethodGet, "/api/market/sample", "dashboard"), "MISS", 1)
	clock = clock.Add(25 * time.Minute)
	assertCacheValue(t, performCacheRequest(router, http.MethodGet, "/api/market/sample", "dashboard"), "HIT", 1)
	clock = clock.Add(6 * time.Minute)
	assertCacheValue(t, performCacheRequest(router, http.MethodGet, "/api/market/sample", "dashboard"), "MISS", 2)

	// News keeps arriving while the market is closed, so the freeze must not cover it.
	assertCacheValue(t, performCacheRequest(router, http.MethodGet, "/api/news/sample", "investment-news"), "MISS", 1)
	clock = clock.Add(11 * time.Minute)
	assertCacheValue(t, performCacheRequest(router, http.MethodGet, "/api/news/sample", "investment-news"), "MISS", 2)
}

func TestPageCacheDropsSnapshotsWrittenByAPreviousProcess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	clock := time.Date(2026, 10, 5, 8, 0, 0, 0, time.Local)
	var calls atomic.Int32
	serve := func(pc *PageCache) *httptest.ResponseRecorder {
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set("user_id", uint(7))
			c.Next()
		})
		router.Use(pc.Middleware())
		router.GET("/api/market/sample", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"value": calls.Add(1)})
		})
		return performCacheRequest(router, http.MethodGet, "/api/market/sample", "dashboard")
	}

	before := NewPageCache(root)
	before.now = func() time.Time { return clock }
	assertCacheValue(t, serve(before), "MISS", 1)
	assertCacheValue(t, serve(before), "HIT", 1)

	// A restart on the same frozen-market clock must not reuse the old payload: it was
	// built by code that may since have been fixed.
	calls.Store(9)
	restarted := NewPageCache(root)
	restarted.now = func() time.Time { return clock }
	assertCacheValue(t, serve(restarted), "MISS", 10)
}

// A cache MISS used to hold a whole stripe (1/64 of all keys) locked across
// c.Next(), so one multi-second upstream fetch stalled unrelated pages. Now only
// requests for the identical key wait, and they share the leader's snapshot.
func TestPageCacheSharesOneBuildAcrossConcurrentRequestsForSameKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pc := NewPageCache(t.TempDir())

	const concurrency = 8
	var calls atomic.Int32
	blocked := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(7))
		c.Next()
	})
	router.Use(pc.Middleware())
	router.GET("/api/market/slow", func(c *gin.Context) {
		once.Do(func() { close(entered) })
		<-blocked
		c.JSON(http.StatusOK, gin.H{"value": calls.Add(1)})
	})

	responses := make([]*httptest.ResponseRecorder, concurrency)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			responses[index] = performCacheRequest(router, http.MethodGet, "/api/market/slow?symbol=600000", "dashboard")
		}(i)
	}

	<-entered
	// Give the parked requests time to register on the in-flight build before the
	// leader is allowed to finish.
	time.Sleep(200 * time.Millisecond)
	close(blocked)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("handler ran %d times, want 1: concurrent requests must share one build", got)
	}

	var miss, hit int
	for _, response := range responses {
		if response.Code != http.StatusOK {
			t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
		}
		// Waiters are served either from the leader's in-memory snapshot or, if they
		// arrived after it was written, from disk: same payload, and marshalling onto
		// disk may drop the trailing newline the JSON renderer added.
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("unexpected body %q: %v", response.Body.String(), err)
		}
		if body["value"] != float64(1) {
			t.Fatalf("unexpected body %q, want the shared snapshot", response.Body.String())
		}
		switch response.Header().Get("X-QuantMind-Cache") {
		case "MISS":
			miss++
		case "HIT":
			hit++
		default:
			t.Fatalf("unexpected cache header %q", response.Header().Get("X-QuantMind-Cache"))
		}
	}
	if miss != 1 || hit != concurrency-1 {
		t.Fatalf("cache headers: miss=%d hit=%d, want miss=1 hit=%d", miss, hit, concurrency-1)
	}

	files, err := filepath.Glob(filepath.Join(pc.root, "dashboard", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected a single snapshot file, files=%v err=%v", files, err)
	}
}

func TestPageCacheSlowKeyDoesNotBlockOtherKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pc := NewPageCache(t.TempDir())

	blocked := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	var otherCalls atomic.Int32

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(7))
		c.Next()
	})
	router.Use(pc.Middleware())
	router.GET("/api/market/slow", func(c *gin.Context) {
		once.Do(func() { close(entered) })
		<-blocked
		c.JSON(http.StatusOK, gin.H{"value": 1})
	})
	router.GET("/api/market/quick", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"value": otherCalls.Add(1)})
	})

	go performCacheRequest(router, http.MethodGet, "/api/market/slow", "dashboard")
	<-entered

	// Same page and the same user, but a different key: it must not wait for the
	// slow upstream to return.
	quick := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		quick <- performCacheRequest(router, http.MethodGet, "/api/market/quick", "dashboard")
	}()

	select {
	case response := <-quick:
		assertCacheValue(t, response, "MISS", 1)
	case <-time.After(2 * time.Second):
		t.Fatal("a different key was blocked by the slow build")
	}
	close(blocked)
}

// The leader must hand its waiters back even when it dies: a panic in the
// handler unwinds past the build, and the deferred release is what stops the
// parked requests from hanging forever.
func TestPageCacheReleasesWaitersWhenLeaderPanics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pc := NewPageCache(t.TempDir())

	var calls atomic.Int32
	blocked := make(chan struct{})
	entered := make(chan struct{})

	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(func(c *gin.Context) {
		c.Set("user_id", uint(7))
		c.Next()
	})
	router.Use(pc.Middleware())
	router.GET("/api/market/flaky", func(c *gin.Context) {
		if calls.Add(1) == 1 {
			close(entered)
			<-blocked
			panic("upstream exploded")
		}
		c.JSON(http.StatusOK, gin.H{"value": 2})
	})

	leader := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		leader <- performCacheRequest(router, http.MethodGet, "/api/market/flaky", "dashboard")
	}()
	<-entered

	waiter := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		waiter <- performCacheRequest(router, http.MethodGet, "/api/market/flaky", "dashboard")
	}()

	time.Sleep(200 * time.Millisecond)
	close(blocked)

	var response *httptest.ResponseRecorder
	select {
	case response = <-waiter:
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter was stranded by the panicking leader")
	}
	assertCacheValue(t, response, "MISS", 2)

	select {
	case response = <-leader:
	case <-time.After(2 * time.Second):
		t.Fatal("the leader never returned from its panic")
	}
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("leader response code = %d, want the 500 gin.Recovery wrote", response.Code)
	}

	pc.builds.mu.Lock()
	pending := len(pc.builds.leads)
	pc.builds.mu.Unlock()
	if pending != 0 {
		t.Fatalf("%d builds left registered in the gate, want none", pending)
	}
}

func performCacheRequest(router http.Handler, method, target, page string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-QuantMind-Page", page)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func assertCacheValue(t *testing.T, response *httptest.ResponseRecorder, cacheStatus string, value float64) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("X-QuantMind-Cache"); got != cacheStatus {
		t.Fatalf("cache status = %q, want %q", got, cacheStatus)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got := body["value"]; got != value {
		t.Fatalf("value = %v, want %v", got, value)
	}
}

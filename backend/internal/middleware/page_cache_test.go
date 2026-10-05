package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

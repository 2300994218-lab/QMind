package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"quantmind/internal/version"

	"github.com/gin-gonic/gin"
)

const (
	pageCacheSchema  = 1
	maxCacheFileSize = 20 << 20  // 20 MiB: do not cache unexpectedly large responses.
	maxCacheSize     = 512 << 20 // 512 MiB across all page directories.
)

// pageCacheSerial distinguishes cache instances created inside one clock tick.
var pageCacheSerial atomic.Uint64

// PageCache persists successful GET responses below temp/pages/<page>. The
// authenticated user id is part of the key, so user-specific responses are
// never shared between accounts.
type PageCache struct {
	root string
	now  func() time.Time
	// builds dedups concurrent snapshot generation, one in-flight build per key.
	builds *buildGate
	// generation identifies this process. Cached snapshots are keyed by it so a
	// restart - the usual way a data-source fix lands - cannot keep serving the
	// payload the previous build produced, which matters most for the long TTLs the
	// closed-market freeze window hands out.
	generation string
	// freezeWindow reports how long a market snapshot stays immutable because the
	// session it mirrors has already closed. Injected so this package need not
	// import the handler package.
	freezeWindow func(time.Time) (time.Duration, bool)
}

type pageCacheEntry struct {
	Schema      int             `json:"schema"`
	AppVersion  string          `json:"app_version"`
	Generation  string          `json:"generation"`
	Page        string          `json:"page"`
	Request     string          `json:"request"`
	UserID      uint            `json:"user_id"`
	CreatedAt   time.Time       `json:"created_at"`
	ExpiresAt   time.Time       `json:"expires_at"`
	Status      int             `json:"status"`
	ContentType string          `json:"content_type"`
	Body        json.RawMessage `json:"body"`
}

type cachedResponseWriter struct {
	gin.ResponseWriter
	body bytes.Buffer
}

func (w *cachedResponseWriter) Write(data []byte) (int, error) {
	_, _ = w.body.Write(data)
	return w.ResponseWriter.Write(data)
}

func (w *cachedResponseWriter) WriteString(data string) (int, error) {
	_, _ = w.body.WriteString(data)
	return w.ResponseWriter.WriteString(data)
}

// NewPageCache creates the on-disk cache and performs a bounded cleanup pass.
func NewPageCache(root string) *PageCache {
	pc := &PageCache{
		root:       filepath.Clean(root),
		now:        time.Now,
		builds:     newBuildGate(),
		generation: fmt.Sprintf("%d.%d", time.Now().UnixNano(), pageCacheSerial.Add(1)),
	}
	if err := os.MkdirAll(pc.root, 0o755); err != nil {
		log.Printf("[PageCache] disabled: cannot create %s: %v", pc.root, err)
		pc.root = ""
		return pc
	}
	pc.cleanup()
	log.Printf("[PageCache] enabled at %s (max 512 MiB)", pc.root)
	return pc
}

// WithMarketFreeze lets main.go bind the cache TTL to the trading calendar: a
// snapshot of an already-closed session is served until the market reopens
// instead of being rebuilt every two minutes.
func (pc *PageCache) WithMarketFreeze(freezeWindow func(time.Time) (time.Duration, bool)) *PageCache {
	pc.freezeWindow = freezeWindow
	return pc
}

// Middleware serves a fresh page snapshot when available. Concurrent requests
// for the same key share one generation: only the leader runs the downstream
// handler, the others wait for its snapshot instead of queueing behind a lock
// that also covered every other key in the stripe. Mutating requests invalidate
// the active page directory after a successful response.
func (pc *PageCache) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if pc.root == "" {
			c.Next()
			return
		}

		page := sanitizePage(c.GetHeader("X-QuantMind-Page"))
		if c.Request.Method != http.MethodGet {
			c.Next()
			if c.Writer.Status() >= 200 && c.Writer.Status() < 300 {
				pc.invalidatePage(page)
			}
			return
		}

		if !acceptsJSON(c) {
			c.Next()
			return
		}

		// Snapshot reads and writes need no lock: `write` publishes through a temp
		// file plus rename, so a reader sees either the previous or the new payload,
		// never a half-written one.
		cachePath, requestLabel := pc.cachePath(c, page)

		// An explicit refresh asks for a rebuild, so it neither reads the snapshot nor
		// joins one; it writes the new snapshot when the handler succeeds.
		if wantsRefresh(c) {
			c.Header("X-QuantMind-Cache", "REFRESH")
			if entry, ok := pc.build(c, page, requestLabel); ok {
				pc.store(cachePath, requestLabel, entry)
			}
			return
		}

		if entry, ok := pc.read(cachePath); ok {
			serveSnapshot(c, entry, pc.now())
			return
		}

		for {
			build, leader := pc.builds.acquire(cachePath)
			if leader {
				pc.runAsLeader(c, page, requestLabel, cachePath, build)
				return
			}
			select {
			case <-build.done:
				if build.ok {
					serveSnapshot(c, build.entry, pc.now())
					return
				}
				// The leader produced nothing cacheable (or panicked before writing a
				// snapshot), and it has released the slot: try again so this client is
				// still served rather than handed an empty body.
			case <-c.Request.Context().Done():
				// The client is gone; the leader keeps building for whoever is left.
				return
			}
		}
	}
}

// runAsLeader generates the snapshot for its key and hands it to the waiters. The
// handler runs with no mutex held, so requests for other keys are unaffected by
// how long the upstream takes.
func (pc *PageCache) runAsLeader(c *gin.Context, page, requestLabel, cachePath string, build *pageBuild) {
	var entry pageCacheEntry
	var ok bool
	// Deferred so the waiters are always released, even if the handler panics on
	// the way out; gin's Recovery middleware catches it further up the chain.
	defer func() { pc.builds.release(cachePath, build, entry, ok) }()

	c.Header("X-QuantMind-Cache", "MISS")
	entry, ok = pc.build(c, page, requestLabel)
	if ok {
		pc.store(cachePath, requestLabel, entry)
	}
}

// build runs the downstream handler while capturing its body, and returns the
// snapshot the response describes. ok=false means the response is not cacheable,
// in which case the handler has already answered the client itself.
func (pc *PageCache) build(c *gin.Context, page, requestLabel string) (pageCacheEntry, bool) {
	writer := &cachedResponseWriter{ResponseWriter: c.Writer}
	c.Writer = writer
	c.Next()

	body := writer.body.Bytes()
	if c.Writer.Status() != http.StatusOK || len(body) == 0 || len(body) > maxCacheFileSize || !json.Valid(body) {
		return pageCacheEntry{}, false
	}

	createdAt := pc.now()
	entry := pageCacheEntry{
		Schema:      pageCacheSchema,
		AppVersion:  version.Version,
		Generation:  pc.generation,
		Page:        page,
		Request:     requestLabel,
		UserID:      userIDFromContext(c),
		CreatedAt:   createdAt,
		ExpiresAt:   createdAt.Add(pc.ttlFor(c.Request.URL.Path, canonicalQuery(c.Request.URL.Query()), createdAt)),
		Status:      c.Writer.Status(),
		ContentType: c.Writer.Header().Get("Content-Type"),
		Body:        append(json.RawMessage(nil), body...),
	}
	if entry.ContentType == "" {
		entry.ContentType = "application/json; charset=utf-8"
	}
	return entry, true
}

func (pc *PageCache) store(cachePath, requestLabel string, entry pageCacheEntry) {
	if err := pc.write(cachePath, entry); err != nil {
		log.Printf("[PageCache] write failed for %s: %v", requestLabel, err)
	}
}

// serveSnapshot answers from an already captured response, without the handler.
func serveSnapshot(c *gin.Context, entry pageCacheEntry, now time.Time) {
	c.Header("X-QuantMind-Cache", "HIT")
	c.Header("X-QuantMind-Cache-Age", strconv.FormatInt(int64(now.Sub(entry.CreatedAt).Seconds()), 10))
	c.Data(entry.Status, entry.ContentType, entry.Body)
	c.Abort()
}

func (pc *PageCache) cachePath(c *gin.Context, page string) (string, string) {
	query := canonicalQuery(c.Request.URL.Query()).Encode()
	requestLabel := c.Request.URL.Path
	if query != "" {
		requestLabel += "?" + query
	}
	userID := userIDFromContext(c)
	identity := fmt.Sprintf("v%s\n%s\n%s\n%d", version.Version, page, requestLabel, userID)
	hash := sha256.Sum256([]byte(identity))
	name := readableRouteName(c.Request.URL.Path) + "__" + hex.EncodeToString(hash[:12]) + ".json"
	return filepath.Join(pc.root, page, name), requestLabel
}

func (pc *PageCache) read(path string) (pageCacheEntry, bool) {
	var entry pageCacheEntry
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &entry) != nil {
		return entry, false
	}
	if entry.Schema != pageCacheSchema || entry.AppVersion != version.Version || entry.Generation != pc.generation ||
		!pc.now().Before(entry.ExpiresAt) || !json.Valid(entry.Body) {
		_ = os.Remove(path)
		return pageCacheEntry{}, false
	}
	return entry, true
}

func (pc *PageCache) write(path string, entry pageCacheEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".qmind-cache-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func (pc *PageCache) invalidatePage(page string) {
	dir := filepath.Join(pc.root, page)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() && (strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".tmp")) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

func (pc *PageCache) cleanup() {
	type cacheFile struct {
		path    string
		size    int64
		modTime time.Time
	}
	var files []cacheFile
	var total int64
	now := pc.now()
	_ = filepath.WalkDir(pc.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".tmp") {
			_ = os.Remove(path)
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		data, readErr := os.ReadFile(path)
		var entry pageCacheEntry
		if readErr != nil || json.Unmarshal(data, &entry) != nil || entry.Schema != pageCacheSchema ||
			entry.AppVersion != version.Version || entry.Generation != pc.generation || !now.Before(entry.ExpiresAt) {
			_ = os.Remove(path)
			return nil
		}
		total += info.Size()
		files = append(files, cacheFile{path: path, size: info.Size(), modTime: info.ModTime()})
		return nil
	})
	if total <= maxCacheSize {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })
	for _, file := range files {
		if total <= maxCacheSize {
			break
		}
		if os.Remove(file.path) == nil {
			total -= file.size
		}
	}
}

func acceptsJSON(c *gin.Context) bool {
	accept := c.GetHeader("Accept")
	return accept == "" || strings.Contains(accept, "json") || strings.Contains(accept, "*/*")
}

func wantsRefresh(c *gin.Context) bool {
	refresh := strings.ToLower(c.Query("refresh"))
	cacheControl := strings.ToLower(c.GetHeader("Cache-Control"))
	return refresh == "true" || refresh == "1" || strings.Contains(cacheControl, "no-cache")
}

func canonicalQuery(values url.Values) url.Values {
	canonical := make(url.Values, len(values))
	for key, items := range values {
		if strings.EqualFold(key, "refresh") || key == "_" {
			continue
		}
		canonical[key] = append([]string(nil), items...)
	}
	return canonical
}

func userIDFromContext(c *gin.Context) uint {
	value, _ := c.Get("user_id")
	userID, _ := value.(uint)
	return userID
}

func sanitizePage(page string) string {
	page = strings.TrimSpace(strings.ToLower(page))
	if page == "" || len(page) > 64 {
		return "shared"
	}
	for _, r := range page {
		if !(unicode.IsLower(r) || unicode.IsDigit(r) || r == '-' || r == '_') {
			return "shared"
		}
	}
	return page
}

func readableRouteName(path string) string {
	name := strings.Trim(strings.TrimPrefix(path, "/api/"), "/")
	name = strings.NewReplacer("/", "_", ":", "_", "\\", "_").Replace(name)
	if name == "" {
		return "response"
	}
	if len(name) > 80 {
		return name[:80]
	}
	return name
}

func (pc *PageCache) ttlFor(path string, query url.Values, now time.Time) time.Duration {
	if isHistoricalQuery(query, now) {
		return 24 * time.Hour
	}

	path = strings.ToLower(path)
	switch {
	case strings.Contains(path, "realtime"), strings.Contains(path, "/quote"), strings.Contains(path, "/minute"), strings.Contains(path, "/trend"):
		return 15 * time.Second
	case strings.Contains(path, "/market/"), strings.Contains(path, "/dashboard"):
		// 休市期间 these snapshots would repeat a ~9s rebuild every two minutes for
		// data that cannot move; hold them until the exchange reopens instead.
		if pc.freezeWindow != nil {
			if window, frozen := pc.freezeWindow(now); frozen {
				return window
			}
		}
		return 2 * time.Minute
	case strings.Contains(path, "/news/"), strings.Contains(path, "/research"), strings.Contains(path, "/announce"):
		return 10 * time.Minute
	case strings.Contains(path, "/settings"), strings.Contains(path, "/strategies"), strings.Contains(path, "/skills"):
		return 5 * time.Minute
	default:
		return 2 * time.Minute
	}
}

func isHistoricalQuery(query url.Values, now time.Time) bool {
	// Only dates strictly before yesterday are frozen: a weekend/holiday request
	// (e.g. 20261004 on 20261005) is answered from live sources by the handler, so
	// pinning it for a day would freeze data that still changes.
	yesterday := now.AddDate(0, 0, -1).Format("20060102")
	for _, key := range []string{"trade_date", "date", "end_date"} {
		value := strings.ReplaceAll(strings.TrimSpace(query.Get(key)), "-", "")
		if value != "" && value < yesterday {
			return true
		}
	}
	return false
}

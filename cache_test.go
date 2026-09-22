package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- discoveryCache unit behaviour ----

func TestCacheGetStoreExpiry(t *testing.T) {
	c := newDiscoveryCache(50 * time.Millisecond)
	if _, ok := c.get("k"); ok {
		t.Fatal("empty cache must miss")
	}
	c.store("k", 200, "application/json", []byte("body"))
	e, ok := c.get("k")
	if !ok || e.status != 200 || e.contentType != "application/json" || string(e.body) != "body" {
		t.Fatalf("get after store = %+v, %v", e, ok)
	}
	time.Sleep(60 * time.Millisecond)
	if _, ok := c.get("k"); ok {
		t.Fatal("entry must expire after TTL")
	}
}

func TestCacheDisabledIgnoresEverything(t *testing.T) {
	c := newDiscoveryCache(0)
	c.store("k", 200, "application/json", []byte("body"))
	if c.enabled() {
		t.Fatal("ttl=0 must be disabled")
	}
	if _, ok := c.get("k"); ok {
		t.Fatal("disabled cache must never hit")
	}
}

func TestCacheOversizeNotStored(t *testing.T) {
	c := newDiscoveryCache(time.Minute)
	c.store("big", 200, "application/json", make([]byte, cacheMaxEntryBytes+1))
	if _, ok := c.get("big"); ok {
		t.Fatal("oversize body must not be stored")
	}
	c.store("edge", 200, "application/json", make([]byte, cacheMaxEntryBytes))
	if _, ok := c.get("edge"); !ok {
		t.Fatal("exactly-1MiB body must be stored")
	}
}

func TestCacheLRUBounds(t *testing.T) {
	c := newDiscoveryCache(time.Hour)
	for i := 0; i < cacheMaxEntries+10; i++ {
		c.store(fmt.Sprintf("k%03d", i), 200, "application/json", []byte("x"))
	}
	if c.order.Len() != cacheMaxEntries || len(c.entries) != cacheMaxEntries {
		t.Fatalf("entries = %d/%d, want %d", c.order.Len(), len(c.entries), cacheMaxEntries)
	}
	if _, ok := c.get("k000"); ok {
		t.Fatal("oldest entry must have been evicted")
	}
	if _, ok := c.get(fmt.Sprintf("k%03d", cacheMaxEntries+9)); !ok {
		t.Fatal("newest entry must survive")
	}
}

// ---- config parsing ----

func TestLoadConfigCacheEnv(t *testing.T) {
	t.Setenv("DISCOVERY_CACHE_TTL_SECONDS", "5")
	t.Setenv("DISCOVERY_CACHE_PATHS", "/a, /b ,, /c/")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CacheTTL != 5*time.Second {
		t.Fatalf("CacheTTL = %v", cfg.CacheTTL)
	}
	if !cfg.CachePaths["/a"] || !cfg.CachePaths["/b"] || !cfg.CachePaths["/c/"] || len(cfg.CachePaths) != 3 {
		t.Fatalf("CachePaths = %v", cfg.CachePaths)
	}

	t.Setenv("DISCOVERY_CACHE_TTL_SECONDS", "")
	t.Setenv("DISCOVERY_CACHE_PATHS", "")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CacheTTL != 300*time.Second {
		t.Fatalf("default CacheTTL = %v, want 300s", cfg.CacheTTL)
	}
	for _, p := range []string{"/v1/models", "/version", "/api/tags", "/v1/props"} {
		if !cfg.CachePaths[p] {
			t.Fatalf("default CachePaths missing %s: %v", p, cfg.CachePaths)
		}
	}
}

func TestMetricsRenderCacheCounters(t *testing.T) {
	m := NewMetrics()
	s := NewScheduler(2, 60, 10)
	m.ObserveCacheHit()
	m.ObserveCacheHit()
	m.ObserveCacheMiss()
	var buf bytes.Buffer
	m.Render(s, &buf)
	out := buf.String()
	for _, want := range []string{
		"# TYPE gothrottle_discovery_cache_hits_total counter",
		"gothrottle_discovery_cache_hits_total 2",
		"gothrottle_discovery_cache_misses_total 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

// ---- end-to-end through buildHandler ----

func testCfg(t *testing.T, upstream *url.URL, ttl time.Duration) *Config {
	t.Helper()
	return &Config{
		ListenAddr: ":0", UpstreamURL: upstream,
		MaxConcurrency: 2, RPM: 1000, MaxWait: 5 * time.Second, QueueLimit: 100,
		RequireAuth: false, StatsPath: "/_throttle/stats", MetricsPath: "/metrics",
		CacheTTL: ttl, CachePaths: parseCachePaths(defaultCachePaths),
	}
}

func metricsCounter(t *testing.T, h http.Handler, name string) int64 {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, name+" ") {
			var v int64
			fmt.Sscanf(line, name+" %d", &v)
			return v
		}
	}
	t.Fatalf("counter %s not found", name)
	return 0
}

func TestHandlerCacheHitMiss(t *testing.T) {
	var hits int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprintf(w, `{"models":["m-%d"]}`, atomic.LoadInt64(&hits))
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	h := buildHandler(testCfg(t, u, time.Minute), NewScheduler(2, 1000, 100), NewMetrics(), newDiscoveryCache(time.Minute))

	do := func(auth string) (int, string) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/v1/models", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		h.ServeHTTP(rec, r)
		return rec.Code, rec.Body.String()
	}

	code1, body1 := do("Bearer A")
	code2, body2 := do("Bearer A")
	if code1 != 200 || code2 != 200 || body1 != body2 {
		t.Fatalf("hit body != miss body: %q vs %q", body1, body2)
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("upstream hits = %d, want 1 (second request must be served from cache)", got)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_hits_total"); v != 1 {
		t.Fatalf("cache_hits = %d, want 1", v)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_misses_total"); v != 1 {
		t.Fatalf("cache_misses = %d, want 1", v)
	}
	if v := metricsCounter(t, h, "gothrottle_admitted_total"); v != 1 {
		t.Fatalf("admitted = %d, want 1 (hit must not enter the queue)", v)
	}

	// different credential -> its own entry, no leak of A's body
	_, bodyB := do("Bearer B")
	if bodyB == body1 {
		t.Fatal("key B must not receive key A's cached body")
	}
	if got := atomic.LoadInt64(&hits); got != 2 {
		t.Fatalf("upstream hits after distinct key = %d, want 2", got)
	}

	// POST and prefix paths are not cacheable
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/models", nil))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/extra", nil))
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_hits_total"); v != 1 {
		t.Fatalf("hits moved on non-GET/prefix path: %d", v)
	}
}

func TestHandlerCacheTTLExpiry(t *testing.T) {
	var hits int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Write([]byte("ok"))
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	h := buildHandler(testCfg(t, u, 30*time.Millisecond), NewScheduler(2, 1000, 100), NewMetrics(), newDiscoveryCache(30*time.Millisecond))
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/version", nil))
		time.Sleep(50 * time.Millisecond)
	}
	if got := atomic.LoadInt64(&hits); got != 3 {
		t.Fatalf("upstream hits = %d, want 3 (entry must refresh after TTL)", got)
	}
}

func TestHandlerNon2xxNotCached(t *testing.T) {
	var hits int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	h := buildHandler(testCfg(t, u, time.Minute), NewScheduler(2, 1000, 100), NewMetrics(), newDiscoveryCache(time.Minute))
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/tags", nil))
		if rec.Code != 500 {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
	}
	if got := atomic.LoadInt64(&hits); got != 2 {
		t.Fatalf("500 must not be cached: upstream hits = %d, want 2", got)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_hits_total"); v != 0 {
		t.Fatalf("hits = %d, want 0 for non-2xx", v)
	}
}

func TestHandlerDisabledCacheZeroCounters(t *testing.T) {
	var hits int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Write([]byte("ok"))
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	h := buildHandler(testCfg(t, u, 0), NewScheduler(2, 1000, 100), NewMetrics(), newDiscoveryCache(0))
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/props", nil))
	}
	if got := atomic.LoadInt64(&hits); got != 3 {
		t.Fatalf("upstream hits = %d, want 3", got)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_misses_total"); v != 0 {
		t.Fatalf("disabled cache must not move miss counter, got %d", v)
	}
}

// concurrent hits + misses + evictions must be race-clean and consistent.
func TestCacheConcurrent(t *testing.T) {
	var hits int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[]}`)
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	h := buildHandler(testCfg(t, u, 20*time.Millisecond), NewScheduler(4, 100000, 1000), NewMetrics(), newDiscoveryCache(20*time.Millisecond))
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				rec := httptest.NewRecorder()
				r := httptest.NewRequest("GET", "/v1/models", nil)
				r.Header.Set("Authorization", fmt.Sprintf("Bearer k%d", i%150))
				h.ServeHTTP(rec, r)
				if rec.Code != 200 {
					t.Errorf("status = %d", rec.Code)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	c := newDiscoveryCache(time.Minute)
	c.store("x", 200, "application/json", []byte("y"))
	_, _ = c.get("x")
}

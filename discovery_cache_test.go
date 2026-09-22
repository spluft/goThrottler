package main

// QA suite for the discovery-response cache (epic t_54812c74, card t_94c52676).
// White-box through the buildHandler seam (SPEC section 8). The backend's
// cache_test.go covers basic hit/miss/TTL/non-2xx/LRU unit paths; this file
// targets the acceptance-criteria behaviours end-to-end: queue bypass,
// auth-gate ordering, per-credential isolation incl. anon, method/path
// fall-through, byte cap and entry cap through the real handler, and metrics
// accounting.

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// countingUpstream returns an httptest server that answers with a unique
// body per request ("resp-<n>") and exposes its hit counter.
func countingUpstream(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"req":%d}`, i)
	}))
	t.Cleanup(up.Close)
	return up, &n
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// statsCounter reads one numeric field from the /_throttle/stats JSON.
func statsCounter(t *testing.T, h http.Handler, field string) int64 {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/_throttle/stats", nil))
	body := rec.Body.String()
	i := strings.Index(body, `"`+field+`":`)
	if i < 0 {
		t.Fatalf("stats missing %s: %s", field, body)
	}
	rest := body[i+len(field)+3:]
	end := strings.IndexAny(rest, ",}\n ")
	if end < 0 {
		end = len(rest)
	}
	v, err := strconv.ParseInt(strings.TrimSpace(rest[:end]), 10, 64)
	if err != nil {
		t.Fatalf("stats %s parse %q: %v", field, rest[:end], err)
	}
	return v
}

func doGet(t *testing.T, h http.Handler, path, auth string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", path, nil)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	h.ServeHTTP(rec, r)
	return rec
}

// AC: cache HIT is served without entering the queue at all. Proven with a
// scheduler that rejects EVERY admission (QueueLimit=0): a queueing request
// would get 429 queue_full; the warm cache entry must still return 200 with
// zero scheduler state movement and zero upstream traffic.
func TestQACacheHitBypassesQueueEntirely(t *testing.T) {
	up, upHits := countingUpstream(t)
	cfg := testCfg(t, mustParse(t, up.URL), time.Minute)
	cfg.QueueLimit = 0 // every queue admission fails with queue_full
	sched := NewScheduler(2, 1000, 0)
	metrics := NewMetrics()
	cache := newDiscoveryCache(time.Minute)
	h := buildHandler(cfg, sched, metrics, cache)

	// Control: same scheduler, non-cacheable path -> 429 (queue really rejects).
	if rec := doGet(t, h, "/not-allowed", "Bearer A"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("control request: status = %d, want 429 (scheduler must reject when queue is full)", rec.Code)
	}

	// Warm the entry directly (white-box), then hit it with a full queue.
	warmReq := httptest.NewRequest("GET", "/v1/models", nil)
	warmReq.Header.Set("Authorization", "Bearer A")
	cache.store("/v1/models|"+keyFingerprint(warmReq),
		http.StatusOK, "application/json", []byte(`{"cached":true}`))

	rec := doGet(t, h, "/v1/models", "Bearer A")
	if rec.Code != http.StatusOK {
		t.Fatalf("cache hit with full queue: status = %d, want 200 (hit must not touch the scheduler)", rec.Code)
	}
	if got := rec.Body.String(); got != `{"cached":true}` {
		t.Fatalf("cache hit body = %q", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("cache hit Content-Type = %q, want application/json", ct)
	}
	if n := upHits.Load(); n != 0 {
		t.Fatalf("upstream hits = %d, want 0 (hit must not proxy)", n)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_hits_total"); v != 1 {
		t.Fatalf("cache_hits = %d, want 1", v)
	}
	if v := metricsCounter(t, h, "gothrottle_admitted_total"); v != 0 {
		t.Fatalf("admitted_total = %d, want 0 (hit and queue_full rejection must not admit)", v)
	}
	if s := statsCounter(t, h, "admitted_total"); s != 0 {
		t.Fatalf("scheduler admitted_total = %d, want 0 (hit bypasses WaitCtx)", s)
	}
	if s := statsCounter(t, h, "queued"); s != 0 {
		t.Fatalf("scheduler queued = %d, want 0", s)
	}
}

// AC F5: the cache is consulted only AFTER the REQUIRE_AUTH anon gate — a
// warm entry must never answer an anonymous request that the gate rejects.
func TestQAAuthGateRunsBeforeCache(t *testing.T) {
	up, upHits := countingUpstream(t)
	cfg := testCfg(t, mustParse(t, up.URL), time.Minute)
	cfg.RequireAuth = true
	h := buildHandler(cfg, NewScheduler(2, 1000, 100), NewMetrics(), newDiscoveryCache(time.Minute))

	// Warm the authenticated entry.
	if rec := doGet(t, h, "/v1/models", "Bearer A"); rec.Code != 200 {
		t.Fatalf("warm-up: status = %d, want 200", rec.Code)
	}
	authedBody := doGet(t, h, "/v1/models", "Bearer A").Body.String() // hit, same body
	if n := upHits.Load(); n != 1 {
		t.Fatalf("upstream hits after warm+hit = %d, want 1", n)
	}

	// Anon GET: 401 from the auth gate, never served from cache.
	rec := doGet(t, h, "/v1/models", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon with RequireAuth: status = %d, want 401", rec.Code)
	}
	if rec.Body.String() == authedBody {
		t.Fatal("anon received the cached authenticated body")
	}
	if n := upHits.Load(); n != 1 {
		t.Fatalf("anon rejection must not proxy: upstream hits = %d, want 1", n)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_hits_total"); v != 1 {
		t.Fatalf("cache_hits = %d, want 1 (anon gate rejection is not a cache hit)", v)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_misses_total"); v != 1 {
		t.Fatalf("cache_misses = %d, want 1 (anon gate rejection is not a cache miss)", v)
	}
}

// Edge case from SPEC 5: with REQUIRE_AUTH=0 anonymous requests share one
// "|anon" entry, and a credentialed request never reads it.
func TestQAAnonKeyIsolation(t *testing.T) {
	up, upHits := countingUpstream(t)
	cfg := testCfg(t, mustParse(t, up.URL), time.Minute) // RequireAuth = false
	h := buildHandler(cfg, NewScheduler(2, 1000, 100), NewMetrics(), newDiscoveryCache(time.Minute))

	a1 := doGet(t, h, "/version", "") // miss -> upstream 1
	a2 := doGet(t, h, "/version", "") // hit  -> same body, no upstream
	if a1.Body.String() != a2.Body.String() {
		t.Fatal("two anon requests must share the |anon cache entry")
	}
	if n := upHits.Load(); n != 1 {
		t.Fatalf("upstream hits = %d, want 1 (anon requests share one key)", n)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_hits_total"); v != 1 {
		t.Fatalf("cache_hits = %d, want 1", v)
	}
	// Credentialed GET: distinct key -> its own miss, its own body.
	c := doGet(t, h, "/version", "Bearer keyed")
	if c.Body.String() == a1.Body.String() {
		t.Fatal("keyed request must not read the anon entry")
	}
	if n := upHits.Load(); n != 2 {
		t.Fatalf("upstream hits after keyed request = %d, want 2", n)
	}
}

// AC 4: exact match only. POST/PUT/HEAD to a discovery path, prefix paths
// and unknown paths must all take the plain queue path against a warm entry
// and never move the hit or miss counters.
func TestQAMethodAndPathFallThrough(t *testing.T) {
	up, upHits := countingUpstream(t)
	cfg := testCfg(t, mustParse(t, up.URL), time.Minute)
	h := buildHandler(cfg, NewScheduler(2, 1000, 100), NewMetrics(), newDiscoveryCache(time.Minute))

	// Warm /v1/models for the anon key.
	if rec := doGet(t, h, "/v1/models", ""); rec.Code != 200 {
		t.Fatalf("warm-up status = %d", rec.Code)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_misses_total"); v != 1 {
		t.Fatalf("misses after warm-up = %d, want 1", v)
	}
	before := upHits.Load() // 1

	fall := []struct {
		method, path string
	}{
		{"POST", "/v1/models"},
		{"PUT", "/v1/models"},
		{"HEAD", "/v1/models"},
		{"GET", "/v1/models/extra"}, // prefix, not exact
		{"GET", "/not-allowed"},     // not allow-listed
		{"GET", "/Version"},         // exact match is case-sensitive
	}
	for i, f := range fall {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(f.method, f.path, nil))
		_ = rec
		if got := upHits.Load(); got != before+int64(i+1) {
			t.Fatalf("%s %s: upstream hits = %d, want %d (must take the queue path)", f.method, f.path, got, before+int64(i+1))
		}
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_hits_total"); v != 0 {
		t.Fatalf("cache_hits = %d, want 0 (fall-through must not hit the cache)", v)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_misses_total"); v != 1 {
		t.Fatalf("cache_misses = %d, want 1 (fall-through traffic must not count as discovery misses)", v)
	}

	// F2: query string is NOT part of the key — same path + same credential
	// with a query still hits the warm entry.
	afterFall := upHits.Load()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models?foo=bar", nil))
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_hits_total"); v != 1 {
		t.Fatalf("cache_hits after query GET = %d, want 1 (query string is not part of the key)", v)
	}
	if got := upHits.Load(); got != afterFall {
		t.Fatalf("query GET proxied: upstream hits = %d, want %d", got, afterFall)
	}
}

// AC 6 variant: 5xx is not cached, and once upstream recovers the 2xx is.
func TestQANon2xxThenRecovery(t *testing.T) {
	var upHits atomic.Int64
	var failing atomic.Bool
	failing.Store(true)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upHits.Add(1)
		if failing.Load() {
			http.Error(w, `{"error":"down"}`, http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer up.Close()
	cfg := testCfg(t, mustParse(t, up.URL), time.Minute)
	h := buildHandler(cfg, NewScheduler(2, 1000, 100), NewMetrics(), newDiscoveryCache(time.Minute))

	for i := 0; i < 3; i++ {
		rec := doGet(t, h, "/api/tags", "")
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("request %d: status = %d, want 502", i+1, rec.Code)
		}
	}
	if n := upHits.Load(); n != 3 {
		t.Fatalf("502 must not be cached: upstream hits = %d, want 3", n)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_hits_total"); v != 0 {
		t.Fatalf("cache_hits = %d, want 0 while upstream errors", v)
	}

	failing.Store(false)
	rec := doGet(t, h, "/api/tags", "")
	if rec.Code != 200 || rec.Body.String() != `{"ok":true}` {
		t.Fatalf("recovery: status=%d body=%q", rec.Code, rec.Body.String())
	}
	rec2 := doGet(t, h, "/api/tags", "") // must be the cached copy
	if rec2.Body.String() != rec.Body.String() {
		t.Fatal("recovered 2xx must be cached")
	}
	if n := upHits.Load(); n != 4 {
		t.Fatalf("upstream hits = %d, want 4 (only the recovered 2xx was cached)", n)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_hits_total"); v != 1 {
		t.Fatalf("cache_hits = %d, want 1", v)
	}
}

// AC 7b / N2: an oversized (>1 MiB) 2xx is forwarded in full but not stored:
// two such requests = two upstream hits and two cache misses.
func TestQAByteCapThroughHandler(t *testing.T) {
	big := bytes.Repeat([]byte("x"), cacheMaxEntryBytes+4096)
	var upHits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upHits.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(big)
	}))
	defer up.Close()
	cfg := testCfg(t, mustParse(t, up.URL), time.Minute)
	h := buildHandler(cfg, NewScheduler(2, 1000, 100), NewMetrics(), newDiscoveryCache(time.Minute))

	for i := 0; i < 2; i++ {
		rec := doGet(t, h, "/v1/models", "Bearer big")
		if rec.Code != 200 {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Body.Len() != len(big) {
			t.Fatalf("client got %d bytes, want full %d (forwarding must not be truncated)", rec.Body.Len(), len(big))
		}
	}
	if n := upHits.Load(); n != 2 {
		t.Fatalf("oversized body must not be cached: upstream hits = %d, want 2", n)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_misses_total"); v != 2 {
		t.Fatalf("cache_misses = %d, want 2", v)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_hits_total"); v != 0 {
		t.Fatalf("cache_hits = %d, want 0", v)
	}
}

// AC 7a / N2 through the real handler: >128 distinct credentials evict the
// oldest entries (drop-oldest), recent ones keep hitting.
func TestQAEntryCapThroughHandler(t *testing.T) {
	up, upHits := countingUpstream(t)
	cfg := testCfg(t, mustParse(t, up.URL), time.Hour)
	metrics := NewMetrics()
	h := buildHandler(cfg, NewScheduler(2, 100000, 1000), metrics, newDiscoveryCache(time.Hour))

	const n = cacheMaxEntries + 2 // 130 distinct keys -> 2 evictions
	for i := 0; i < n; i++ {
		rec := doGet(t, h, "/v1/models", fmt.Sprintf("Bearer k%03d", i))
		if rec.Code != 200 {
			t.Fatalf("warm-up %d: status = %d", i, rec.Code)
		}
	}
	if got := upHits.Load(); got != n {
		t.Fatalf("warm-up upstream hits = %d, want %d", got, n)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_hits_total"); v != 0 {
		t.Fatalf("cache_hits during warm-up = %d, want 0", v)
	}

	// Recent entry survived -> hit.
	if rec := doGet(t, h, "/v1/models", "Bearer k005"); rec.Code != 200 {
		t.Fatal("recent entry must hit")
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_hits_total"); v != 1 {
		t.Fatalf("cache_hits = %d, want 1 (k005 must be cached)", v)
	}
	if got := upHits.Load(); got != n {
		t.Fatalf("hit proxied anyway: upstream hits = %d", got)
	}

	// Oldest entries were evicted -> miss + re-proxy.
	doGet(t, h, "/v1/models", "Bearer k000")
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_misses_total"); v != n+1 {
		t.Fatalf("cache_misses = %d, want %d (evicted k000 must miss)", v, n+1)
	}
	if got := upHits.Load(); got != n+1 {
		t.Fatalf("evicted entry must re-proxy: upstream hits = %d, want %d", got, n+1)
	}
	// admitted_total tracks everything that queued: n warm-ups + 1 eviction miss.
	if v := metricsCounter(t, h, "gothrottle_admitted_total"); v != n+1 {
		t.Fatalf("admitted_total = %d, want %d (the one hit must not count)", v, n+1)
	}
}

// F7 / edge cases: TTL=0 disables, negative clamps to 0, garbage falls back
// to the default, whitespace-only path lists parse to an empty allow-list.
func TestQACacheConfigEdgeValues(t *testing.T) {
	t.Setenv("DISCOVERY_CACHE_TTL_SECONDS", "0")
	t.Setenv("DISCOVERY_CACHE_PATHS", defaultCachePaths)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CacheTTL != 0 {
		t.Fatalf("TTL=0 -> %v, want 0 (disabled)", cfg.CacheTTL)
	}

	t.Setenv("DISCOVERY_CACHE_TTL_SECONDS", "-7")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CacheTTL != 0 {
		t.Fatalf("negative TTL -> %v, want clamped 0", cfg.CacheTTL)
	}

	t.Setenv("DISCOVERY_CACHE_TTL_SECONDS", "not-a-number")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CacheTTL != defaultCacheTTL*time.Second {
		t.Fatalf("garbage TTL -> %v, want default %ds", cfg.CacheTTL, defaultCacheTTL)
	}

	t.Setenv("DISCOVERY_CACHE_PATHS", " , ,\t, ")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CachePaths) != 0 {
		t.Fatalf("whitespace-only paths -> %v, want empty allow-list", cfg.CachePaths)
	}

	t.Setenv("DISCOVERY_CACHE_PATHS", "/custom/a,/custom/b")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CachePaths) != 2 || !cfg.CachePaths["/custom/a"] || !cfg.CachePaths["/custom/b"] || cfg.CachePaths["/v1/models"] {
		t.Fatalf("custom paths -> %v, want exactly /custom/a,/custom/b", cfg.CachePaths)
	}
}

// AC 2 + AC 5 with strict metrics accounting (hits=1 misses=1 admitted=1),
// then TTL expiry through the handler with a 1s TTL re-proxies and refreshes.
func TestQAHandlerMetricsAccounting(t *testing.T) {
	up, upHits := countingUpstream(t)
	cfg := testCfg(t, mustParse(t, up.URL), time.Second)
	h := buildHandler(cfg, NewScheduler(2, 1000, 100), NewMetrics(), newDiscoveryCache(time.Second))

	doGet(t, h, "/v1/models", "Bearer acct") // miss
	doGet(t, h, "/v1/models", "Bearer acct") // hit
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_hits_total"); v != 1 {
		t.Fatalf("cache_hits = %d, want 1", v)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_misses_total"); v != 1 {
		t.Fatalf("cache_misses = %d, want 1", v)
	}
	if v := metricsCounter(t, h, "gothrottle_admitted_total"); v != 1 {
		t.Fatalf("admitted_total = %d, want 1", v)
	}
	if n := upHits.Load(); n != 1 {
		t.Fatalf("upstream hits = %d, want 1", n)
	}

	// AC 5: past TTL the next GET re-proxies and refreshes.
	time.Sleep(1100 * time.Millisecond)
	doGet(t, h, "/v1/models", "Bearer acct")
	if n := upHits.Load(); n != 2 {
		t.Fatalf("after TTL expiry upstream hits = %d, want 2", n)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_misses_total"); v != 2 {
		t.Fatalf("cache_misses = %d, want 2 (expired entry counts as a miss)", v)
	}
	doGet(t, h, "/v1/models", "Bearer acct") // refreshed entry serves
	if n := upHits.Load(); n != 2 {
		t.Fatalf("refreshed entry must hit: upstream hits = %d, want 2", n)
	}
	if v := metricsCounter(t, h, "gothrottle_discovery_cache_hits_total"); v != 2 {
		t.Fatalf("cache_hits = %d, want 2", v)
	}
}

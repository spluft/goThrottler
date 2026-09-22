# SPEC — gothrottler discovery-response cache

- Epic: t_54812c74 (board spluft, project gothrottler)
- Backend card: t_64aac7df
- Repo: /opt/git/spluft/gothrottler (remote github.com/spluft/goThrottler, main @ 98e59d7)
- Component: single-file `main.go` (queue-based soft rate limiter reverse proxy)

## 1. Business description

goThrottler fronts an upstream LLM gateway with a FIFO queue (concurrency
semaphore + sliding-window RPM). Every request — including cheap, static
"discovery" polling — must enter the queue to be served.

Agents (OpenClaw / Codex / Ollama-compatible clients) poll
`GET /v1/models`, `GET /version`, `GET /api/tags`, `GET /v1/props` with a
~2 s client timeout. Under load the p50 queue wait is ~15 s, so these polls:

1. burn queue slots and RPM budget that real inference traffic needs;
2. time out client-side, leaving the queue and logging ~2 000
   "client left the queue" lines per 5 days;
3. ask for responses that essentially never change (a model list that mutates
   at most when the upstream config changes).

Value: serve discovery GETs from a small in-memory TTL cache, keyed per
credential, **without entering the scheduler queue at all**. Queue slots, RPM
budget and log noise drop immediately; clients get sub-millisecond answers.

## 2. Use cases

UC-1 Poll from cache.
  Actor: agent polling `/v1/models` every few seconds.
  Flow: first miss → proxied through the queue normally, 2xx response stored.
  Subsequent identical requests within TTL → written straight to the client;
  no queue, no rate slot, no concurrency slot; log line `cache hit <path> | <clientInfo>`.
  Error path: none specific — a hit cannot fail differently from a normal
  proxied response of the same bytes.

UC-2 Isolation between keys.
  Actor: two clients with different credentials.
  Flow: client A's `/v1/models` (model list X) never served to client B
  (model list Y). The cache key includes `keyFingerprint(r)`, so different
  credentials are different entries.

UC-3 Staleness bound.
  Flow: an entry older than TTL_SECONDS (default 300) is treated as a miss
  and replaced by the next proxied 2xx response. Worst-case staleness of a
  discovery answer = TTL.

UC-4 Everything else unchanged.
  Any request that is not an exact method-GET + exact allow-listed path match
  (e.g. `POST /v1/chat/completions`, `GET /v1/models/extra`) flows through
  auth → queue → proxy exactly as before, with the same counters.

Alternative flows:
  - Non-2xx upstream response (401, 404, 502, …) is never cached; it is
    forwarded as today and the next request is another miss.
  - Upstream body larger than the per-entry cap (1 MiB) is streamed normally
    but not stored.
  - `DISCOVERY_CACHE_TTL_SECONDS=0` disables the cache (no reads, no writes).

## 3. Requirements

Functional:
  F1. Exact match only: `r.Method == "GET"` and `r.URL.Path` ∈
      `DISCOVERY_CACHE_PATHS` (comma-separated, default
      `/v1/models,/version,/api/tags,/v1/props`). No prefix/wildcard matching.
  F2. Cache key = path + `"|"` + `keyFingerprint(r)` (the credential's
      SHA-256 short hash; "anon" without credential). Query strings are not
      part of the key — acceptable because the key already isolates
      credentials and none of the four discovery endpoints is
      query-parameterized by clients.
  F3. Hit path: write cached status + Content-Type + body; log
      `cache hit <path> | <clientInfo>`; MUST NOT call `sched.WaitCtx`,
      MUST NOT touch inflight/RPM/queue counters, MUST NOT increment
      `gothrottle_admitted_total`.
  F4. Miss on a discovery path: proxy through the queue exactly as today; on
      a final 2xx upstream response store {status, content-type, body}.
  F5. Auth: cache is consulted only after the `REQUIRE_AUTH` anon-rejection
      gate, so caching never bypasses authentication.
  F6. New counters `gothrottle_discovery_cache_hits_total` and
      `gothrottle_discovery_cache_misses_total` rendered by `Metrics.Render`
      (misses count discovery-eligible requests that were not served from
      cache; they still count in admitted_total as normal queue traffic).
  F7. Env config follows the existing style (`getEnv`/`envInt`):
      `DISCOVERY_CACHE_TTL_SECONDS` (default 300),
      `DISCOVERY_CACHE_PATHS` (default above). Empty/whitespace path segments
      are ignored.

Non-functional:
  N1. stdlib only — `go.mod`, `go.sum`, `vendor/` must not change.
  N2. Bounded memory: ≤ 128 distinct entries, ≤ 1 MiB stored per entry
      (oversized responses are skipped, not truncated), total stored body
      bytes ≤ 128 MiB hard cap; drop-oldest (LRU) on entry overflow. Heap
      must stay flat after warm-up under sustained polling.
  N3. Thread safety: concurrent hits/misses/evictions race-clean
      (`go test -race`).
  N4. Cache hits add no measurable latency (map read under RWMutex) and no
      log volume beyond one line per hit.
  N5. Streaming behaviour of everything else (SSE, FlushInterval=-1) is
      untouched.

## 4. Technical design

All changes in `main.go`.

### 4.1 Config (`loadConfig`)

```go
CacheTTL   time.Duration // envInt("DISCOVERY_CACHE_TTL_SECONDS", 300) * time.Second
CachePaths map[string]bool // parsed from env string, exact paths
```

### 4.2 Cache structure

```go
const (
    cacheMaxEntries    = 128        // distinct entries
    cacheMaxEntryBytes = 1 << 20    // 1 MiB stored body per entry
)

type cacheEntry struct {
    status      int
    contentType string
    body        []byte
    expires     time.Time
    key         string // mirrored for O(1) LRU eviction
}

type discoveryCache struct {
    mu      sync.RWMutex
    ttl     time.Duration
    maxByt  int
    entries map[string]*list.Element // key -> *cacheEntry (front = MRU)
    order   *list.List               // LRU order, front = most recently used
}
```

- `get(key)` — `RLock`, look up, check `expires`; on a live hit promote to
  front under a write `Lock`. Returns a value copy of the small metadata +
  the body slice, which is treated as immutable after store (the proxy
  buffers a private copy on store), so handing the slice out read-only is
  race-free.
- `store(key, status, ct, body)` — no-op if `ttl <= 0` or
  `len(body) > maxByt`. Replaces an existing entry in place (fresh expiry,
  promoted to MRU); otherwise appends as MRU and, when
  `len(entries) > cacheMaxEntries`, removes the list back (oldest) and its
  map slot.
- Expiry is lazy (checked on `get`); expired entries only leave via a new
  store or LRU eviction, so memory stays bounded without a background reaper.

### 4.3 Response capture (miss path)

`captureWriter` embeds the existing `*statusRecorder` (pass-through
Write/WriteHeader/Flush) and buffers body bytes while
`status ∈ 2xx && total ≤ cacheMaxEntryBytes`. Any of: first status not 2xx,
body exceeding the cap, or `WriteHeader` after body writes began (proxy
ErrorHandler after a partial stream) → permanently non-storable
(`storable=false`). On handler return, the proxy code stores
`rec.status`, the captured `Content-Type`, and the buffer when storable.

The existing `statusRecorder` stays wrapped underneath, so queue
metrics/log lines are unchanged for misses.

### 4.4 Handler flow (`mux.HandleFunc("/")`)

```
auth gate (unchanged)
if r.Method == GET && cfg.CachePaths[r.URL.Path] && cache.enabled():
    if e, ok := cache.get(path + "|" + keyFingerprint(r)); ok:
        metrics.ObserveCacheHit()
        write status + Content-Type + body; log "cache hit <path> | <clientInfo>"; return
    metrics.ObserveCacheMiss()
    wrap w in captureWriter
sched.WaitCtx … (unchanged queue path)
rp.ServeHTTP(w, r)
if captureWriter && storable: cache.store(...)
metrics.ObserveResponse / log (unchanged)
```

The hit branch returns before `WaitCtx`, so the scheduler, its counters and
the "client left the queue" path are structurally untouched on hits.

### 4.5 Metrics

Two fields on `Metrics` (guarded by its existing mutex): `cacheHits`,
`cacheMisses` int64 with `ObserveCacheHit/ObserveCacheMiss`, rendered after
`gothrottle_client_aborts_total`:

```
# HELP gothrottle_discovery_cache_hits_total Discovery GETs served from cache (no queue).
# TYPE gothrottle_discovery_cache_hits_total counter
gothrottle_discovery_cache_hits_total N
# HELP gothrottle_discovery_cache_misses_total Discovery GETs proxied through the queue (cache miss/expired).
# TYPE gothrottle_discovery_cache_misses_total counter
gothrottle_discovery_cache_misses_total N
```

`admitted_total` semantics unchanged (misses legitimately queue).

## 5. Edge cases

- Two `WriteHeader` calls (200 streamed then proxy error) → not cached.
- Body exactly 1 MiB → cached; 1 MiB + 1 byte → not cached, still forwarded.
- HEAD/POST to a discovery path → normal queue path (exact method match).
- `/v1/models/extra` (prefix but not exact) → normal queue path.
- Anonymous requests when `REQUIRE_AUTH=0` → cached under key `…|anon`
  (same fingerprint as today's logs; no new leak — fingerprint is already
  SHA-256 derived and the cached body is the same body anon already sees).
- Cache disabled (`TTL=0`) → zero allocations, zero lookups; the path simply
  stays on the plain queue path (no hit/miss counters move).

## 6. Out of scope

- No invalidation API (TTL expiry is the invalidation mechanism).
- No shared/persistent cache, no cross-replica coherence.
- No compression, no Vary/ETag/Last-Modified handling.
- No scheduler changes; REQUIRE_AUTH semantics unchanged.

## 7. Acceptance criteria (verifiable)

1. `go build ./... && go test -race ./...` green; `git diff` shows no changes
   to `go.mod`, `go.sum`, `vendor/`.
2. Two sequential `GET /v1/models` with the same Authorization header and a
   counting upstream: upstream hits == 1; second response byte-identical;
   `/metrics` shows hits=1, misses=1, admitted_total=1.
3. Same path with two different Authorization values: upstream hits == 2; no
   response from key A ever served to key B.
4. `POST /v1/models` and `GET /v1/models/extra` never hit the cache
   (hits stays 0; admitted_total increments).
5. After TTL elapses (test uses TTL=1s), the next GET is a miss and refreshes
   the entry.
6. Non-2xx upstream (500) is not cached (two requests → two upstream hits).
7. >128 distinct keys → entry count capped at 128 (oldest evicted); a >1 MiB
   body is not stored (metrics show 2 misses for two such requests).
8. 1000 concurrent mixed requests under `-race`: no data race, scheduler
   counters only reflect non-hit traffic.

## 8. Test plan outline (for QA card t_94c52676)

Unit (no network): `discoveryCache` get/store/TTL/LRU/size-cap behaviour;
`loadConfig` parsing of the two new envs; `Metrics.Render` includes the two
new counters.
Integration: `httptest.Server` as upstream with a request counter +
in-process handler stack (a small `buildHandler(cfg, sched, metrics, cache,
upstream)` seam extracted from `main` so tests reuse the exact production
wiring). Cases = acceptance criteria 2–8.
Memory: warm 128 keys × ~64 KiB bodies, then 10 k hits; `runtime.MemStats`
Alloc delta after warm-up < small constant (heap flat).
Concurrency: `-race` hammering get/store through the handler.

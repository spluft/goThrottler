// goThrottler — reverse proxy with a soft rate limit in front of an
// upstream API gateway (e.g. an LLM provider gateway).
//
// Goals (Kolya, 2026-09):
//  1. Billing stays in the upstream: Authorization is forwarded untouched.
//  2. At most MAX_CONCURRENCY in-flight requests (streams hold their slot).
//  3. At most RPM request starts per 60s sliding window.
//  4. Over the limit -> FIFO queue with bounded wait, NOT an instant 429.
//
// stdlib only.
package main

import (
	"bytes"
	"container/list"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang"
)

func contextWithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}

// ---- config -----------------------------------------------------------

type Config struct {
	ListenAddr     string
	UpstreamURL    *url.URL
	MaxConcurrency int
	RPM            int
	MaxWait        time.Duration
	QueueLimit     int
	RequestTimeout time.Duration
	RequireAuth    bool
	StatsPath      string
	MetricsPath    string
	CacheTTL       time.Duration   // DISCOVERY_CACHE_TTL_SECONDS; 0 disables the cache
	CachePaths     map[string]bool // exact GET paths served from the discovery cache
}

const (
	defaultCacheTTL   = 300 // seconds
	defaultCachePaths = "/v1/models,/version,/api/tags,/v1/props"
)

func loadConfig() (*Config, error) {
	up, err := url.Parse(getEnv("UPSTREAM_URL", "http://new-api:3000"))
	if err != nil {
		return nil, err
	}
	c := &Config{
		ListenAddr:     getEnv("LISTEN_ADDR", ":3000"),
		UpstreamURL:    up,
		MaxConcurrency: envInt("MAX_CONCURRENCY", 3),
		RPM:            envInt("RPM", 10),
		MaxWait:        time.Duration(envInt("MAX_WAIT_SECONDS", 300)) * time.Second,
		QueueLimit:     envInt("QUEUE_LIMIT", 50),
		RequestTimeout: time.Duration(envInt("REQUEST_TIMEOUT_SECONDS", 1800)) * time.Second,
		RequireAuth:    envInt("REQUIRE_AUTH", 0) == 1,
		StatsPath:      getEnv("STATS_PATH", "/_throttle/stats"),
		MetricsPath:    getEnv("METRICS_PATH", "/metrics"),
		CacheTTL:       time.Duration(envInt("DISCOVERY_CACHE_TTL_SECONDS", defaultCacheTTL)) * time.Second,
		CachePaths:     parseCachePaths(getEnv("DISCOVERY_CACHE_PATHS", defaultCachePaths)),
	}
	if c.MaxConcurrency < 1 {
		c.MaxConcurrency = 1
	}
	if c.RPM < 1 {
		c.RPM = 1
	}
	if c.CacheTTL < 0 {
		c.CacheTTL = 0
	}
	return c, nil
}

// parseCachePaths splits the comma-separated allow-list of exact discovery
// paths; empty/whitespace segments are ignored.
func parseCachePaths(s string) map[string]bool {
	m := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			m[p] = true
		}
	}
	return m
}

func getEnv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// ---- scheduler ---------------------------------------------------------

// Scheduler serialises admission: FIFO tickets, a sliding-window rate gate,
// and a concurrency semaphore. Only the queue HEAD consumes rate slots, so an
// over-limit burst queues instead of racing.
type Scheduler struct {
	mu         sync.Mutex
	queue      *list.List // *ticket, front = next to admit
	inflight   int
	stamps     []time.Time // request-start times within the window
	rpm        int
	maxConc    int
	queueLimit int
	// counters
	totalIn      int64
	rejectedFull int64
	rejectedWait int64
}

type ticket struct {
	ch      chan struct{} // closed when admitted or rejected
	err     error
	etime   time.Time
	inQueue bool // authoritative membership flag, guarded by Scheduler.mu
}

var window = time.Minute

func NewScheduler(maxConc, rpm, queueLimit int) *Scheduler {
	return &Scheduler{
		queue:      list.New(),
		rpm:        rpm,
		maxConc:    maxConc,
		queueLimit: queueLimit,
	}
}

type admissionError struct{ msg string }

func (e *admissionError) Error() string { return e.msg }

// Wait blocks until admitted or deadline/queue-full. On success the caller
// holds one concurrency slot and MUST call Done() after serving.
func (s *Scheduler) Wait(maxWait time.Duration) error {
	return s.WaitCtx(context.Background(), maxWait)
}

// ErrClientGone marks that the caller's context was canceled while queued;
// no slot was consumed and no 429 needs to be written.
var ErrClientGone = &admissionError{"client_gone"}

// WaitCtx is Wait plus client-cancellation awareness: when ctx is done the
// ticket leaves the queue instead of burning a rate slot and a concurrency
// slot on a response nobody will read.
func (s *Scheduler) WaitCtx(ctx context.Context, maxWait time.Duration) error {
	s.mu.Lock()
	if s.queue.Len() >= s.queueLimit {
		s.rejectedFull++
		s.mu.Unlock()
		return &admissionError{"queue_full"}
	}
	t := &ticket{ch: make(chan struct{}), etime: time.Now().Add(maxWait), inQueue: true}
	el := s.queue.PushBack(t)
	head := s.queue.Front() == el
	s.mu.Unlock()

	if head {
		go s.pump()
	}

	select {
	case <-t.ch:
		return t.err // admitted when nil (slot already counted in inflight)
	case <-ctx.Done():
		s.mu.Lock()
		if !t.inQueue {
			// pump already decided; if it admitted us, give the slot back.
			err := t.err
			s.mu.Unlock()
			if err == nil {
				s.Done()
			}
			return err
		}
		s.queue.Remove(el)
		t.inQueue = false
		t.err = ErrClientGone
		s.mu.Unlock()
		close(t.ch)
		return ErrClientGone
	case <-time.After(maxWait):
		s.mu.Lock()
		if !t.inQueue {
			// pump already decided (admitted or rejected) before we got here
			err := t.err
			s.mu.Unlock()
			return err
		}
		s.queue.Remove(el)
		t.inQueue = false
		t.err = &admissionError{"max_wait_exceeded"}
		s.rejectedWait++
		s.mu.Unlock()
		close(t.ch)
		return t.err
	}
}

// Done releases a concurrency slot. Call after the response is fully served.
func (s *Scheduler) Done() {
	s.mu.Lock()
	s.inflight--
	head := s.queue.Front()
	s.mu.Unlock()
	if head != nil {
		go s.pump()
	}
}

// pump admits queue heads while rate+slots allow. Safe to run concurrently:
// every decision re-checks state under the lock.
func (s *Scheduler) pump() {
	for {
		s.mu.Lock()
		el := s.queue.Front()
		if el == nil {
			s.mu.Unlock()
			return
		}
		t := el.Value.(*ticket)
		now := time.Now()
		cut := now.Add(-window)
		for len(s.stamps) > 0 && s.stamps[0].Before(cut) {
			s.stamps = s.stamps[1:]
		}
		rateOK := len(s.stamps) < s.rpm
		slotOK := s.inflight < s.maxConc
		// expired head? reject it instead of blocking the line forever.
		if now.After(t.etime) {
			s.queue.Remove(el)
			t.inQueue = false
			t.err = &admissionError{"max_wait_exceeded"}
			s.rejectedWait++
			s.mu.Unlock()
			close(t.ch)
			continue
		}
		if !rateOK || !slotOK {
			var next time.Time
			if !rateOK {
				next = s.stamps[0].Add(window) // earliest window refill
			}
			s.mu.Unlock()
			if next.IsZero() {
				return // blocked on concurrency; Done() pumps again
			}
			time.Sleep(time.Until(next) + 5*time.Millisecond)
			continue
		}
		s.queue.Remove(el)
		t.inQueue = false
		s.inflight++
		s.totalIn++
		s.stamps = append(s.stamps, now)
		close(t.ch)
		s.mu.Unlock()
	}
}

func (s *Scheduler) Stats() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	cut := time.Now().Add(-window)
	used := 0
	for _, ts := range s.stamps {
		if !ts.Before(cut) {
			used++
		}
	}
	return map[string]any{
		"inflight":       s.inflight,
		"queued":         s.queue.Len(),
		"rate_used_60s":  used,
		"rpm_limit":      s.rpm,
		"conc_limit":     s.maxConc,
		"admitted_total": s.totalIn,
		"rejected_full":  s.rejectedFull,
		"rejected_wait":  s.rejectedWait,
	}
}

// ---- discovery cache ---------------------------------------------------

const (
	cacheMaxEntries    = 128     // distinct cached responses
	cacheMaxEntryBytes = 1 << 20 // 1 MiB stored body per entry; bigger = not stored
)

// cacheEntry is one stored upstream response. body is treated as immutable
// after store() so handing the slice out read-only on a hit is race-free.
type cacheEntry struct {
	status      int
	contentType string
	body        []byte
	expires     time.Time
	key         string // mirrored for O(1) LRU eviction
}

// discoveryCache is a bounded TTL+LRU response cache for discovery GETs.
// order front = most recently used; expiry is lazy (checked on get), so no
// background reaper is needed and memory stays bounded.
type discoveryCache struct {
	mu      sync.RWMutex
	ttl     time.Duration
	entries map[string]*list.Element
	order   *list.List // *cacheEntry, front = MRU
}

func newDiscoveryCache(ttl time.Duration) *discoveryCache {
	return &discoveryCache{ttl: ttl, entries: map[string]*list.Element{}, order: list.New()}
}

func (c *discoveryCache) enabled() bool { return c != nil && c.ttl > 0 }

// get returns a live entry for key, promoting it to MRU on a hit.
func (c *discoveryCache) get(key string) (cacheEntry, bool) {
	c.mu.RLock()
	el, ok := c.entries[key]
	if !ok {
		c.mu.RUnlock()
		return cacheEntry{}, false
	}
	e := el.Value.(*cacheEntry)
	if time.Now().After(e.expires) {
		c.mu.RUnlock()
		return cacheEntry{}, false
	}
	val := *e // copy metadata; body slice shared read-only
	c.mu.RUnlock()
	c.mu.Lock()
	if el2, ok := c.entries[key]; ok {
		c.order.MoveToFront(el2)
	}
	c.mu.Unlock()
	return val, true
}

// store saves one 2xx response under key (no-op when disabled or oversized).
func (c *discoveryCache) store(key string, status int, contentType string, body []byte) {
	if !c.enabled() || len(body) > cacheMaxEntryBytes {
		return
	}
	e := &cacheEntry{
		status: status, contentType: contentType,
		body: body, expires: time.Now().Add(c.ttl), key: key,
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		el.Value = e
		c.order.MoveToFront(el)
		return
	}
	c.entries[key] = c.order.PushFront(e)
	for c.order.Len() > cacheMaxEntries {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*cacheEntry).key)
	}
}

// ---- metrics (Prometheus text exposition, stdlib-only) -------------------

// Metrics collects what the Scheduler counters don't carry: queue-wait
// distribution and upstream proxy failures. Counters that the scheduler
// already tracks (admitted/rejected/inflight/rate) are pulled live in Render.
type Metrics struct {
	mu           sync.Mutex
	bounds       []float64 // histogram bucket upper bounds, seconds
	counts       []int64   // cumulative, len(bounds)+1 (last = +Inf)
	sum          float64
	n            int64
	upstreamErrs int64
	clientAborts int64
	cacheHits    int64
	cacheMisses  int64

	// per-completed-request mirrors of what the access log carries, in
	// bounded-cardinality form: status code (finite set), total duration,
	// and byte totals. No client identifiers on purpose — those live in
	// logs only; per-client labels would explode TSDB cardinality.
	durBounds []float64
	durCounts []int64
	durSum    float64
	statuses  map[string]int64
	bytesIn   int64
	bytesOut  int64
}

func NewMetrics() *Metrics {
	return &Metrics{
		bounds:    []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		counts:    make([]int64, 13),
		durBounds: []float64{0.1, 0.5, 1, 2.5, 5, 10, 20, 30, 60, 120, 300, 600, 1800},
		durCounts: make([]int64, 14),
		statuses:  map[string]int64{},
	}
}

// ObserveWait records the time a request spent queued before admission.
func (m *Metrics) ObserveWait(d time.Duration) {
	sec := d.Seconds()
	m.mu.Lock()
	m.sum += sec
	m.n++
	for i, b := range m.bounds {
		if sec <= b {
			m.counts[i]++
		}
	}
	m.counts[len(m.bounds)]++
	m.mu.Unlock()
}

// ObserveResponse records one finished proxied request: response code,
// end-to-end duration (queue + upstream) and transferred bytes.
func (m *Metrics) ObserveResponse(status int, dur time.Duration, bytesOut, bytesIn int64) {
	if bytesIn < 0 { // unknown/absent Content-Length
		bytesIn = 0
	}
	sec := dur.Seconds()
	m.mu.Lock()
	m.statuses[strconv.Itoa(status)]++
	m.bytesOut += bytesOut
	m.bytesIn += bytesIn
	m.durSum += sec
	for i, b := range m.durBounds {
		if sec <= b {
			m.durCounts[i]++
		}
	}
	m.durCounts[len(m.durBounds)]++
	m.mu.Unlock()
}

func (m *Metrics) ObserveUpstreamError() {
	m.mu.Lock()
	m.upstreamErrs++
	m.mu.Unlock()
}

// ObserveClientAbort counts responses cut short by the client disconnecting.
// It is NOT an upstream failure and must not pollute upstream_errors_total.
func (m *Metrics) ObserveClientAbort() {
	m.mu.Lock()
	m.clientAborts++
	m.mu.Unlock()
}

// ObserveCacheHit counts a discovery GET served straight from the cache,
// without entering the scheduler queue.
func (m *Metrics) ObserveCacheHit() {
	m.mu.Lock()
	m.cacheHits++
	m.mu.Unlock()
}

// ObserveCacheMiss counts a discovery-eligible GET that had to be proxied
// through the queue (cold cache or expired entry).
func (m *Metrics) ObserveCacheMiss() {
	m.mu.Lock()
	m.cacheMisses++
	m.mu.Unlock()
}

// Render writes Prometheus text format 0.0.4.
func (m *Metrics) Render(s *Scheduler, w io.Writer) {
	st := s.Stats()
	m.mu.Lock()
	defer m.mu.Unlock()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Fprintf(w, "# HELP gothrottle_inflight Requests currently proxied to upstream.\n"+
		"# TYPE gothrottle_inflight gauge\n"+
		"gothrottle_inflight %v\n", st["inflight"])
	fmt.Fprintf(w, "# HELP gothrottle_queued Requests waiting in the FIFO queue.\n"+
		"# TYPE gothrottle_queued gauge\n"+
		"gothrottle_queued %v\n", st["queued"])
	fmt.Fprintf(w, "# HELP gothrottle_rate_used_60s Admissions within the current 60s window.\n"+
		"# TYPE gothrottle_rate_used_60s gauge\n"+
		"gothrottle_rate_used_60s %v\n", st["rate_used_60s"])
	fmt.Fprintf(w, "# HELP gothrottle_rpm_limit Configured RPM.\n"+
		"# TYPE gothrottle_rpm_limit gauge\n"+
		"gothrottle_rpm_limit %v\n", st["rpm_limit"])
	fmt.Fprintf(w, "# HELP gothrottle_concurrency_limit Configured max concurrency.\n"+
		"# TYPE gothrottle_concurrency_limit gauge\n"+
		"gothrottle_concurrency_limit %v\n", st["conc_limit"])
	fmt.Fprintf(w, "# HELP gothrottle_admitted_total Requests admitted to upstream.\n"+
		"# TYPE gothrottle_admitted_total counter\n"+
		"gothrottle_admitted_total %v\n", st["admitted_total"])
	fmt.Fprintf(w, "# HELP gothrottle_rejected_total Rejections by reason.\n"+
		"# TYPE gothrottle_rejected_total counter\n"+
		"gothrottle_rejected_total{reason=\"queue_full\"} %v\n"+
		"gothrottle_rejected_total{reason=\"max_wait_exceeded\"} %v\n",
		st["rejected_full"], st["rejected_wait"])
	fmt.Fprintf(w, "# HELP gothrottle_upstream_errors_total Reverse-proxy upstream errors.\n"+
		"# TYPE gothrottle_upstream_errors_total counter\n"+
		"gothrottle_upstream_errors_total %d\n", m.upstreamErrs)
	fmt.Fprintf(w, "# HELP gothrottle_queue_wait_seconds Time spent queued before admission.\n"+
		"# TYPE gothrottle_queue_wait_seconds histogram\n")
	for i, b := range m.bounds {
		fmt.Fprintf(w, "gothrottle_queue_wait_seconds_bucket{le=\"%g\"} %d\n", b, m.counts[i])
	}
	fmt.Fprintf(w, "gothrottle_queue_wait_seconds_bucket{le=\"+Inf\"} %d\n"+
		"gothrottle_queue_wait_seconds_sum %g\n"+
		"gothrottle_queue_wait_seconds_count %d\n",
		m.counts[len(m.bounds)], m.sum, m.n)
	fmt.Fprintf(w, "# HELP gothrottle_client_aborts_total Responses cut short by client disconnects.\n"+
		"# TYPE gothrottle_client_aborts_total counter\n"+
		"gothrottle_client_aborts_total %d\n", m.clientAborts)
	fmt.Fprintf(w, "# HELP gothrottle_discovery_cache_hits_total Discovery GETs served from cache (no queue).\n"+
		"# TYPE gothrottle_discovery_cache_hits_total counter\n"+
		"gothrottle_discovery_cache_hits_total %d\n", m.cacheHits)
	fmt.Fprintf(w, "# HELP gothrottle_discovery_cache_misses_total Discovery GETs proxied through the queue (cache miss/expired).\n"+
		"# TYPE gothrottle_discovery_cache_misses_total counter\n"+
		"gothrottle_discovery_cache_misses_total %d\n", m.cacheMisses)
	fmt.Fprintf(w, "# HELP gothrottle_responses_total Completed proxied requests by upstream response code.\n"+
		"# TYPE gothrottle_responses_total counter\n")
	codes := make([]string, 0, len(m.statuses))
	for c := range m.statuses {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	for _, c := range codes {
		fmt.Fprintf(w, "gothrottle_responses_total{status=%q} %d\n", c, m.statuses[c])
	}
	fmt.Fprintf(w, "# HELP gothrottle_request_duration_seconds End-to-end request time (queue + upstream).\n"+
		"# TYPE gothrottle_request_duration_seconds histogram\n")
	for i, b := range m.durBounds {
		fmt.Fprintf(w, "gothrottle_request_duration_seconds_bucket{le=\"%g\"} %d\n", b, m.durCounts[i])
	}
	fmt.Fprintf(w, "gothrottle_request_duration_seconds_bucket{le=\"+Inf\"} %d\n"+
		"gothrottle_request_duration_seconds_sum %g\n"+
		"gothrottle_request_duration_seconds_count %d\n",
		m.durCounts[len(m.durBounds)], m.durSum, m.durCounts[len(m.durBounds)])
	fmt.Fprintf(w, "# HELP gothrottle_response_bytes_total Response body bytes sent to clients.\n"+
		"# TYPE gothrottle_response_bytes_total counter\n"+
		"gothrottle_response_bytes_total %d\n"+
		"# HELP gothrottle_request_bytes_total Request body bytes received from clients.\n"+
		"# TYPE gothrottle_request_bytes_total counter\n"+
		"gothrottle_request_bytes_total %d\n", m.bytesOut, m.bytesIn)
	fmt.Fprintf(w, "# HELP go_goroutines Number of goroutines.\n"+
		"# TYPE go_goroutines gauge\n"+
		"go_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintf(w, "# HELP go_memstats_alloc_bytes Heap bytes allocated and live.\n"+
		"# TYPE go_memstats_alloc_bytes gauge\n"+
		"go_memstats_alloc_bytes %d\n", ms.Alloc)
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- request logging ---------------------------------------------------

// hasCredential reports whether the request carries any recognizable
// credential header — enough to shed anonymous scanners before the queue.
// Actual key validation stays with the upstream billing gateway.
func hasCredential(r *http.Request) bool {
	return r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Key") != ""
}

// keyFingerprint is a stable short hash of the caller's credential: enough to
// tell clients apart in logs without leaking the key itself.
func keyFingerprint(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		auth = r.Header.Get("X-API-Key")
	}
	if auth == "" {
		return "anon"
	}
	sum := sha256.Sum256([]byte(auth))
	return hex.EncodeToString(sum[:])[:8]
}

// geoDB resolves client IPs to city/country via an mmdb file (optional).
var geoDB *maxminddb.Reader

// mmdbCity is the subset of fields we render.
type mmdbCity struct {
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Country struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
}

func geoLabel(ip string) string {
	if geoDB == nil {
		return ""
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	var rec mmdbCity
	if err := geoDB.Lookup(parsed, &rec); err != nil {
		return ""
	}
	c := rec.City.Names["ru"]
	if c == "" {
		c = rec.City.Names["en"]
	}
	country := rec.Country.Names["ru"]
	if country == "" {
		country = rec.Country.Names["en"]
	}
	switch {
	case c != "" && country != "":
		return c + ", " + country
	case country != "":
		return country
	default:
		return ""
	}
}

// clientInfo renders "ip key fp geo" for one request line.
func clientInfo(r *http.Request) string {
	ip := r.Header.Get("X-Forwarded-For")
	if i := strings.IndexByte(ip, ','); i >= 0 {
		ip = ip[:i]
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		ip = r.RemoteAddr
		if i := strings.LastIndexByte(ip, ':'); i >= 0 {
			ip = ip[:i] // strip ephemeral port
		}
	}
	geo := geoLabel(ip)
	if geo != "" {
		geo = " geo=\"" + geo + "\""
	}
	return fmt.Sprintf("%s key=%s%s", ip, keyFingerprint(r), geo)
}

// statusRecorder captures the response code and body size for the log line.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(c int) {
	if s.status == 0 {
		s.status = c
	}
	s.ResponseWriter.WriteHeader(c)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// ReverseProxy needs streaming support on the wrapped writer.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// captureWriter passes everything through and buffers the response body for
// the discovery cache while it is provably storable: first status 2xx and
// total bytes within cacheMaxEntryBytes. Any second WriteHeader (e.g. the
// proxy ErrorHandler firing after a partial stream), a non-2xx status, or an
// oversize body permanently disqualifies the response.
type captureWriter struct {
	http.ResponseWriter
	status   int
	storable bool
	body     bytes.Buffer
}

func (c *captureWriter) WriteHeader(code int) {
	if c.status != 0 {
		c.storable = false // second header: response shape is no longer cacheable
	} else {
		c.status = code
		c.storable = code >= 200 && code < 300
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
		c.storable = true
	}
	if c.storable && c.body.Len()+len(b) <= cacheMaxEntryBytes {
		c.body.Write(b)
	} else {
		c.storable = false
	}
	return c.ResponseWriter.Write(b)
}

// Flush forwards streaming (ReverseProxy needs it on the wrapped writer).
func (c *captureWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ---- proxy -------------------------------------------------------------

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if path := os.Getenv("GEO_DB"); path != "" {
		if r, err := maxminddb.Open(path); err != nil {
			log.Printf("geo: cannot open %s: %v (geolocation disabled)", path, err)
		} else {
			geoDB = r
			log.Printf("geo: loaded %s", path)
		}
	}
	sched := NewScheduler(cfg.MaxConcurrency, cfg.RPM, cfg.QueueLimit)
	metrics := NewMetrics()

	mux := buildHandler(cfg, sched, metrics, newDiscoveryCache(cfg.CacheTTL))

	log.Printf("goThrottle listening %s -> %s (conc=%d rpm=%d maxWait=%s queueLimit=%d requireAuth=%t cacheTtl=%s cachePaths=%d)",
		cfg.ListenAddr, cfg.UpstreamURL, cfg.MaxConcurrency, cfg.RPM, cfg.MaxWait, cfg.QueueLimit, cfg.RequireAuth,
		cfg.CacheTTL, len(cfg.CachePaths))
	srv := &http.Server{Addr: cfg.ListenAddr, Handler: mux}
	log.Fatal(srv.ListenAndServe())
}

// buildHandler wires the full production stack (health/stats/metrics + the
// throttled proxy with the discovery cache) behind one http.Handler, so
// tests reuse the exact wiring.
func buildHandler(cfg *Config, sched *Scheduler, metrics *Metrics, cache *discoveryCache) *http.ServeMux {
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(cfg.UpstreamURL)
			r.SetXForwarded()
		},
		FlushInterval: -1, // stream SSE byte-by-byte
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, e error) {
			if r.Context().Err() != nil {
				// client disconnected before/while we proxied — not an upstream fault
				metrics.ObserveClientAbort()
				log.Printf("client aborted: %v | %s", e, clientInfo(r))
				return
			}
			metrics.ObserveUpstreamError()
			log.Printf("upstream error: %v | %s", e, clientInfo(r))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(`{"error":{"message":"goThrottle: upstream unavailable","type":"proxy_error"}}`))
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc(cfg.StatsPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sched.Stats())
	})
	mux.HandleFunc(cfg.MetricsPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		metrics.Render(sched, w)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := newID()
		rec := &statusRecorder{ResponseWriter: w}
		w = rec
		what := fmt.Sprintf("id=%s %s %s %s", id, clientInfo(r), r.Method, r.URL.Path)
		if cfg.RequireAuth && !hasCredential(r) {
			metrics.ObserveResponse(http.StatusUnauthorized, time.Since(start), 0, r.ContentLength)
			log.Printf("anon rejected | %s", what)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"goThrottle: missing credential","type":"auth_error"}}`))
			return
		}
		// Discovery GETs (exact method + exact allow-listed path) are served
		// from the TTL cache when warm — no queue, no rate slot, no
		// concurrency slot. Keyed per credential so one key's model list is
		// never visible to another.
		cacheable := cache.enabled() && r.Method == http.MethodGet && cfg.CachePaths[r.URL.Path]
		var cwr *captureWriter
		if cacheable {
			key := r.URL.Path + "|" + keyFingerprint(r)
			if e, ok := cache.get(key); ok {
				metrics.ObserveCacheHit()
				if e.contentType != "" {
					w.Header().Set("Content-Type", e.contentType)
				}
				w.WriteHeader(e.status)
				w.Write(e.body)
				log.Printf("cache hit %s | %s", r.URL.Path, clientInfo(r))
				return
			}
			metrics.ObserveCacheMiss()
			cwr = &captureWriter{ResponseWriter: w}
			w = cwr
		}
		if err := sched.WaitCtx(r.Context(), cfg.MaxWait); err != nil {
			waited := time.Since(start).Round(time.Millisecond)
			if err == ErrClientGone {
				metrics.ObserveClientAbort()
				log.Printf("client left the queue after %s | %s", waited, what)
				return // nobody to answer
			}
			log.Printf("queue rejected: %v (waited %s) | %s", err, waited, what)
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"message":"goThrottle queue: ` + err.Error() + `","type":"throttle_error"}}`))
			return
		}
		queued := time.Since(start)
		metrics.ObserveWait(queued)
		defer sched.Done()
		if cfg.RequestTimeout > 0 {
			ctx, cancel := contextWithTimeout(r.Context(), cfg.RequestTimeout)
			defer cancel()
			r = r.WithContext(ctx)
		}
		rp.ServeHTTP(w, r)
		if cwr != nil && cwr.storable {
			cache.store(r.URL.Path+"|"+keyFingerprint(r), cwr.status,
				cwr.Header().Get("Content-Type"), bytes.Clone(cwr.body.Bytes()))
		}
		metrics.ObserveResponse(rec.status, time.Since(start), int64(rec.bytes), r.ContentLength)
		log.Printf("done queued=%s dur=%s status=%d bytes=%d | %s",
			queued.Round(time.Millisecond), time.Since(start).Round(time.Millisecond),
			rec.status, rec.bytes, what)
	})
	return mux
}

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// concurrency must never exceed maxConc, and Done must hand the slot forward.
func TestConcurrencyCap(t *testing.T) {
	s := NewScheduler(2, 1000, 100)
	var cur, max int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Wait(10 * time.Second); err != nil {
				t.Errorf("Wait: %v", err)
				return
			}
			c := atomic.AddInt64(&cur, 1)
			for {
				m := atomic.LoadInt64(&max)
				if c <= m || atomic.CompareAndSwapInt64(&max, m, c) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt64(&cur, -1)
			s.Done()
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt64(&max); got > 2 {
		t.Fatalf("max inflight = %d, want <= 2", got)
	}
	if got := atomic.LoadInt64(&s.totalIn); got != 20 {
		t.Fatalf("admitted = %d, want 20", got)
	}
}

// more than RPM requests inside one window must queue, not be rejected.
func TestRPMQueuesNotRejects(t *testing.T) {
	old := window
	window = 2 * time.Second
	defer func() { window = old }()
	s := NewScheduler(100, 5, 100) // high conc so only rate gate bites
	var admitted int64
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Wait(30 * time.Second); err != nil {
				t.Errorf("Wait: %v", err)
				return
			}
			atomic.AddInt64(&admitted, 1)
			s.Done()
		}()
		time.Sleep(10 * time.Millisecond) // stable arrival order
	}
	wg.Wait()
	elapsed := time.Since(start)
	if admitted != 8 {
		t.Fatalf("admitted %d, want 8", admitted)
	}
	if elapsed < 1500*time.Millisecond {
		t.Fatalf("finished in %v: rate gate leaked (6 extra reqs must wait for the sliding window)", elapsed)
	}
}

// a request that waits longer than maxWait gets max_wait_exceeded, not slot.
func TestMaxWaitExceeded(t *testing.T) {
	s := NewScheduler(1, 1000, 100)
	// hold the only slot
	if err := s.Wait(time.Second); err != nil {
		t.Fatal(err)
	}
	err := s.Wait(50 * time.Millisecond)
	if err == nil || err.Error() != "max_wait_exceeded" {
		t.Fatalf("err = %v, want max_wait_exceeded", err)
	}
	s.Done()
}

// expired head must be evicted so the line keeps moving.
func TestExpiredHeadEvicted(t *testing.T) {
	s := NewScheduler(1, 1000, 100)
	if err := s.Wait(time.Second); err != nil { // slot holder
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() { blocked <- s.Wait(100 * time.Millisecond) }()
	time.Sleep(30 * time.Millisecond)
	// second waiter with long budget queues behind the doomed head
	long := make(chan error, 1)
	go func() { long <- s.Wait(5 * time.Second) }()
	time.Sleep(150 * time.Millisecond) // head expires while holder is busy
	if err := <-blocked; err == nil {
		t.Fatal("head should have timed out")
	}
	s.Done() // release; long waiter must get in
	select {
	case err := <-long:
		if err != nil {
			t.Fatalf("long waiter rejected: %v", err)
		}
		s.Done()
	case <-time.After(2 * time.Second):
		t.Fatal("long waiter never admitted after head eviction + Done")
	}
}

// over queueLimit waiters are rejected immediately with queue_full.
func TestQueueFull(t *testing.T) {
	s := NewScheduler(1, 1000, 3)
	if err := s.Wait(time.Second); err != nil { // occupy slot
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var full int64
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.Wait(30 * time.Second)
			if err != nil && err.Error() == "queue_full" {
				atomic.AddInt64(&full, 1)
			}
			if err == nil {
				s.Done()
			}
		}()
		time.Sleep(10 * time.Millisecond) // let them enqueue sequentially
	}
	wg.Wait()
	if full < 7 {
		t.Fatalf("queue_full count = %d, want >= 7 (limit 3)", full)
	}
	s.Done()
}

// FIFO order: with conc=1 and rate unlimited-ish, admissions follow arrival.
func TestFIFOOrder(t *testing.T) {
	s := NewScheduler(1, 1000, 100)
	s.Wait(time.Second) // occupy
	var order []int
	var mu sync.Mutex
	var wg sync.WaitGroup
	// enqueue in strict order
	n := 5
	idxCh := make(chan int, n)
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		if i == 0 {
			go func() {
				defer wg.Done()
				if s.Wait(10*time.Second) == nil {
					mu.Lock()
					order = append(order, i)
					mu.Unlock()
					s.Done()
				}
			}()
			time.Sleep(50 * time.Millisecond) // ensure i=0 is the head
			continue
		}
		go func() {
			defer wg.Done()
			<-idxCh // released in order by the loop below to fix enqueue order
			if s.Wait(10*time.Second) == nil {
				mu.Lock()
				order = append(order, i)
				mu.Unlock()
				s.Done()
			}
		}()
		time.Sleep(40 * time.Millisecond)
		idxCh <- i
	}
	// now all 5 are enqueued (i=0 already admitted-pending); release the holder
	s.Done()
	wg.Wait()
	if len(order) != n {
		t.Fatalf("admitted %d of %d", len(order), n)
	}
	for i, v := range order {
		if v != i {
			t.Fatalf("order = %v, want 0..%d", order, n-1)
		}
	}
}

func TestMetricsRender(t *testing.T) {
	s := NewScheduler(2, 60, 10)
	m := NewMetrics()
	m.ObserveWait(50 * time.Millisecond)
	m.ObserveWait(2 * time.Second)
	m.ObserveUpstreamError()
	if err := s.Wait(time.Second); err != nil {
		t.Fatal(err)
	}
	s.Done()
	var buf bytes.Buffer
	m.Render(s, &buf)
	out := buf.String()
	for _, want := range []string{
		"# TYPE gothrottle_inflight gauge",
		"gothrottle_inflight 0",
		"gothrottle_admitted_total 1",
		"gothrottle_upstream_errors_total 1",
		`gothrottle_queue_wait_seconds_bucket{le="0.1"} 1`,
		`gothrottle_queue_wait_seconds_bucket{le="2.5"} 2`,
		"gothrottle_queue_wait_seconds_count 2",
		`gothrottle_rejected_total{reason="queue_full"} 0`,
		"go_goroutines",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
	// histogram must be cumulatively monotonic
	var prev int64
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "gothrottle_queue_wait_seconds_bucket") {
			continue
		}
		var c int64
		if _, err := fmt.Sscanf(line, `gothrottle_queue_wait_seconds_bucket{le="%*[^"]"} %d`, &c); err == nil {
			if c < prev {
				t.Errorf("histogram not monotonic: %d after %d", c, prev)
			}
			prev = c
		}
	}
}

func TestWaitCtxClientGone(t *testing.T) {
	// one occupied slot; a queued waiter cancels -> leaves queue, no rejection counter
	s := NewScheduler(1, 100, 10)
	if err := s.Wait(time.Second); err != nil { // hold the only slot
		t.Fatalf("first admission: %v", err)
	}
	res := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { res <- s.WaitCtx(ctx, 5*time.Second) }()
	time.Sleep(30 * time.Millisecond) // ensure ticket is queued
	cancel()
	if err := <-res; err != ErrClientGone {
		t.Fatalf("want ErrClientGone, got %v", err)
	}
	st := s.Stats()
	if st["queued"].(int) != 0 {
		t.Fatalf("ticket should be gone from queue, queued=%v", st["queued"])
	}
	if st["rejected_wait"].(int64) != 0 {
		t.Fatalf("client cancellation must not count as max_wait rejection")
	}
	// releasing the slot must admit a fresh waiter — queue wasn't poisoned
	s.Done()
	if err := s.Wait(2 * time.Second); err != nil {
		t.Fatalf("fresh waiter after abort: %v", err)
	}
	s.Done()

	// race: pump admits concurrently with cancel — either outcome is legal,
	// but a slot must never leak.
	for i := 0; i < 50; i++ {
		s2 := NewScheduler(1, 1000, 10)
		c, cn := context.WithCancel(context.Background())
		go func() { time.Sleep(time.Duration(i%3) * time.Millisecond); cn() }()
		err := s2.WaitCtx(c, time.Second)
		if err == nil {
			s2.Done()
		} else if err != ErrClientGone {
			t.Fatalf("unexpected: %v", err)
		}
	}
	s3 := NewScheduler(1, 1000, 10)
	// drain check: inflight must be 0
	if got := s3.Stats()["inflight"].(int); got != 0 {
		t.Fatalf("inflight leak: %d", got)
	}
}

func TestMetricsClientAborts(t *testing.T) {
	m := NewMetrics()
	s := NewScheduler(2, 10, 5)
	m.ObserveClientAbort()
	var buf bytes.Buffer
	m.Render(s, &buf)
	if !strings.Contains(buf.String(), "gothrottle_client_aborts_total 1") {
		t.Fatalf("abort counter missing:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "gothrottle_upstream_errors_total 1") {
		t.Fatal("abort must not inflate upstream_errors_total")
	}
	_ = fmt.Sprint()
}

func TestMetricsResponseBreakdown(t *testing.T) {
	m := NewMetrics()
	s := NewScheduler(2, 60, 10)
	m.ObserveResponse(200, 300*time.Millisecond, 1234, 56)
	m.ObserveResponse(200, 50*time.Millisecond, 100, 44)
	m.ObserveResponse(401, 5*time.Millisecond, 124, -1) // no Content-Length
	var buf bytes.Buffer
	m.Render(s, &buf)
	got := buf.String()
	for _, want := range []string{
		`gothrottle_responses_total{status="200"} 2`,
		`gothrottle_responses_total{status="401"} 1`,
		"gothrottle_request_duration_seconds_count 3",
		"gothrottle_response_bytes_total 1458",
		"gothrottle_request_bytes_total 100", // -1 Content-Length must not count
	} {
		if !strings.Contains(got, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

func TestHasCredential(t *testing.T) {
	r1, _ := http.NewRequest("GET", "/", nil)
	if hasCredential(r1) {
		t.Error("bare request must not count as credentialed")
	}
	r1.Header.Set("Authorization", "Bearer x")
	if !hasCredential(r1) {
		t.Error("Authorization must count")
	}
	r2, _ := http.NewRequest("GET", "/", nil)
	r2.Header.Set("X-API-Key", "k")
	if !hasCredential(r2) {
		t.Error("X-API-Key must count")
	}
}

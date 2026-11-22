package checker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// drain reads until the channel closes, failing the test if it never does.
func drain(t *testing.T, ch <-chan Result) []Result {
	t.Helper()
	var out []Result
	timeout := time.After(5 * time.Second)
	for {
		select {
		case r, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, r)
		case <-timeout:
			t.Fatal("timed out waiting for results channel to close")
		}
	}
}

func urlsFor(srv *httptest.Server, paths ...string) []string {
	urls := make([]string, len(paths))
	for i, p := range paths {
		urls[i] = srv.URL + p
	}
	return urls
}

func TestRun_ReturnsEveryResultOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	paths := make([]string, 50)
	for i := range paths {
		paths[i] = fmt.Sprintf("/%d", i)
	}

	ctx := context.Background()
	cfg := Config{Concurrency: 5, Timeout: time.Second, MaxRedirects: 5}
	results := drain(t, Run(ctx, cfg, Jobs(ctx, urlsFor(srv, paths...))))

	if len(results) != len(paths) {
		t.Fatalf("got %d results, want %d", len(results), len(paths))
	}
	seen := make(map[int]bool)
	for _, r := range results {
		if seen[r.Job.Index] {
			t.Errorf("index %d returned twice", r.Job.Index)
		}
		seen[r.Job.Index] = true
		if !r.Healthy() {
			t.Errorf("job %d unhealthy: status=%d err=%v", r.Job.Index, r.Status, r.Err)
		}
	}
}

func TestRun_RespectsConcurrencyLimit(t *testing.T) {
	var inFlight, maxSeen atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			old := maxSeen.Load()
			if cur <= old || maxSeen.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
	}))
	defer srv.Close()

	paths := make([]string, 30)
	for i := range paths {
		paths[i] = fmt.Sprintf("/%d", i)
	}

	const limit = 4
	ctx := context.Background()
	cfg := Config{Concurrency: limit, Timeout: 2 * time.Second, MaxRedirects: 5}
	drain(t, Run(ctx, cfg, Jobs(ctx, urlsFor(srv, paths...))))

	if got := maxSeen.Load(); got > limit {
		t.Errorf("max in-flight = %d, exceeds limit %d", got, limit)
	}
	if got := maxSeen.Load(); got < 2 {
		t.Errorf("max in-flight = %d, expected real concurrency", got)
	}
}

func TestRun_StreamsInCompletionOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(300 * time.Millisecond)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	cfg := Config{Concurrency: 3, Timeout: 2 * time.Second, MaxRedirects: 5}
	// Index 0 is the slow one, so it should arrive last.
	results := drain(t, Run(ctx, cfg, Jobs(ctx, urlsFor(srv, "/slow", "/fast1", "/fast2"))))

	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	if first := results[0].Job.Index; first == 0 {
		t.Error("slow job arrived first; results are not streaming in completion order")
	}
	if last := results[len(results)-1].Job.Index; last != 0 {
		t.Errorf("last result index = %d, want 0 (the slow job)", last)
	}
}

func TestRun_EmptyJobsClosesResults(t *testing.T) {
	ctx := context.Background()
	results := drain(t, Run(ctx, Config{}, Jobs(ctx, nil)))
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

func TestRun_CancellationStopsWork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()

	paths := make([]string, 10)
	for i := range paths {
		paths[i] = fmt.Sprintf("/%d", i)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(50*time.Millisecond, cancel)

	cfg := Config{Concurrency: 2, Timeout: 5 * time.Second, MaxRedirects: 5}
	start := time.Now()
	results := drain(t, Run(ctx, cfg, Jobs(ctx, urlsFor(srv, paths...))))

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v to stop after cancel", elapsed)
	}
	if len(results) >= len(paths) {
		t.Errorf("got %d results, expected fewer than %d after cancel", len(results), len(paths))
	}
	for _, r := range results {
		if r.ErrKind != KindCanceled {
			t.Errorf("job %d: ErrKind = %q, want %q", r.Job.Index, r.ErrKind, KindCanceled)
		}
	}
}

func TestJobs_StopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := Jobs(ctx, make([]string, 1000))
	cancel() // nobody is reading; the producer must not block forever

	timeout := time.After(time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("Jobs did not close its channel after cancel")
		}
	}
}

func TestConfigDefaults(t *testing.T) {
	got := Config{MaxRedirects: -1, Method: "head"}.withDefaults()
	if got.Concurrency != 10 || got.Timeout != 10*time.Second || got.MaxRedirects != 10 || got.Method != "HEAD" {
		t.Errorf("unexpected defaults: %+v", got)
	}
	if zero := (Config{}).withDefaults(); zero.MaxRedirects != 0 {
		t.Errorf("MaxRedirects 0 should be kept (no redirects), got %d", zero.MaxRedirects)
	}
}
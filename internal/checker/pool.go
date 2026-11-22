package checker

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config controls how Run checks URLs.
type Config struct {
	Concurrency  int           // number of worker goroutines (default 10)
	Timeout      time.Duration // per-request timeout (default 10s)
	MaxRedirects int           // redirects to follow; 0 means none (default 10 if negative)
	Method       string        // HTTP method (default GET)
}

func (c Config) withDefaults() Config {
	if c.Concurrency <= 0 {
		c.Concurrency = 10
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	if c.MaxRedirects < 0 {
		c.MaxRedirects = 10
	}
	if c.Method == "" {
		c.Method = http.MethodGet
	}
	c.Method = strings.ToUpper(c.Method)
	return c
}

// Run starts cfg.Concurrency workers that read jobs and send results on the
// returned channel, in completion order (not input order).
//
// The results channel is closed once all workers have finished, which happens
// when jobs is closed and drained, or when ctx is canceled.
//
// Contract: the caller MUST keep receiving from the results channel until it
// is closed. Workers always deliver the result of a request they have already
// made, so a consumer that stops reading will leak goroutines.
//
// On cancellation, workers stop taking new jobs, and requests already in
// flight finish quickly with ErrKind == KindCanceled.
func Run(ctx context.Context, cfg Config, jobs <-chan Job) <-chan Result {
	cfg = cfg.withDefaults()
	client := NewClient(cfg.Timeout, cfg.MaxRedirects, cfg.Concurrency)
	results := make(chan Result)

	var wg sync.WaitGroup
	wg.Add(cfg.Concurrency)
	for i := 0; i < cfg.Concurrency; i++ {
		go func() {
			defer wg.Done()
			worker(ctx, client, cfg.Method, jobs, results)
		}()
	}

	// Only this goroutine closes results, and only after every sender is done.
	go func() {
		wg.Wait()
		close(results)
		client.CloseIdleConnections()
	}()

	return results
}

func worker(ctx context.Context, client *http.Client, method string, jobs <-chan Job, results chan<- Result) {
	for {
		// select picks randomly when several cases are ready, so check
		// cancellation explicitly to stop promptly.
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case job, ok := <-jobs:
			if !ok {
				return
			}
			results <- Check(ctx, client, method, job)
		}
	}
}

// Jobs turns a slice of URLs into a jobs channel, numbering them by position.
// It stops early if ctx is canceled, and always closes the channel.
func Jobs(ctx context.Context, urls []string) <-chan Job {
	ch := make(chan Job)
	go func() {
		defer close(ch)
		for i, u := range urls {
			select {
			case ch <- Job{Index: i, URL: u}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}
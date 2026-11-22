package checker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/boom", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ok", http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCheck_Statuses(t *testing.T) {
	srv := newTestServer(t)
	client := NewClient(time.Second, 5, 4)

	tests := []struct {
		name         string
		path         string
		wantStatus   int
		wantHealthy  bool
		wantFinalEnd string
	}{
		{"ok", "/ok", 200, true, "/ok"},
		{"not found", "/missing", 404, false, "/missing"},
		{"server error", "/boom", 500, false, "/boom"},
		{"redirect followed", "/start", 200, true, "/ok"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Check(context.Background(), client, http.MethodGet, Job{URL: srv.URL + tc.path})

			if got.Err != nil {
				t.Fatalf("unexpected error: %v", got.Err)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("status = %d, want %d", got.Status, tc.wantStatus)
			}
			if got.Healthy() != tc.wantHealthy {
				t.Errorf("Healthy() = %v, want %v", got.Healthy(), tc.wantHealthy)
			}
			if !strings.HasSuffix(got.FinalURL, tc.wantFinalEnd) {
				t.Errorf("FinalURL = %q, want suffix %q", got.FinalURL, tc.wantFinalEnd)
			}
			if got.Latency <= 0 {
				t.Errorf("Latency = %v, want > 0", got.Latency)
			}
		})
	}
}

func TestCheck_Errors(t *testing.T) {
	srv := newTestServer(t)

	// A server that is started and immediately closed gives a free, dead port.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	tests := []struct {
		name     string
		url      string
		timeout  time.Duration
		redirect int
		want     ErrKind
	}{
		{"timeout", srv.URL + "/slow", 50 * time.Millisecond, 5, KindTimeout},
		{"redirect loop", srv.URL + "/loop", time.Second, 3, KindRedirects},
		{"connection refused", deadURL, time.Second, 5, KindRefused},
		{"dns failure", "http://does-not-exist.invalid", time.Second, 5, KindDNS},
		{"invalid url", "http://bad host/", time.Second, 5, KindInvalid},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient(tc.timeout, tc.redirect, 4)
			got := Check(context.Background(), client, http.MethodGet, Job{URL: tc.url})

			if got.Err == nil {
				t.Fatalf("expected error, got status %d", got.Status)
			}
			if got.ErrKind != tc.want {
				t.Errorf("ErrKind = %q, want %q (err: %v)", got.ErrKind, tc.want, got.Err)
			}
			if got.Healthy() {
				t.Error("Healthy() = true for a failed request")
			}
		})
	}
}

func TestCheck_ContextCanceled(t *testing.T) {
	srv := newTestServer(t)
	client := NewClient(2*time.Second, 5, 4)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	got := Check(ctx, client, http.MethodGet, Job{URL: srv.URL + "/slow"})

	if got.ErrKind != KindCanceled {
		t.Errorf("ErrKind = %q, want %q (err: %v)", got.ErrKind, KindCanceled, got.Err)
	}
}
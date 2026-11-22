package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("/missing", http.NotFound)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// exec runs the CLI in-process and returns its exit code and output.
func exec(ctx context.Context, stdin io.Reader, args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(ctx, args, stdin, &out, &errOut, false)
	return code, out.String(), errOut.String()
}

func TestRun_ExitCodes(t *testing.T) {
	srv := newServer(t)
	bg := context.Background()

	t.Run("all healthy", func(t *testing.T) {
		code, out, errOut := exec(bg, nil, srv.URL+"/ok", srv.URL+"/ok?x=1")
		if code != exitOK {
			t.Errorf("code = %d, want %d\nstderr: %s", code, exitOK, errOut)
		}
		if strings.Count(out, "200") != 2 {
			t.Errorf("stdout should have two 200 lines:\n%s", out)
		}
		if !strings.Contains(errOut, "Checked 2 URLs") {
			t.Errorf("summary missing from stderr:\n%s", errOut)
		}
	})

	t.Run("one failure", func(t *testing.T) {
		code, out, _ := exec(bg, nil, srv.URL+"/ok", srv.URL+"/missing")
		if code != exitFailures {
			t.Errorf("code = %d, want %d", code, exitFailures)
		}
		if !strings.Contains(out, "404") {
			t.Errorf("stdout should show the 404:\n%s", out)
		}
	})

	t.Run("invalid line alongside valid ones", func(t *testing.T) {
		code, out, errOut := exec(bg, nil, srv.URL+"/ok", "not-a-url")
		if code != exitFailures {
			t.Errorf("code = %d, want %d", code, exitFailures)
		}
		if !strings.Contains(out, "200") {
			t.Errorf("valid URL should still be checked:\n%s", out)
		}
		if !strings.Contains(errOut, "warning: skipping") {
			t.Errorf("expected a warning on stderr:\n%s", errOut)
		}
	})

	t.Run("only invalid input", func(t *testing.T) {
		if code, _, _ := exec(bg, nil, "not-a-url"); code != exitUsage {
			t.Errorf("code = %d, want %d", code, exitUsage)
		}
	})

	t.Run("no input", func(t *testing.T) {
		code, _, errOut := exec(bg, nil)
		if code != exitUsage {
			t.Errorf("code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(errOut, "no URLs provided") || !strings.Contains(errOut, "Usage:") {
			t.Errorf("expected message and usage:\n%s", errOut)
		}
	})

	t.Run("interrupted", func(t *testing.T) {
		ctx, cancel := context.WithCancel(bg)
		cancel() // already canceled, as if the user pressed Ctrl+C
		code, _, _ := exec(ctx, nil, srv.URL+"/ok", srv.URL+"/ok?x=1")
		if code != exitInterrupted {
			t.Errorf("code = %d, want %d", code, exitInterrupted)
		}
	})
}

func TestRun_InputSources(t *testing.T) {
	srv := newServer(t)
	bg := context.Background()

	t.Run("file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "urls.txt")
		content := "# links\n" + srv.URL + "/ok\n\n" + srv.URL + "/ok?x=1\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := exec(bg, nil, "-f", path)
		if code != exitOK || strings.Count(out, "200") != 2 {
			t.Errorf("code=%d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		code, _, _ := exec(bg, nil, "-f", filepath.Join(t.TempDir(), "nope.txt"))
		if code != exitUsage {
			t.Errorf("code = %d, want %d", code, exitUsage)
		}
	})

	t.Run("piped stdin", func(t *testing.T) {
		code, out, _ := exec(bg, strings.NewReader(srv.URL+"/ok\n"))
		if code != exitOK || !strings.Contains(out, "200") {
			t.Errorf("code=%d stdout:\n%s", code, out)
		}
	})

	t.Run("dash reads stdin", func(t *testing.T) {
		code, out, _ := exec(bg, strings.NewReader(srv.URL+"/ok\n"), "-f", "-")
		if code != exitOK || !strings.Contains(out, "200") {
			t.Errorf("code=%d stdout:\n%s", code, out)
		}
	})
}

func TestRun_FlagValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"help", []string{"-h"}, exitOK},
		{"unknown flag", []string{"-nope"}, exitUsage},
		{"zero workers", []string{"-c", "0", "https://a.test"}, exitUsage},
		{"zero timeout", []string{"-t", "0s", "https://a.test"}, exitUsage},
		{"negative redirects", []string{"-r", "-1", "https://a.test"}, exitUsage},
		{"bad method", []string{"-m", "POST", "https://a.test"}, exitUsage},
		{"bad duration", []string{"-t", "fast", "https://a.test"}, exitUsage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if code, _, _ := exec(context.Background(), nil, tc.args...); code != tc.want {
				t.Errorf("code = %d, want %d", code, tc.want)
			}
		})
	}
}

func TestRun_HeadMethod(t *testing.T) {
	var seen atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Method)
	}))
	defer srv.Close()

	code, _, errOut := exec(context.Background(), nil, "-m", "head", srv.URL)
	if code != exitOK {
		t.Fatalf("code = %d\nstderr: %s", code, errOut)
	}
	if got := seen.Load(); got != "HEAD" {
		t.Errorf("server saw method %v, want HEAD", got)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// If stdout goes away (e.g. `urlcheck ... | head -1`), we must stop cleanly
// and not hang, leak workers, or panic.
func TestRun_WriteErrorStopsCleanly(t *testing.T) {
	srv := newServer(t)
	var errOut bytes.Buffer

	args := []string{"-c", "2"}
	for i := 0; i < 20; i++ {
		args = append(args, srv.URL+"/ok?i="+string(rune('a'+i)))
	}

	code := run(context.Background(), args, nil, failingWriter{}, &errOut, false)
	if code != exitFailures {
		t.Errorf("code = %d, want %d", code, exitFailures)
	}
	if !strings.Contains(errOut.String(), "write error") {
		t.Errorf("expected write error on stderr:\n%s", errOut.String())
	}
}
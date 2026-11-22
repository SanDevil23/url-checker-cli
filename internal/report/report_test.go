package report

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"urlcheck/internal/checker"
)

func result(url string, status int, latency time.Duration) checker.Result {
	return checker.Result{
		Job:      checker.Job{URL: url},
		Status:   status,
		FinalURL: url,
		Latency:  latency,
	}
}

func failure(url string, kind checker.ErrKind, latency time.Duration) checker.Result {
	return checker.Result{
		Job:     checker.Job{URL: url},
		Latency: latency,
		Err:     errors.New("boom"),
		ErrKind: kind,
	}
}

func TestPrinter_Lines(t *testing.T) {
	redirected := result("http://go.dev", 200, 210*time.Millisecond)
	redirected.FinalURL = "https://go.dev/"

	tests := []struct {
		name string
		in   checker.Result
		want string
	}{
		{
			"ok",
			result("https://go.dev", 200, 142*time.Millisecond),
			"200  " + "  142ms" + "  https://go.dev\n",
		},
		{
			"not found",
			result("https://example.com/missing", 404, 88*time.Millisecond),
			"404  " + "   88ms" + "  https://example.com/missing\n",
		},
		{
			"redirect shows final url",
			redirected,
			"200  " + "  210ms" + "  http://go.dev  -> https://go.dev/\n",
		},
		{
			"timeout",
			failure("https://slow.test", checker.KindTimeout, 5*time.Second),
			"ERR  " + "   5.0s" + "  https://slow.test  (timeout)\n",
		},
		{
			"unclassified error shows message",
			failure("https://odd.test", checker.KindOther, 10*time.Millisecond),
			"ERR  " + "   10ms" + "  https://odd.test  (boom)\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := NewPrinter(&buf, false).Print(tc.in); err != nil {
				t.Fatal(err)
			}
			if got := buf.String(); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestPrinter_Colors(t *testing.T) {
	tests := []struct {
		name      string
		in        checker.Result
		wantColor string
		wantLabel string
	}{
		{"2xx green", result("https://a.test", 200, time.Millisecond), ansiGreen, "200"},
		{"3xx yellow", result("https://a.test", 301, time.Millisecond), ansiYellow, "301"},
		{"4xx red", result("https://a.test", 404, time.Millisecond), ansiRed, "404"},
		{"5xx red", result("https://a.test", 503, time.Millisecond), ansiRed, "503"},
		{"error red", failure("https://a.test", checker.KindDNS, time.Millisecond), ansiRed, "ERR"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			_ = NewPrinter(&buf, true).Print(tc.in)
			want := tc.wantColor + tc.wantLabel + ansiReset
			if !strings.HasPrefix(buf.String(), want) {
				t.Errorf("line %q does not start with %q", buf.String(), want)
			}
		})
	}

	t.Run("disabled emits no escape codes", func(t *testing.T) {
		var buf bytes.Buffer
		_ = NewPrinter(&buf, false).Print(result("https://a.test", 404, time.Millisecond))
		if strings.Contains(buf.String(), "\x1b") {
			t.Errorf("unexpected ANSI codes in %q", buf.String())
		}
	})
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "<1ms"},
		{500 * time.Microsecond, "<1ms"},
		{842 * time.Millisecond, "842ms"},
		{999 * time.Millisecond, "999ms"},
		{time.Second, "1.0s"},
		{1250 * time.Millisecond, "1.2s"},
		{59 * time.Second, "59.0s"},
		{125 * time.Second, "2m5s"},
	}
	for _, tc := range tests {
		if got := FormatDuration(tc.in); got != tc.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStats(t *testing.T) {
	var s Stats
	s.Add(result("https://fast.test", 200, 100*time.Millisecond))
	s.Add(result("https://slow.test", 200, 300*time.Millisecond))
	s.Add(result("https://missing.test", 404, 50*time.Millisecond))
	s.Add(failure("https://hang.test", checker.KindTimeout, 2*time.Second))
	s.Add(failure("https://interrupted.test", checker.KindCanceled, time.Second))

	if s.Total != 5 || s.OK != 2 || s.Failed != 2 || s.Canceled != 1 {
		t.Errorf("counts = total %d ok %d failed %d canceled %d", s.Total, s.OK, s.Failed, s.Canceled)
	}
	if !s.HasFailures() {
		t.Error("HasFailures() = false, want true")
	}
	// Average covers only requests that got a response: (100+300+50)/3.
	if got := s.AvgLatency(); got != 150*time.Millisecond {
		t.Errorf("AvgLatency = %v, want 150ms", got)
	}
	if s.Slowest.Job.URL != "https://slow.test" {
		t.Errorf("Slowest = %q", s.Slowest.Job.URL)
	}

	var buf bytes.Buffer
	if err := s.Write(&buf, 2500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	want := "\nChecked 5 URLs in 2.5s: 2 ok, 2 failed, 1 canceled\n" +
		"Latency: avg 150ms, slowest 300ms (https://slow.test)\n" +
		"Failures: 1 http 4xx, 1 timeout\n"
	if got := buf.String(); got != want {
		t.Errorf("summary mismatch\ngot:\n%q\nwant:\n%q", got, want)
	}
}

func TestStats_CanceledAloneIsNotAFailure(t *testing.T) {
	var s Stats
	s.Add(failure("https://a.test", checker.KindCanceled, time.Millisecond))
	if s.HasFailures() {
		t.Error("canceled requests must not trigger a failing exit code")
	}
}

func TestStats_SingleURLAndNoLatency(t *testing.T) {
	var s Stats
	s.Add(failure("https://a.test", checker.KindDNS, time.Millisecond))

	var buf bytes.Buffer
	_ = s.Write(&buf, 10*time.Millisecond)
	want := "\nChecked 1 URL in 10ms: 0 ok, 1 failed\nFailures: 1 dns\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStats_Empty(t *testing.T) {
	var s Stats
	if s.AvgLatency() != 0 || s.HasFailures() {
		t.Error("zero Stats should be all zeros")
	}
}

func TestColorEnabled_NoColorWins(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if ColorEnabled(os.Stdout) {
		t.Error("NO_COLOR should disable color")
	}
}
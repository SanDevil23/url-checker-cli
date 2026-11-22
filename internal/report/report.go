package report

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"urlcheck/internal/checker"
)

const (
	ansiReset  = "\x1b[0m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
)

// ColorEnabled reports whether it is sensible to emit ANSI colors to f:
// f must be a terminal, NO_COLOR must be unset (https://no-color.org), and
// TERM must not be "dumb".
func ColorEnabled(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// Printer writes one line per result. It is not safe for concurrent use,
// which is fine: only the consumer goroutine in main prints.
type Printer struct {
	w     io.Writer
	color bool
}

func NewPrinter(w io.Writer, color bool) *Printer {
	return &Printer{w: w, color: color}
}

// Print writes a line such as:
//
//	200    142ms  https://go.dev
//	301    210ms  http://go.dev  -> https://go.dev/
//	ERR     5.0s  https://slow.example.com  (timeout)
func (p *Printer) Print(r checker.Result) error {
	label, color := "ERR", ansiRed
	if r.Err == nil && r.Status > 0 {
		label = fmt.Sprintf("%d", r.Status)
		color = statusColor(r.Status)
	}

	line := fmt.Sprintf("%s  %7s  %s", p.paint(label, color), FormatDuration(r.Latency), r.Job.URL)
	switch {
	case r.Err != nil:
		line += fmt.Sprintf("  (%s)", Reason(r))
	case r.FinalURL != "" && r.FinalURL != r.Job.URL:
		line += "  -> " + r.FinalURL
	}

	_, err := fmt.Fprintln(p.w, line)
	return err
}

func (p *Printer) paint(s, color string) string {
	if !p.color {
		return s
	}
	return color + s + ansiReset
}

func statusColor(status int) string {
	switch {
	case status < 300:
		return ansiGreen
	case status < 400:
		return ansiYellow
	default:
		return ansiRed
	}
}

// Reason returns a short human-readable explanation for a failed result.
func Reason(r checker.Result) string {
	switch r.ErrKind {
	case checker.KindTimeout:
		return "timeout"
	case checker.KindDNS:
		return "dns lookup failed"
	case checker.KindTLS:
		return "tls error"
	case checker.KindRefused:
		return "connection refused"
	case checker.KindRedirects:
		return "too many redirects"
	case checker.KindCanceled:
		return "canceled"
	case checker.KindInvalid:
		return "invalid url"
	}
	if r.Err != nil {
		return r.Err.Error()
	}
	return "unknown error"
}

// FormatDuration renders latencies compactly: 842ms, 1.2s, 2m5s.
func FormatDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}

// Stats accumulates results for the end-of-run summary.
//
// Definitions:
//   - OK:       Result.Healthy() (no error, status < 400)
//   - Canceled: interrupted by Ctrl+C; not counted as a failure
//   - Failed:   everything else (transport errors and 4xx/5xx)
//
// Latency figures only cover requests that got an HTTP response, so a pile of
// timeouts doesn't distort the average.
type Stats struct {
	Total, OK, Failed, Canceled int
	Responded                   int
	Slowest                     checker.Result
	Failures                    map[string]int // breakdown, e.g. "timeout", "http 4xx"

	totalLatency time.Duration
}

func (s *Stats) Add(r checker.Result) {
	s.Total++

	if r.Status > 0 {
		s.Responded++
		s.totalLatency += r.Latency
		if r.Latency > s.Slowest.Latency {
			s.Slowest = r
		}
	}

	switch {
	case r.Healthy():
		s.OK++
	case r.ErrKind == checker.KindCanceled:
		s.Canceled++
	default:
		s.Failed++
		if s.Failures == nil {
			s.Failures = make(map[string]int)
		}
		s.Failures[failureKey(r)]++
	}
}

func failureKey(r checker.Result) string {
	if r.Err != nil {
		if r.ErrKind == checker.KindOther || r.ErrKind == checker.KindNone {
			return "other"
		}
		return string(r.ErrKind)
	}
	return fmt.Sprintf("http %dxx", r.Status/100)
}

func (s Stats) AvgLatency() time.Duration {
	if s.Responded == 0 {
		return 0
	}
	return s.totalLatency / time.Duration(s.Responded)
}

// HasFailures drives the process exit code: true means exit 1.
func (s Stats) HasFailures() bool { return s.Failed > 0 }

// Write prints the summary, for example:
//
//	Checked 50 URLs in 2.3s: 47 ok, 3 failed
//	Latency: avg 142ms, slowest 1.2s (https://example.com/slow)
//	Failures: 2 timeout, 1 http 4xx
func (s Stats) Write(w io.Writer, elapsed time.Duration) error {
	noun := "URLs"
	if s.Total == 1 {
		noun = "URL"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\nChecked %d %s in %s: %d ok, %d failed",
		s.Total, noun, FormatDuration(elapsed), s.OK, s.Failed)
	if s.Canceled > 0 {
		fmt.Fprintf(&b, ", %d canceled", s.Canceled)
	}
	b.WriteByte('\n')

	if s.Responded > 0 {
		fmt.Fprintf(&b, "Latency: avg %s, slowest %s (%s)\n",
			FormatDuration(s.AvgLatency()), FormatDuration(s.Slowest.Latency), s.Slowest.Job.URL)
	}

	if len(s.Failures) > 0 {
		keys := make([]string, 0, len(s.Failures))
		for k := range s.Failures {
			keys = append(keys, k)
		}
		sort.Strings(keys) // map iteration order is random; keep output stable

		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = fmt.Sprintf("%d %s", s.Failures[k], k)
		}
		fmt.Fprintf(&b, "Failures: %s\n", strings.Join(parts, ", "))
	}

	_, err := io.WriteString(w, b.String())
	return err
}
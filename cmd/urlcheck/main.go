package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"urlcheck/internal/checker"
	"urlcheck/internal/input"
	"urlcheck/internal/report"
)

// Exit codes. 0/1/2 are what CI cares about; 130 is the shell convention for
// "terminated by Ctrl+C" (128 + SIGINT).
const (
	exitOK          = 0
	exitFailures    = 1
	exitUsage       = 2
	exitInterrupted = 130
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// After the first Ctrl+C, restore default signal behavior so a second
	// Ctrl+C kills the process immediately if shutdown ever hangs.
	go func() {
		<-ctx.Done()
		stop()
	}()

	code := run(ctx, os.Args[1:], stdinIfPiped(), os.Stdout, os.Stderr, report.ColorEnabled(os.Stdout))
	stop()
	os.Exit(code)
}

// stdinIfPiped returns os.Stdin only when something is piped or redirected
// into the program. For an interactive terminal it returns nil, so that
// running `urlcheck` with no arguments prints usage instead of hanging.
func stdinIfPiped() io.Reader {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice != 0 {
		return nil
	}
	return os.Stdin
}

// run is main without the process-level side effects (os.Exit, os.Stdout,
// signals), which makes it testable. It returns the exit code.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, color bool) int {
	fs := flag.NewFlagSet("urlcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		file        = fs.String("f", "", "read URLs from `file` (\"-\" for stdin)")
		concurrency = fs.Int("c", 10, "number of concurrent `workers`")
		timeout     = fs.Duration("t", 10*time.Second, "per-request `timeout`")
		redirects   = fs.Int("r", 10, "maximum `redirects` to follow")
		method      = fs.String("m", "GET", "HTTP `method`: GET or HEAD")
	)
	fs.Usage = func() { usage(fs) }

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage // the flag package already printed the problem
	}

	// Validate flags.
	*method = strings.ToUpper(*method)
	switch {
	case *concurrency < 1:
		return usageError(stderr, "-c must be at least 1")
	case *timeout <= 0:
		return usageError(stderr, "-t must be positive")
	case *redirects < 0:
		return usageError(stderr, "-r must not be negative")
	case *method != "GET" && *method != "HEAD":
		return usageError(stderr, "-m must be GET or HEAD")
	}

	// Collect input.
	loaded, err := input.Load(input.Options{Args: fs.Args(), File: *file, Stdin: stdin})
	switch {
	case errors.Is(err, input.ErrNoInput):
		fmt.Fprintln(stderr, "urlcheck: no URLs provided")
		usage(fs)
		return exitUsage
	case err != nil:
		fmt.Fprintf(stderr, "urlcheck: %v\n", err)
		return exitUsage
	}
	for _, inv := range loaded.Invalid {
		fmt.Fprintf(stderr, "warning: skipping %s\n", inv)
	}
	if len(loaded.Entries) == 0 {
		fmt.Fprintln(stderr, "urlcheck: no valid URLs to check")
		return exitUsage
	}

	urls := make([]string, len(loaded.Entries))
	for i, e := range loaded.Entries {
		urls[i] = e.URL
	}

	// Run. runCtx lets us stop the pool ourselves (e.g. stdout closed) without
	// confusing that with a user interrupt, which is tracked on ctx.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	cfg := checker.Config{
		Concurrency:  *concurrency,
		Timeout:      *timeout,
		MaxRedirects: *redirects,
		Method:       *method,
	}
	printer := report.NewPrinter(stdout, color)
	var stats report.Stats
	var writeErr error
	start := time.Now()

	// We must drain the channel until it closes (see checker.Run's contract),
	// even after a write error, so keep looping but stop printing.
	for r := range checker.Run(runCtx, cfg, checker.Jobs(runCtx, urls)) {
		stats.Add(r)
		if writeErr != nil {
			continue
		}
		if err := printer.Print(r); err != nil {
			writeErr = err
			cancel()
		}
	}

	_ = stats.Write(stderr, time.Since(start))
	if n := len(loaded.Invalid); n > 0 {
		fmt.Fprintf(stderr, "Skipped %d invalid input line(s)\n", n)
	}

	switch {
	case ctx.Err() != nil:
		return exitInterrupted
	case writeErr != nil:
		fmt.Fprintf(stderr, "urlcheck: write error: %v\n", writeErr)
		return exitFailures
	case stats.HasFailures() || len(loaded.Invalid) > 0:
		return exitFailures
	default:
		return exitOK
	}
}

func usage(fs *flag.FlagSet) {
	out := fs.Output()
	fmt.Fprint(out, `Usage: urlcheck [flags] [url ...]

Check many URLs concurrently. URLs come from arguments, from a file (-f), or
from piped stdin, one per line; blank lines and lines starting with # are ignored.

Flags:
`)
	fs.PrintDefaults()
	fmt.Fprint(out, `
Exit codes:
  0    all URLs healthy (no transport error, status < 400)
  1    at least one URL failed, or input contained invalid lines
  2    usage error
  130  interrupted

Examples:
  urlcheck https://go.dev https://example.com
  urlcheck -f urls.txt -c 20 -t 5s
  cat urls.txt | urlcheck -m HEAD
`)
}

func usageError(w io.Writer, msg string) int {
	fmt.Fprintf(w, "urlcheck: %s\nTry 'urlcheck -h' for usage.\n", msg)
	return exitUsage
}
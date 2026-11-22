package input

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

// ErrNoInput is returned when no URLs were supplied by any source.
var ErrNoInput = errors.New("no URLs provided")

// Entry is a validated URL along with where it came from.
type Entry struct {
	URL    string
	Source string // file name, "stdin", or "arg"
	Line   int    // 1-based line number; 0 for args
}

// Invalid describes an input line that could not be used.
type Invalid struct {
	Source string
	Line   int
	Raw    string
	Err    error
}

func (i Invalid) String() string {
	if i.Line > 0 {
		return fmt.Sprintf("%s:%d: %q: %v", i.Source, i.Line, i.Raw, i.Err)
	}
	return fmt.Sprintf("%s: %q: %v", i.Source, i.Raw, i.Err)
}

// Result is everything collected from the inputs. Invalid entries are
// reported, not fatal: the caller decides what to do with them.
type Result struct {
	Entries []Entry
	Invalid []Invalid
}

// Options describes where to read URLs from.
type Options struct {
	Args  []string  // positional URLs
	File  string    // path to a file; "-" means Stdin
	Stdin io.Reader // nil when stdin is a terminal (nothing was piped in)
}

// Load gathers URLs from args, a file, and/or stdin.
//
// Stdin is only read when File is "-", or when there are no args and no file.
// Pass a nil Stdin when it is an interactive terminal, so the program does
// not hang waiting for input.
func Load(opts Options) (Result, error) {
	var res Result

	for _, a := range opts.Args {
		res.add("arg", 0, a)
	}

	switch {
	case opts.File == "-":
		if opts.Stdin == nil {
			return res, errors.New("-f - given but stdin is not available")
		}
		if err := res.parse(opts.Stdin, "stdin"); err != nil {
			return res, err
		}
	case opts.File != "":
		f, err := os.Open(opts.File)
		if err != nil {
			return res, err
		}
		defer f.Close()
		if err := res.parse(f, opts.File); err != nil {
			return res, err
		}
	case len(opts.Args) == 0 && opts.Stdin != nil:
		if err := res.parse(opts.Stdin, "stdin"); err != nil {
			return res, err
		}
	}

	if len(res.Entries) == 0 && len(res.Invalid) == 0 {
		return res, ErrNoInput
	}
	return res, nil
}

// Parse reads URLs from r, one per line. Blank lines and lines starting with
// '#' are ignored. Only whole-line comments are supported, because '#' is
// valid inside a URL (fragments).
func Parse(r io.Reader, source string) (Result, error) {
	var res Result
	err := res.parse(r, source)
	return res, err
}

func (res *Result) parse(r io.Reader, source string) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024) // tolerate very long lines

	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if lineNo == 1 {
			line = strings.TrimPrefix(line, "\ufeff") // UTF-8 BOM
		}
		line = strings.TrimSpace(line) // also handles CRLF
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		res.add(source, lineNo, line)
	}
	return sc.Err()
}

func (res *Result) add(source string, line int, raw string) {
	raw = strings.TrimSpace(raw)
	if err := Validate(raw); err != nil {
		res.Invalid = append(res.Invalid, Invalid{Source: source, Line: line, Raw: raw, Err: err})
		return
	}
	res.Entries = append(res.Entries, Entry{URL: raw, Source: source, Line: line})
}

// Validate reports whether raw is an absolute http(s) URL with a host.
func Validate(raw string) error {
	if raw == "" {
		return errors.New("empty URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	case "":
		return errors.New("missing scheme (expected http:// or https://)")
	default:
		return fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return errors.New("missing host")
	}
	return nil
}
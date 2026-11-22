package input

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"http", "http://example.com", false},
		{"https with path and query", "https://example.com/a/b?x=1", false},
		{"fragment", "https://example.com/page#section", false},
		{"port", "http://localhost:8080/health", false},
		{"uppercase scheme", "HTTP://Example.com", false},
		{"no scheme", "example.com", true},
		{"ftp scheme", "ftp://example.com/file", true},
		{"no host", "http://", true},
		{"port only", "http://:8080", true},
		{"space in host", "http://exa mple.com", true},
		{"empty", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.in)
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
		})
	}
}

func TestParse(t *testing.T) {
	data := "\ufeff# my links\r\n" +
		"https://go.dev\r\n" +
		"\r\n" +
		"   https://example.com/a#frag   \n" +
		"not-a-url\n" +
		"ftp://example.com\n" +
		"# trailing comment\n" +
		"http://localhost:8080"

	res, err := Parse(strings.NewReader(data), "urls.txt")
	if err != nil {
		t.Fatal(err)
	}

	wantEntries := []Entry{
		{URL: "https://go.dev", Source: "urls.txt", Line: 2},
		{URL: "https://example.com/a#frag", Source: "urls.txt", Line: 4},
		{URL: "http://localhost:8080", Source: "urls.txt", Line: 8},
	}
	if !reflect.DeepEqual(res.Entries, wantEntries) {
		t.Errorf("entries =\n%+v\nwant\n%+v", res.Entries, wantEntries)
	}

	if len(res.Invalid) != 2 {
		t.Fatalf("got %d invalid, want 2: %v", len(res.Invalid), res.Invalid)
	}
	if res.Invalid[0].Line != 5 || res.Invalid[0].Raw != "not-a-url" {
		t.Errorf("first invalid = %+v", res.Invalid[0])
	}
	if res.Invalid[1].Line != 6 || res.Invalid[1].Raw != "ftp://example.com" {
		t.Errorf("second invalid = %+v", res.Invalid[1])
	}
}

func TestParse_LongLine(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", 200*1024)
	res, err := Parse(strings.NewReader(long), "big.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 {
		t.Errorf("got %d entries, want 1", len(res.Entries))
	}
}

func TestInvalidString(t *testing.T) {
	withLine := Invalid{Source: "urls.txt", Line: 3, Raw: "foo", Err: errors.New("bad")}
	if got, want := withLine.String(), `urls.txt:3: "foo": bad`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	arg := Invalid{Source: "arg", Raw: "foo", Err: errors.New("bad")}
	if got, want := arg.String(), `arg: "foo": bad`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "urls.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	file := writeTemp(t, "https://from-file.test\n# skip\nbogus\n")

	t.Run("args only", func(t *testing.T) {
		res, err := Load(Options{Args: []string{"https://a.test", "https://b.test"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Entries) != 2 || res.Entries[0].Source != "arg" {
			t.Errorf("entries = %+v", res.Entries)
		}
	})

	t.Run("file", func(t *testing.T) {
		res, err := Load(Options{File: file})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Entries) != 1 || len(res.Invalid) != 1 {
			t.Errorf("entries=%d invalid=%d", len(res.Entries), len(res.Invalid))
		}
	})

	t.Run("args and file combine", func(t *testing.T) {
		res, err := Load(Options{Args: []string{"https://a.test"}, File: file})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Entries) != 2 {
			t.Errorf("got %d entries, want 2", len(res.Entries))
		}
	})

	t.Run("stdin fallback", func(t *testing.T) {
		res, err := Load(Options{Stdin: strings.NewReader("https://piped.test\n")})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Entries) != 1 || res.Entries[0].Source != "stdin" {
			t.Errorf("entries = %+v", res.Entries)
		}
	})

	t.Run("stdin ignored when args given", func(t *testing.T) {
		res, err := Load(Options{
			Args:  []string{"https://a.test"},
			Stdin: strings.NewReader("https://piped.test\n"),
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Entries) != 1 {
			t.Errorf("got %d entries, want 1", len(res.Entries))
		}
	})

	t.Run("dash means stdin", func(t *testing.T) {
		res, err := Load(Options{File: "-", Stdin: strings.NewReader("https://piped.test\n")})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Entries) != 1 {
			t.Errorf("got %d entries, want 1", len(res.Entries))
		}
	})

	t.Run("dash without stdin", func(t *testing.T) {
		if _, err := Load(Options{File: "-"}); err == nil {
			t.Error("expected error")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if _, err := Load(Options{File: filepath.Join(t.TempDir(), "nope.txt")}); err == nil {
			t.Error("expected error")
		}
	})

	t.Run("no input", func(t *testing.T) {
		if _, err := Load(Options{}); !errors.Is(err, ErrNoInput) {
			t.Errorf("err = %v, want ErrNoInput", err)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		empty := writeTemp(t, "# only a comment\n\n")
		if _, err := Load(Options{File: empty}); !errors.Is(err, ErrNoInput) {
			t.Errorf("err = %v, want ErrNoInput", err)
		}
	})
}
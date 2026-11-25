# urlcheck

A small CLI that checks many URLs concurrently and reports their status, latency,
and failure reason. URLs can come from command-line arguments, a file, or piped
stdin.

```
$ urlcheck https://go.dev https://example.com https://nonexistent.invalid
200    142ms  https://go.dev
200     98ms  https://example.com
ERR     12ms  https://nonexistent.invalid  (dns lookup failed)

Checked 3 URLs in 142ms: 2 ok, 1 failed
Latency: avg 120ms, slowest 142ms (https://go.dev)
Failures: 1 dns
```

## Install

Requires Go 1.26.3+.

```
go install urlcheck/cmd/urlcheck
```

Or build from a checkout of this repo:

```
go build -o urlcheck ./cmd/urlcheck
```

## Usage

```
urlcheck [flags] [url ...]
```

URLs are gathered from positional arguments, from a file (`-f`), or from piped
stdin — whichever is given. When reading from a file or stdin, input is one URL
per line; blank lines and lines starting with `#` are ignored.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-f` | | Read URLs from `file` (`-` for stdin) |
| `-c` | `10` | Number of concurrent workers |
| `-t` | `10s` | Per-request timeout |
| `-r` | `10` | Maximum redirects to follow |
| `-m` | `GET` | HTTP method: `GET` or `HEAD` |

### Examples

```
urlcheck https://go.dev https://example.com
urlcheck -f urls.txt -c 20 -t 5s
cat urls.txt | urlcheck -m HEAD
```

### Exit codes

| Code | Meaning |
|------|---------|
| `0`   | All URLs healthy (no transport error, status < 400) |
| `1`   | At least one URL failed, or input contained invalid lines |
| `2`   | Usage error |
| `130` | Interrupted (Ctrl+C) |

This makes `urlcheck` suitable as a CI health-check step: a nonzero exit means
something is down.

## Output

Each checked URL prints one line as soon as its request completes — results
arrive in completion order, not input order:

```
200    142ms  https://go.dev
301    210ms  http://go.dev  -> https://go.dev/
ERR     5.0s  https://slow.example.com  (timeout)
```

Status codes are colored (2xx green, 3xx yellow, 4xx/5xx and errors red) when
stdout is a terminal. Color is disabled automatically when output is piped or
redirected, when `NO_COLOR` is set, or when `TERM=dumb`.

A summary is written to stderr after all requests finish:

```
Checked 50 URLs in 2.3s: 47 ok, 3 failed
Latency: avg 142ms, slowest 1.2s (https://example.com/slow)
Failures: 2 timeout, 1 http 4xx
```

Latency figures only cover requests that received a response, so timeouts
don't distort the average.

### Failure reasons

Transport errors are classified so the summary breakdown is meaningful:

- `timeout` — request exceeded `-t`
- `dns` — DNS lookup failed
- `tls` — certificate/TLS handshake error
- `refused` — connection refused
- `redirects` — exceeded `-r` redirect hops
- `invalid` — malformed request (e.g. bad URL)
- `canceled` — interrupted before completion (not counted as a failure)
- `other` — anything else

## Behavior notes

- Ctrl+C stops in-flight requests promptly; a result for anything already
  dispatched still gets printed, and the process exits with code `130`.
- If stdout can't be written to (e.g. a broken pipe), the remaining requests
  are drained but not printed, and the process exits with code `1`.
- Running `urlcheck` with no arguments and nothing piped in prints usage
  instead of hanging on stdin.

## Project layout

```
cmd/urlcheck/        CLI entry point: flag parsing, wiring, exit codes
internal/checker/    HTTP checks, worker pool, error classification
internal/input/      URL collection and validation (args/file/stdin)
internal/report/     Line and summary formatting
```

## Development

```
go test ./...
go build ./...
```

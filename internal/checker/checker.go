package checker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"
)

// ErrKind is a coarse classification of why a request failed.
type ErrKind string

const (
	KindNone      ErrKind = ""
	KindTimeout   ErrKind = "timeout"
	KindDNS       ErrKind = "dns"
	KindTLS       ErrKind = "tls"
	KindRefused   ErrKind = "refused"
	KindRedirects ErrKind = "redirects"
	KindCanceled  ErrKind = "canceled"
	KindInvalid   ErrKind = "invalid"
	KindOther     ErrKind = "other"
)

// ErrTooManyRedirects is returned (wrapped) when the redirect cap is exceeded.
var ErrTooManyRedirects = errors.New("too many redirects")

// Job is one URL to check. Index is its original position in the input.
type Job struct {
	Index int
	URL   string
}

// Result is the outcome of checking one Job.
type Result struct {
	Job      Job
	Status   int           // 0 if no response was received
	FinalURL string        // URL after following redirects
	Latency  time.Duration // time until response headers arrived (or error)
	Err      error
	ErrKind  ErrKind
}

// Healthy is the default definition: no transport error and status < 400.
func (r Result) Healthy() bool {
	return r.Err == nil && r.Status > 0 && r.Status < 400
}

// NewClient builds one shared client. Reuse it across all workers so that
// connection pooling works.
func NewClient(timeout time.Duration, maxRedirects, concurrency int) *http.Client {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        concurrency,
		MaxIdleConnsPerHost: concurrency,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return fmt.Errorf("%w (limit %d)", ErrTooManyRedirects, maxRedirects)
			}
			return nil
		},
	}
}

// Check performs a single request and never panics or returns an error:
// everything that goes wrong is recorded in the Result.
func Check(ctx context.Context, client *http.Client, method string, job Job) Result {
	res := Result{Job: job}

	req, err := http.NewRequestWithContext(ctx, method, job.URL, nil)
	if err != nil {
		res.Err, res.ErrKind = err, KindInvalid
		return res
	}

	start := time.Now()
	resp, err := client.Do(req)
	res.Latency = time.Since(start)
	if err != nil {
		res.Err, res.ErrKind = err, classify(err)
		return res
	}
	defer resp.Body.Close()

	// Drain the body so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)

	res.Status = resp.StatusCode
	res.FinalURL = resp.Request.URL.String()
	return res
}

// classify maps a request error to an ErrKind. Order matters: more specific
// checks come before the generic timeout check.
func classify(err error) ErrKind {
	var (
		dnsErr      *net.DNSError
		unknownAuth x509.UnknownAuthorityError
		certInvalid x509.CertificateInvalidError
		hostErr     x509.HostnameError
		tlsRecord   tls.RecordHeaderError
		netErr      net.Error
	)

	switch {
	case errors.Is(err, context.Canceled):
		return KindCanceled
	case errors.Is(err, ErrTooManyRedirects):
		return KindRedirects
	case errors.As(err, &dnsErr):
		return KindDNS
	case errors.As(err, &unknownAuth),
		errors.As(err, &certInvalid),
		errors.As(err, &hostErr),
		errors.As(err, &tlsRecord):
		return KindTLS
	case errors.Is(err, syscall.ECONNREFUSED):
		return KindRefused
	case errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &netErr) && netErr.Timeout():
		return KindTimeout
	default:
		return KindOther
	}
}

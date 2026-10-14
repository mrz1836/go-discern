// Package httpx is the single HTTP transport contract shared by the Jev and Clef
// adapters: one bounded POST attempt per call, no redirects, no retries, and
// errors that never carry URLs, headers, credentials or bodies.
package httpx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/mrz1836/go-discern"
)

// Transport defaults and limits.
const (
	// DefaultTimeout bounds one call, including reading the whole response body.
	DefaultTimeout = 30 * time.Second

	// DefaultMaxResponseBytes is the default response body limit.
	DefaultMaxResponseBytes = 4 << 20

	// MaxResponseBytesLimit is the largest response body limit adapters accept.
	MaxResponseBytesLimit = 64 << 20

	// UserAgent is the User-Agent header sent on every call.
	UserAgent = "go-discern"

	// MaxRetryAfter is the longest retry hint reported; larger hints are clamped.
	MaxRetryAfter = 24 * time.Hour
)

// Sentinel causes. They are reachable through errors.Is on the *discern.ProviderError
// but never appear in its Error string.
var (
	// ErrStatus is the cause of a FailureRejected error built by Rejected.
	ErrStatus = errors.New("httpx: provider rejected the call")

	// errTooLarge is the cause of a FailureTooLarge error.
	errTooLarge = errors.New("httpx: response body exceeds the size limit")

	// errBuildRequest is the cause when the HTTP request cannot be built. The
	// underlying error is dropped because it quotes the URL.
	errBuildRequest = errors.New("httpx: cannot build the HTTP request")

	// errTransport replaces a transport error that carries no inner error.
	errTransport = errors.New("httpx: transport failed")
)

// Request is one provider call.
type Request struct {
	// Provider labels every error.
	Provider discern.Provider

	// URL is the full endpoint URL. It never appears in errors.
	URL string

	// Authorization is the full header value, such as "Bearer <token>". It is
	// omitted when empty.
	Authorization string

	// Body is sent verbatim as application/json.
	Body []byte

	// Timeout bounds the call including the whole body read. Values <= 0 use
	// DefaultTimeout.
	Timeout time.Duration

	// MaxBytes is the response body limit, clamped to [1, MaxResponseBytesLimit].
	MaxBytes int64

	// RequestIDHeaders are tried in order; the first valid value (see ValidID)
	// becomes Response.RequestID, and invalid values are skipped as if absent.
	// Names in canonical form (http.CanonicalHeaderKey) are looked up without
	// allocating.
	RequestIDHeaders []string

	// RayIDHeader optionally names the header copied to Response.RayID, ideally
	// in canonical form. An invalid value is dropped.
	RayIDHeader string
}

// Response is a fully read HTTP response of any status. RequestID and RayID are
// empty unless the response carried a valid one.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	RequestID  string
	RayID      string
}

// NewClient returns a copy of hc that never follows redirects and has no cookie
// jar. A nil hc yields a client on http.DefaultTransport. The transport is shared
// and never mutated, hc.Timeout is kept, and hc itself is never modified.
func NewClient(hc *http.Client) *http.Client {
	var c http.Client
	if hc == nil {
		c.Transport = http.DefaultTransport
	} else {
		c = *hc
	}
	c.CheckRedirect = noRedirect
	c.Jar = nil
	return &c
}

// noRedirect stops the client at the first response: a redirect is returned to
// the adapter as a rejection, and credentials are never sent to another URL.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Do makes exactly one POST attempt and reads the whole body, bounded by MaxBytes.
// The timeout covers the request and the full body read; the caller's deadline
// still applies. A non-2xx status, including a redirect, which is never followed,
// is not an error: the Response is returned so the adapter can inspect the body.
// Every error is a *discern.ProviderError, and the body is closed on every path.
// An error after a non-2xx status line, such as a body over the limit or a read
// that fails, still carries that response's retry hint.
func Do(ctx context.Context, hc *http.Client, r Request) (*Response, error) {
	if hc == nil {
		hc = NewClient(nil)
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxBytes := min(max(r.MaxBytes, 1), MaxResponseBytesLimit)

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, r.URL, bytes.NewReader(r.Body))
	if err != nil {
		return nil, discern.NewProviderError(r.Provider, discern.FailureTransport, errBuildRequest)
	}
	// A rewindable body lets the standard library send the request again on its
	// own: HTTP/2 resends it when a stream is refused or reset or the connection
	// goes away, even after the server has read the whole body, and a reset can
	// repeat until the timeout. Without GetBody every call is one attempt.
	req.GetBody = nil
	setHeaders(req.Header, r.Authorization)

	resp, err := hc.Do(req)
	if err != nil {
		return nil, classify(ctx, callCtx, r.Provider, transportCause(err))
	}
	// A close error after a complete read changes nothing: the body is in hand.
	defer func() { _ = resp.Body.Close() }()

	out := &Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		RequestID:  firstHeader(resp.Header, r.RequestIDHeaders),
	}
	if r.RayIDHeader != "" {
		if v := resp.Header.Get(r.RayIDHeader); ValidID(v) {
			out.RayID = v
		}
	}
	if resp.ContentLength > maxBytes {
		return nil, out.failure(r.Provider, discern.FailureTooLarge, errTooLarge)
	}
	body, err := readBody(resp.Body, resp.ContentLength, maxBytes)
	if err != nil {
		pe := classify(ctx, callCtx, r.Provider, err)
		out.annotateRead(pe)
		return nil, pe
	}
	if int64(len(body)) > maxBytes {
		return nil, out.failure(r.Provider, discern.FailureTooLarge, errTooLarge)
	}
	out.Body = body
	return out, nil
}

// setHeaders sets the request headers of every call on h, a fresh header map.
// The four values share one backing array, allocated per call so that no two
// requests share header memory; each value is a full slice expression, so an
// append to one cannot overwrite the next.
//
// There is no Idempotency-Key: with one, the standard library may replay a POST
// on a reused connection.
func setHeaders(h http.Header, authorization string) {
	values := append(make([]string, 0, 4), "application/json", "application/json", UserAgent, authorization)
	h["Content-Type"] = values[0:1:1]
	h["Accept"] = values[1:2:2]
	h["User-Agent"] = values[2:3:3]
	if authorization != "" {
		h["Authorization"] = values[3:4:4]
	}
}

// readBody reads r to EOF, but never more than maxBytes+1 bytes, so a result
// longer than maxBytes means the body is too large. A declared length, already
// known to be within the limit, sizes the buffer exactly, with one spare byte to
// see EOF without growing: the body is allocated for once. A body of unknown
// length is read by io.ReadAll, which grows in chunks and copies once into a
// right-sized result, so it allocates about twice what it reads.
func readBody(r io.Reader, contentLength, maxBytes int64) ([]byte, error) {
	limited := io.LimitReader(r, maxBytes+1)
	if contentLength < 0 {
		return io.ReadAll(limited)
	}
	buf := make([]byte, 0, contentLength+1)
	for len(buf) < cap(buf) {
		n, err := limited.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if errors.Is(err, io.EOF) {
			return buf, nil
		}
		if err != nil {
			return buf, err
		}
	}
	// The body is longer than declared: read on, still within the limit.
	rest, err := io.ReadAll(limited)
	return append(buf, rest...), err
}

// Malformed builds the FailureMalformed error for a response whose body fails
// decoding. It carries the status and the request and ray IDs; cause names the
// failing field but never quotes the body.
func Malformed(provider discern.Provider, resp *Response, cause error) *discern.ProviderError {
	pe := discern.NewProviderError(provider, discern.FailureMalformed, cause)
	if resp != nil {
		resp.annotate(pe)
	}
	return pe
}

// Rejected builds the FailureRejected error for a refused call, such as a non-2xx
// response or a Cloudflare envelope with success:false. It carries the status,
// request and ray IDs, the retry hint, and the provider's codes; messages are
// reachable only through ProviderMessages. The cause is ErrStatus.
func Rejected(provider discern.Provider, resp *Response, now time.Time, codes []int, messages []string) *discern.ProviderError {
	pe := discern.NewProviderError(provider, discern.FailureRejected, ErrStatus, messages...)
	if resp != nil {
		resp.annotate(pe)
		pe.RetryAfter, pe.HasRetryAfter = RetryAfter(resp.Header, now)
	}
	pe.Codes = slices.Clone(codes)
	return pe
}

// failure returns the ProviderError for a response whose body could not be
// used, carrying the response metadata; see annotateRead.
func (r *Response) failure(provider discern.Provider, kind discern.FailureKind, cause error) *discern.ProviderError {
	pe := discern.NewProviderError(provider, kind, cause)
	r.annotateRead(pe)
	return pe
}

// annotate copies the response metadata onto pe.
func (r *Response) annotate(pe *discern.ProviderError) {
	pe.StatusCode = r.StatusCode
	pe.RequestID = r.RequestID
	pe.RayID = r.RayID
}

// annotateRead is annotate for a failure while reading the body. A non-2xx
// response is a refusal whatever its body turned out to be, so its retry hint
// is kept too, dated now; a 2xx response's hint is not reported.
func (r *Response) annotateRead(pe *discern.ProviderError) {
	r.annotate(pe)
	if r.StatusCode < http.StatusOK || r.StatusCode >= http.StatusMultipleChoices {
		pe.RetryAfter, pe.HasRetryAfter = RetryAfter(r.Header, time.Now())
	}
}

// classify maps a failed exchange onto a failure kind: the caller's context
// first, then this call's own timeout, then any other error that matches
// context.DeadlineExceeded, such as an injected http.Client's Timeout firing
// while awaiting headers or reading the body, else a transport failure with err
// as the cause. A timeout's cause is the bare context.DeadlineExceeded.
func classify(caller, call context.Context, provider discern.Provider, err error) *discern.ProviderError {
	if cerr := caller.Err(); cerr != nil {
		kind := discern.FailureCanceled
		if errors.Is(cerr, context.DeadlineExceeded) {
			kind = discern.FailureDeadlineExceeded
		}
		return discern.NewProviderError(provider, kind, cerr)
	}
	if errors.Is(call.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return discern.NewProviderError(provider, discern.FailureTimeout, context.DeadlineExceeded)
	}
	return discern.NewProviderError(provider, discern.FailureTransport, err)
}

// transportCause strips every *url.Error, whose message quotes the URL (for Clef,
// the account ID), and keeps only the innermost error.
func transportCause(err error) error {
	for {
		ue, ok := errors.AsType[*url.Error](err)
		if !ok {
			return err
		}
		if ue.Err == nil {
			return errTransport
		}
		err = ue.Err
	}
}

// firstHeader returns the first valid value (see ValidID) among the named
// headers.
func firstHeader(h http.Header, names []string) string {
	for _, name := range names {
		if v := h.Get(name); ValidID(v) {
			return v
		}
	}
	return ""
}

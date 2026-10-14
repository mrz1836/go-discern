// Package adapter is the plumbing the Jev and Clef adapters share: their common
// options and the validation of them, the function that holds each Client's
// connection settings where no formatting can reach them, the one attempt each
// call makes, and the errors built along the way. Each adapter keeps only what
// is specific to its provider.
package adapter

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/httpx"
	"github.com/mrz1836/go-discern/internal/systemone"
)

// Config holds the settings both adapters accept through options. Each adapter
// declares its own Option type over Config, so the two option sets stay
// distinct types and cannot be mixed.
type Config struct {
	Model       string
	Timeout     time.Duration
	HTTPClient  *http.Client
	MaxBytes    int64
	Endpoint    string
	HasEndpoint bool
}

// Configure returns the defaults (model, httpx.DefaultTimeout and
// httpx.DefaultMaxResponseBytes) with opts applied in order. Nil options are
// skipped, and the last option of each kind wins. Options only record their
// argument; Origin and the adapter validate the result.
func Configure[O ~func(*Config)](model string, opts []O) Config {
	cfg := Config{Model: model, Timeout: httpx.DefaultTimeout, MaxBytes: httpx.DefaultMaxResponseBytes}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	return cfg
}

// Origin validates the timeout, the response limit and the test endpoint, in
// that order, and returns the API origin: production, or the validated
// loopback endpoint when WithTestEndpoint was used. Errors match
// discern.ErrInvalidConfig and never quote the rejected value.
func (c *Config) Origin(production string) (string, error) {
	switch {
	case c.Timeout <= 0:
		return "", ConfigError("timeout", "must be positive")
	case c.MaxBytes < 1 || c.MaxBytes > httpx.MaxResponseBytesLimit:
		return "", ConfigError("max_response_bytes", "must be from 1 byte to 64 MiB")
	case !c.HasEndpoint:
		return production, nil
	}
	base, err := httpx.TestEndpoint(c.Endpoint)
	if err != nil {
		return "", ConfigError("endpoint", "must be an https URL on 127.0.0.0/8 or [::1] with an optional port and no path, query, fragment or userinfo")
	}
	return base, nil
}

// CheckCredential validates a credential sent in the Authorization header: 1 to
// 4096 bytes of visible ASCII. The error names the field, never the value.
func CheckCredential(field, credential string) error {
	if !httpx.ValidCredential(credential) {
		return ConfigError(field, "must be 1 to 4096 bytes of visible ASCII, without spaces or control characters")
	}
	return nil
}

// ConfigError returns a *discern.ValidationError matching
// discern.ErrInvalidConfig. The reason must not contain the rejected value.
func ConfigError(field, reason string) error {
	return &discern.ValidationError{Err: discern.ErrInvalidConfig, Field: field, Reason: reason}
}

// Describe returns the redacted form both adapters print for a Client, such as
// `jev.Client{model: "jev-1.13.0"}`.
func Describe(pkg, model string) string {
	return pkg + ".Client{model: " + strconv.Quote(model) + "}"
}

// Spec describes one provider: where calls go and how its responses read.
type Spec struct {
	// Provider labels every error.
	Provider discern.Provider

	// URL is the full endpoint URL and Authorization the full header value.
	// The Sender keeps both inside its closure.
	URL           string
	Authorization string

	// RequestIDHeader and RayIDHeader name the response headers copied to the
	// Observation and to errors. Either may be empty.
	RequestIDHeader string
	RayIDHeader     string

	// Refusal, when set, extracts the provider's error codes and messages from
	// a non-2xx body. Without it, error bodies are opaque.
	Refusal func(body []byte) (codes []int, messages []string)

	// Decode turns a fully read 2xx response into an Observation. The Sender
	// returns an error that is not a *discern.ProviderError, such as a strict
	// decoding failure, as httpx.Malformed, so every such error carries the
	// response metadata; a ProviderError, such as a refusal built with
	// Rejected, is returned as it is. The Sender sets RequestID and RayID on a
	// decoded Observation.
	Decode func(resp *httpx.Response, req *discern.Request) (*discern.Observation, error)
}

// Sender makes the calls of one Client. Everything a call needs, including the
// URL and the Authorization value, is captured inside the function: fmt prints a
// func only as an address, with every verb and at every depth, and
// reflection-based dumpers cannot see what a closure captures, so no formatting
// of a Client or of a struct holding one can reach them. The nil Sender belongs
// to a zero Client and prepares nothing.
type Sender func(ctx context.Context, req *discern.Request, body []byte) (*discern.Observation, error)

// NewSender returns the Sender for cfg, which Origin has validated, and spec.
// Header names are canonicalized once here, so that reading them from each
// response allocates nothing.
func NewSender(cfg *Config, spec Spec) Sender {
	hc := httpx.NewClient(cfg.HTTPClient)
	base := httpx.Request{
		Provider:      spec.Provider,
		URL:           spec.URL,
		Authorization: spec.Authorization,
		Timeout:       cfg.Timeout,
		MaxBytes:      cfg.MaxBytes,
		RayIDHeader:   http.CanonicalHeaderKey(spec.RayIDHeader),
	}
	if spec.RequestIDHeader != "" {
		base.RequestIDHeaders = []string{http.CanonicalHeaderKey(spec.RequestIDHeader)}
	}
	provider, refusal, decode := spec.Provider, spec.Refusal, spec.Decode
	return func(ctx context.Context, req *discern.Request, body []byte) (*discern.Observation, error) {
		hr := base
		hr.Body = body
		resp, err := httpx.Do(ctx, hc, hr)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			var (
				codes    []int
				messages []string
			)
			if refusal != nil {
				codes, messages = refusal(resp.Body)
			}
			return nil, Rejected(provider, resp, codes, messages)
		}
		obs, err := decode(resp, req)
		if err != nil {
			if _, ok := errors.AsType[*discern.ProviderError](err); ok {
				return nil, err
			}
			return nil, httpx.Malformed(provider, resp, err)
		}
		obs.RequestID, obs.RayID = resp.RequestID, resp.RayID
		return obs, nil
	}
}

// Check returns the errors every Prepare starts with: a zero Client, which
// matches discern.ErrInvalidConfig, then a nil request, which matches
// discern.ErrInvalidRequest.
func (s Sender) Check(req *discern.Request) error {
	if s == nil {
		return ConfigError("client", "is not initialized; create it with New")
	}
	if req == nil {
		return &discern.ValidationError{Err: discern.ErrInvalidRequest, Field: "request", Reason: "is nil"}
	}
	return nil
}

// CheckResponseSize returns an error matching discern.ErrUnsupported when even
// the shortest response that answers req (systemone.MinResultSize) is larger
// than maxBytes, the Client's response limit: every answer to such a request
// would be billed and then fail as discern.FailureTooLarge. The error is a
// *discern.ValidationError on the field "request".
func CheckResponseSize(req *discern.Request, maxBytes int64) error {
	if systemone.MinResultSize(req) > maxBytes {
		return &discern.ValidationError{
			Err: discern.ErrUnsupported, Field: "request",
			Reason: "the response would exceed the response size limit; raise WithMaxResponseBytes",
		}
	}
	return nil
}

// Call returns a discern.Call that sends the encoded body for req. Each
// invocation makes exactly one attempt.
func (s Sender) Call(req *discern.Request, body []byte) discern.Call {
	return func(ctx context.Context) (*discern.Observation, error) {
		return s(ctx, req, body)
	}
}

// Rejected returns the FailureRejected error for a refused call, with the
// status, the request and ray IDs, the retry hint dated now, and the provider's
// codes; the messages are reachable only through ProviderMessages.
func Rejected(provider discern.Provider, resp *httpx.Response, codes []int, messages []string) *discern.ProviderError {
	return httpx.Rejected(provider, resp, time.Now(), codes, messages)
}

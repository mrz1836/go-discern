package jev

import (
	"net/http"
	"time"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/adapter"
	"github.com/mrz1836/go-discern/internal/httpx"
	"github.com/mrz1836/go-discern/internal/ident"
	"github.com/mrz1836/go-discern/internal/systemone"
)

// DefaultModel is the model a Client requests unless WithModel says otherwise. It
// is the default of TypeSafe's own SDKs and an alias that moves to newer versions;
// TypeSafe recommends pinning a versioned model, such as "jev-1.13.0", in
// production.
const DefaultModel = "jev-latest"

const (
	// productionBase is the origin of TypeSafe's hosted API.
	productionBase = "https://api.typesafe.ai"

	// systemOnePath is the System One endpoint, relative to the origin.
	systemOnePath = "/v1/systemone"

	// requestIDHeader carries TypeSafe's identifier for the request.
	requestIDHeader = "x-typesafe-request-id"

	// maxModelLength bounds a model name, in bytes.
	maxModelLength = 128
)

// Option configures a Client in New. Nil options are skipped, and the last option
// of each kind wins. Options only record their argument; New validates it.
type Option func(*config)

// config is the configuration shared with the Clef adapter. Option is still a
// type of this package, so Jev and Clef options cannot be mixed.
type config = adapter.Config

// WithModel sets the model sent with every request: 1 to 128 bytes of
// [A-Za-z0-9._-], such as "jev-1.13.0". Any other value makes New fail with
// discern.ErrInvalidConfig. The default is DefaultModel.
func WithModel(model string) Option {
	return func(c *config) { c.Model = model }
}

// WithTimeout bounds each call, including sending the request and reading the
// whole response body. The caller's context deadline still applies when it is
// earlier. The default is 30 seconds; d must be positive.
func WithTimeout(d time.Duration) Option {
	return func(c *config) { c.Timeout = d }
}

// WithHTTPClient sets the HTTP client that carries every call. New copies it with
// redirects disabled and no cookie jar, and never modifies hc; its Transport is
// shared and its Timeout still applies, and when it fires first the call fails
// with discern.FailureTimeout, as WithTimeout does. A nil hc selects a client on
// http.DefaultTransport, which is also the default.
//
// A transport that retries, follows redirects or replays requests on its own
// defeats the one-attempt guarantee; such transports are outside this package's
// guarantees.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *config) { c.HTTPClient = hc }
}

// WithMaxResponseBytes limits the response body size. A larger body fails the
// call with discern.FailureTooLarge, which matches discern.ErrMalformedResponse.
// The default is 4 MiB; n must be from 1 to 64 MiB.
//
// Prepare also compares the limit with the shortest response that could answer
// a request: when even that is larger, as with dozens of Choice questions with
// hundreds of long option IDs each, the request fails with
// discern.ErrUnsupported before any call, rather than being billed and then
// failing as too large. Raise the limit to send such requests.
func WithMaxResponseBytes(n int64) Option {
	return func(c *config) { c.MaxBytes = n }
}

// WithTestEndpoint replaces https://api.typesafe.ai with a loopback base URL, such
// as the URL of an httptest.NewTLSServer. It exists for tests: only https on
// 127.0.0.0/8 or [::1], with an optional port and no path, query or userinfo, is
// accepted. Host names, including "localhost", are rejected because they resolve
// through the system resolver. Requests go to <base>/v1/systemone.
func WithTestEndpoint(base string) Option {
	return func(c *config) { c.Endpoint, c.HasEndpoint = base, true }
}

// Client is the Jev adapter: a discern.Adapter that sends each prepared request to
// TypeSafe's System One endpoint. Register it with discern.WithAdapter. A Client
// is immutable after New and safe for concurrent use.
//
// Every method has a value receiver, so printing a Client or *Client with fmt goes
// through String or GoString, which show only the model. The API key and the URL
// exist only inside a function the Client holds: fmt prints a func as an address
// with every verb, even through unexported fields, and reflection-based dumpers
// cannot see what a function captures, so no formatting of a Client or of a value
// holding one reveals them. The zero Client is unusable; its Prepare reports
// discern.ErrInvalidConfig.
type Client struct {
	model    string
	maxBytes int64 // the response limit, for Prepare's size check
	send     adapter.Sender
}

// New returns a Client that authenticates with apiKey, sent as a bearer token.
// apiKey must be 1 to 4096 bytes of visible ASCII (no spaces or control
// characters). Invalid arguments or options return a *discern.ValidationError
// matching discern.ErrInvalidConfig; its message never repeats the rejected value.
func New(apiKey string, opts ...Option) (*Client, error) {
	if err := adapter.CheckCredential("api_key", apiKey); err != nil {
		return nil, err
	}
	cfg := adapter.Configure(DefaultModel, opts)
	if !ident.Name(cfg.Model, maxModelLength) {
		return nil, adapter.ConfigError("model", "must be 1 to 128 bytes of [A-Za-z0-9._-]")
	}
	base, err := cfg.Origin(productionBase)
	if err != nil {
		return nil, err
	}
	return &Client{model: cfg.Model, maxBytes: cfg.MaxBytes, send: adapter.NewSender(&cfg, adapter.Spec{
		Provider:        discern.Jev,
		URL:             base + systemOnePath,
		Authorization:   "Bearer " + apiKey,
		RequestIDHeader: requestIDHeader,
		// Jev does not document its error bodies; the FastAPI-style "detail" it
		// sends is read best-effort into provider messages and never fails the
		// call a second time.
		Refusal: systemone.DecodeDetailRefusal,
		Decode:  decode,
	})}, nil
}

// Provider returns discern.Jev.
func (c Client) Provider() discern.Provider { return discern.Jev }

// Model returns the model the Client requests, as configured. The model the
// provider reports is recorded separately in each Observation, so an alias such
// as DefaultModel can be traced to the version that answered.
func (c Client) Model() string { return c.model }

// Prepare encodes req once into the exact request body and returns a Call that
// sends it. It performs no I/O. A nil req returns an error matching
// discern.ErrInvalidRequest, as does a request the encoder cannot represent.
//
// Jev documents no question count, question ID, body size or truncation limits,
// so Prepare adds none beyond the checks discern applies to every request; the
// API's own refusal surfaces as discern.ErrRejected. The one check of its own is
// local: a request whose shortest possible response is larger than the
// WithMaxResponseBytes limit returns a *discern.ValidationError on the field
// "request" matching discern.ErrUnsupported, because every answer to it would be
// billed and then rejected as too large.
//
// Each invocation of the returned Call makes exactly one POST attempt: it never
// retries and never follows a redirect, which fails as discern.ErrRejected. Its
// errors are *discern.ProviderError values.
func (c Client) Prepare(req *discern.Request) (discern.Call, error) {
	if err := c.send.Check(req); err != nil {
		return nil, err
	}
	body, err := systemone.EncodeRequest(c.model, req)
	if err != nil {
		return nil, err
	}
	if err = adapter.CheckResponseSize(req, c.maxBytes); err != nil {
		return nil, err
	}
	return c.send.Call(req, body), nil
}

// String returns `jev.Client{model: "<model>"}`. It never includes the API key or
// the URL.
func (c Client) String() string { return adapter.Describe("jev", c.model) }

// GoString returns the same redacted form as String, so %#v cannot leak the API key
// or the URL either.
func (c Client) GoString() string { return c.String() }

// decode strictly decodes a 2xx body. A non-2xx status is a rejection that
// carries the status, request ID and retry hint, and whatever messages its
// "detail" holds. A 2xx body that fails strict decoding is a malformed response,
// which the Sender labels with the status and request ID.
func decode(resp *httpx.Response, req *discern.Request) (*discern.Observation, error) {
	return systemone.DecodeResult(resp.Body, req)
}

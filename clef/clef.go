package clef

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/adapter"
	"github.com/mrz1836/go-discern/internal/httpx"
	"github.com/mrz1836/go-discern/internal/systemone"
)

// Models. The request body names the same model as the route. This package sends
// text and JSON state only, whichever model is selected.
const (
	// ModelClef is @cf/cloudflare/clef, the default model, with a 65,536-token
	// context window.
	ModelClef = "clef"

	// ModelFlash is @cf/cloudflare/clef-flash: smaller, faster and cheaper, with
	// a 24,576-token context window.
	ModelFlash = "clef-flash"

	// ModelOmni is @cf/cloudflare/clef-omni: a 30B-parameter mixture of experts
	// with 3B active, with a 64,000-token context window. Its audio and video
	// inputs are not supported.
	ModelOmni = "clef-omni"
)

// Documented Clef request limits. Prepare checks both before any call.
const (
	// MaxQuestions is Clef's documented per-request question limit.
	MaxQuestions = 64

	// MaxRequestBytes is Clef's documented request body limit (13 MiB). It is
	// documented for clef and clef-flash and applied to clef-omni as well.
	MaxRequestBytes = 13 << 20
)

const (
	// productionBase is the Cloudflare REST API origin.
	productionBase = "https://api.cloudflare.com"

	// accountIDLength is the length of a Cloudflare account ID in hex digits.
	accountIDLength = 32

	// Response headers observed on Workers AI calls. None is documented, so
	// each is read best-effort.
	requestIDHeader = "Cf-Ai-Req-Id"
	rayIDHeader     = "Cf-Ray"
	neuronsHeader   = "Cf-Ai-Neurons"
)

// errNoResult is the cause of a malformed-response error for a successful
// envelope that carries no result.
var errNoResult = errors.New("clef: successful envelope has no result")

// Option configures a Client in New. Nil options are skipped, and the last option
// of each kind wins.
type Option func(*config)

// config is the configuration shared with the Jev adapter. Option is still a
// type of this package, so Clef and Jev options cannot be mixed.
type config = adapter.Config

// WithModel selects the model: ModelClef (the default), ModelFlash or ModelOmni.
// Any other value, including a full "@cf/cloudflare/..." name or a different
// letter case, makes New fail with discern.ErrInvalidConfig.
func WithModel(model string) Option {
	return func(c *config) { c.Model = model }
}

// WithTimeout bounds each call, including reading the whole response body. The
// default is 30 seconds, and the caller's context deadline still applies. d must
// be positive.
func WithTimeout(d time.Duration) Option {
	return func(c *config) { c.Timeout = d }
}

// WithHTTPClient sets the HTTP client. New copies it with redirects disabled and
// no cookie jar; the caller's client is never modified, and its Transport and
// Timeout are used as they are. When its Timeout fires first, the call fails
// with discern.FailureTimeout, as WithTimeout does. A nil client selects a
// default client on http.DefaultTransport. A transport that retries or follows
// redirects on its own is outside this package's one-attempt guarantee.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *config) { c.HTTPClient = hc }
}

// WithMaxResponseBytes limits the response body size. The default is 4 MiB, and n
// must be from 1 to 64 MiB. A larger response fails with
// discern.FailureTooLarge, which matches discern.ErrMalformedResponse.
//
// Prepare also compares the limit with the shortest response that could answer
// a request: when even that is larger, as with many Choice questions with
// hundreds of long option IDs each, the request fails with
// discern.ErrUnsupported before any call, rather than being billed and then
// failing as too large. Raise the limit to send such requests.
func WithMaxResponseBytes(n int64) Option {
	return func(c *config) { c.MaxBytes = n }
}

// WithTestEndpoint replaces https://api.cloudflare.com with a loopback base URL,
// such as the URL of an httptest.NewTLSServer, and keeps the API path. It is
// meant for tests only: base must be https on 127.0.0.0/8 or [::1], with an
// optional port and no path, query, fragment or userinfo. Anything else makes New
// fail with discern.ErrInvalidConfig.
func WithTestEndpoint(base string) Option {
	return func(c *config) { c.Endpoint, c.HasEndpoint = base, true }
}

// Client is a discern.Adapter for Cloudflare Workers AI Clef. Build it with New;
// the zero Client prepares no calls. A Client is immutable and safe for
// concurrent use. Formatting it with fmt, by any verb, never reveals the API
// token, the account ID or the endpoint URL.
type Client struct {
	model string

	// maxBytes is the response limit, for Prepare's size check.
	maxBytes int64

	// send makes one call. The URL, which contains the account ID, and the
	// Authorization value exist only inside this function: fmt and
	// reflection-based dumpers print a func as an address, so no verb or dump can
	// reach them.
	send adapter.Sender
}

// New returns a Client for the Cloudflare account and API token.
//
// accountID must be exactly 32 hexadecimal characters. It is placed in the URL
// path, so this is deliberately narrower than any string Cloudflare might accept.
// Cloudflare account IDs are lower-case hex, so an upper-case ID is lower-cased
// before it is placed in the URL.
// apiToken must be 1 to 4096 visible ASCII characters, with no spaces or control
// characters. Errors match discern.ErrInvalidConfig, are *discern.ValidationError
// values naming the field, and never quote the arguments.
func New(accountID, apiToken string, opts ...Option) (*Client, error) {
	if !validAccountID(accountID) {
		return nil, adapter.ConfigError("account_id", "must be exactly 32 hexadecimal characters")
	}
	if err := adapter.CheckCredential("api_token", apiToken); err != nil {
		return nil, err
	}
	cfg := adapter.Configure(ModelClef, opts)
	switch cfg.Model {
	case ModelClef, ModelFlash, ModelOmni:
	default:
		return nil, adapter.ConfigError("model", "must be clef, clef-flash or clef-omni")
	}
	base, err := cfg.Origin(productionBase)
	if err != nil {
		return nil, err
	}
	return &Client{model: cfg.Model, maxBytes: cfg.MaxBytes, send: adapter.NewSender(&cfg, adapter.Spec{
		Provider:        discern.Clef,
		URL:             base + "/client/v4/accounts/" + strings.ToLower(accountID) + "/ai/run/@cf/cloudflare/" + cfg.Model,
		Authorization:   "Bearer " + apiToken,
		RequestIDHeader: requestIDHeader,
		RayIDHeader:     rayIDHeader,
		// A refused call's body may be an envelope, with or without success,
		// an HTML error page or nothing at all; its codes and messages are read
		// best-effort and never fail the call a second time.
		Refusal: systemone.DecodeRefusal,
		Decode:  decode,
	})}, nil
}

// Provider returns discern.Clef.
func (c Client) Provider() discern.Provider { return discern.Clef }

// Model returns the configured model: ModelClef, ModelFlash or ModelOmni.
func (c Client) Model() string { return c.model }

// Prepare checks Clef's limits and encodes the request body once, without any
// I/O. It returns an error matching discern.ErrInvalidRequest for a nil request,
// and one matching discern.ErrUnsupported for more than MaxQuestions questions,
// an encoded body larger than MaxRequestBytes, or a request whose shortest
// possible response is larger than the WithMaxResponseBytes limit, because
// every answer to it would be billed and then rejected as too large.
//
// Each invocation of the returned Call makes exactly one POST attempt and never
// retries. A non-2xx status, including a redirect, and a 2xx envelope with
// success:false or any errors[] entry match discern.ErrRejected; a 2xx envelope
// without a result, or with one that fails strict decoding, matches
// discern.ErrMalformedResponse. Every error is a *discern.ProviderError.
func (c Client) Prepare(req *discern.Request) (discern.Call, error) {
	if err := c.send.Check(req); err != nil {
		return nil, err
	}
	if len(req.Questions) > MaxQuestions {
		return nil, unsupported("questions", "clef accepts at most 64 questions")
	}
	body, err := systemone.EncodeRequest(c.model, req)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxRequestBytes {
		return nil, unsupported("request", "the encoded body exceeds clef's 13 MiB request limit")
	}
	if err = adapter.CheckResponseSize(req, c.maxBytes); err != nil {
		return nil, err
	}
	return c.send.Call(req, body), nil
}

// String returns the type and model only, such as clef.Client{model: "clef"}.
func (c Client) String() string { return adapter.Describe("clef", c.model) }

// GoString returns the same redacted form as String, so %#v never prints the
// API token, the account ID or the endpoint URL.
func (c Client) GoString() string { return c.String() }

// decode reads the envelope of a 2xx response and decodes its result. An
// envelope with success:false is a refusal whatever its errors[] entries look
// like; one with success:true must have a result and no errors[] entry. Any
// other error is a malformed response, which the Sender labels with the
// response metadata.
func decode(resp *httpx.Response, req *discern.Request) (*discern.Observation, error) {
	env, err := systemone.DecodeEnvelope(resp.Body)
	if err != nil {
		return nil, err
	}
	// Whether Cloudflare ever sends success:false or errors[] with a 2xx status
	// is undocumented; either one is a refusal, never an answer, even when the
	// errors[] entries carry neither a code nor a message.
	if !env.Success || env.ErrorCount > 0 {
		return nil, adapter.Rejected(discern.Clef, resp, env.Codes, env.Messages)
	}
	if env.Result == nil {
		return nil, errNoResult
	}
	obs, err := env.DecodeResult(req)
	if err != nil {
		return nil, err
	}
	if n, ok := neurons(resp.Header); ok {
		if obs.Usage == nil {
			obs.Usage = &discern.Usage{}
		}
		obs.Usage.Neurons = &n
	}
	return obs, nil
}

// neurons parses the cf-ai-neurons header. An absent, unparseable, infinite, NaN
// or negative value reports false: it is a billing hint, never guessed.
func neurons(h http.Header) (float64, bool) {
	v := strings.TrimSpace(h.Get(neuronsHeader))
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return 0, false
	}
	if n == 0 {
		n = 0 // store negative zero as zero
	}
	return n, true
}

// validAccountID reports whether s is exactly 32 hexadecimal characters.
func validAccountID(s string) bool {
	if len(s) != accountIDLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// unsupported returns the error for a request that exceeds a Clef limit. The
// reason never contains request content.
func unsupported(field, reason string) error {
	return &discern.ValidationError{Err: discern.ErrUnsupported, Field: field, Reason: reason}
}

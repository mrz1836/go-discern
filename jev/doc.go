// Package jev is the discern adapter for TypeSafe's hosted Jev System One API
// (POST https://api.typesafe.ai/v1/systemone, authenticated with a bearer API
// key).
//
// Create a Client with an API key and register it with an evaluator:
//
//	jc, err := jev.New(apiKey, jev.WithModel("jev-1.13.0"))
//	if err != nil {
//		return err
//	}
//	ev, err := discern.New(discern.WithAdapter(jc))
//	if err != nil {
//		return err
//	}
//	res, err := ev.Evaluate(ctx, discern.Request{Providers: discern.JevOnly, State: state, Questions: questions})
//
// # Models
//
// DefaultModel is "jev-latest", the default of TypeSafe's own SDKs. "jev-latest"
// and "jev-preview" are aliases that move to newer versions over time, so pin a
// versioned model such as "jev-1.13.0" in production, as TypeSafe recommends.
// Every Result records both the model requested (Outcome.RequestedModel) and the
// model the provider reported (Observation.Model), so alias drift stays visible
// in stored records. A reported model must be at most 256 bytes of visible ASCII,
// or the response is malformed. Jev has no Flash variant. In October 2026 both
// aliases resolved to "jev-1.13.0", and the names "jev" and "jev-1.13", which
// appear in TypeSafe's SDK examples, were refused as unknown models (HTTP 400).
//
// # Calls, timeouts and retries
//
// Each Call makes exactly one HTTPS POST. The package never retries, never follows
// a redirect (a 3xx response is a rejection, and the API key is never sent to
// another URL) and never stores cookies. The timeout set by WithTimeout (30
// seconds by default) covers the whole exchange, including reading the response
// body; the caller's context deadline still applies, and whichever expires first
// ends the call. The two cases are reported apart, as discern.FailureTimeout and
// discern.FailureDeadlineExceeded. Response bodies are limited by
// WithMaxResponseBytes (4 MiB by default).
//
// A Retry-After or retry-after-ms header is reported on the *discern.ProviderError
// (RetryAfter, HasRetryAfter) and never acted on: whether repeating a possibly
// billed call is safe is the caller's decision. A fractional retry-after-ms is
// rounded up to a whole millisecond, and a refusal keeps its hint even when its
// body is too large or cannot be read.
//
// # Errors
//
// Every call error is a *discern.ProviderError carrying the HTTP status and the
// x-typesafe-request-id request ID when known; a request ID that is not 1 to 256
// bytes of visible ASCII is dropped. A non-2xx status matches
// discern.ErrRejected. Jev does not document the shape of its error bodies; the
// FastAPI-style body it sends, such as
// {"detail":{"error_type":"api_usage_error","message":"Unknown model: jev"}}, is
// read best-effort into discern.ProviderError.ProviderMessages, one message per
// error formatted as "error_type: message" (or "type: msg" for each entry of a
// detail array, or whichever of the two is present). At most 64 messages of up
// to 1 KiB each are kept, and any other body yields none. Messages may echo
// request content, so they never appear in Error, GoString or a record. Jev has
// no numeric error codes. A response body over the size limit, whatever its
// status, or a 2xx body that fails strict decoding matches
// discern.ErrMalformedResponse. Error strings, String and GoString never contain
// the API key, the URL, request content or response bodies.
//
// # Limits
//
// Jev's documented limits (at most 255 Choice options, 2 to 10 Score levels) are
// enforced by discern for every request before any call, together with rules
// discern applies to every provider so that one request is valid for both,
// which are narrower than Jev's own documentation:
//
//   - a Choice question needs at least 2 options;
//   - question IDs must match [A-Za-z0-9_.-]{1,100};
//   - instructions must be non-empty text, or a non-empty JSON object or array:
//     null, an empty object and an empty array are rejected, although Jev
//     documents null instructions as allowed;
//   - every Score level needs a description: null is rejected.
//
// Jev documents a context limit of 64k tokens per request and 32k tokens for the
// state plus the longest question, but not what happens beyond either. In
// October 2026 a state of about 31,000 tokens was answered and one of about
// 70,000 tokens was refused with HTTP 400 (error_type max_tokens_exceeded), not
// truncated. Tokens cannot be counted locally, so the limit is not checked: keep
// large states well inside it. Jev documents no maximum question
// count, question ID character set, request body size or truncation behavior,
// so this package adds no limits for them; a request the API refuses fails with
// discern.ErrRejected.
//
// One check is local: a request whose shortest possible response is larger than
// the WithMaxResponseBytes limit fails with discern.ErrUnsupported before any
// call, instead of being billed and then failing as too large.
//
// # Testing
//
// WithTestEndpoint points a Client at an https server on a loopback address
// (127.0.0.0/8 or [::1]), such as an httptest.NewTLSServer; it accepts nothing
// else, so it cannot redirect production traffic. Pair it with WithHTTPClient
// and the test server's Client. WithHTTPClient copies the client with redirects
// disabled and no cookie jar and never modifies it, but an injected transport
// that retries, follows redirects or replays requests by itself is outside this
// package's one-attempt guarantee. Code that needs no HTTP at all can use the
// fakes in package discerntest.
package jev

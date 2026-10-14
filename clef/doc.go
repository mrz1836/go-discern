// Package clef is the discern adapter for Cloudflare Workers AI Clef, a hosted
// System One judgment model. A Client sends each prepared discern.Request to
//
//	POST https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/run/@cf/cloudflare/{model}
//
// with the API token as a Bearer credential, and decodes the result inside
// Cloudflare's response envelope into a discern.Observation. The account ID is
// sent in lower case, as Cloudflare spells account IDs. Register a Client with
// discern.WithAdapter:
//
//	cc, err := clef.New(accountID, apiToken, clef.WithModel(clef.ModelFlash))
//	if err != nil {
//		return err
//	}
//	ev, err := discern.New(discern.WithAdapter(cc))
//
// # Models
//
// WithModel selects one of three models; the request body names the same model
// as the route:
//
//   - ModelClef (@cf/cloudflare/clef), the default, has the largest context
//     window, 65,536 tokens, and is the safest choice for answer quality.
//   - ModelFlash (@cf/cloudflare/clef-flash) is the smallest, fastest and
//     cheapest, with a 24,576-token window, so it accepts the least state.
//   - ModelOmni (@cf/cloudflare/clef-omni) is a 30B-parameter mixture of experts
//     with 3B active, priced between the other two, with a 64,000-token window.
//
// Tokens cannot be counted locally, so the context window is not checked before
// a call. Cloudflare's model pages say long text state is truncated to fit, but
// in October 2026 only clef-omni did so: it cut a state of about 73,000 tokens to
// exactly 64,000 input tokens and answered, so not every submitted byte is
// guaranteed to influence its answer. clef and clef-flash instead rejected a
// request that did not fit their window before the model ran, with HTTP 422 and
// code 3030 ("Request requires N tokens; the maximum is M") or, for a much larger
// request, HTTP 413 and code 5021. Such a rejection matches discern.ErrRejected,
// with the code in the record's Failure.Codes and Cloudflare's text in
// discern.ProviderError.ProviderMessages. This is observed behavior, which
// Cloudflare may change.
//
// Only text and JSON state is supported, for every model: image, audio and video
// inputs are not.
//
// # Limits
//
// A request may have at most MaxQuestions (64) questions, and its encoded body
// may be at most MaxRequestBytes (13 MiB), whichever model is selected; the body
// limit is documented for clef and clef-flash. Prepare checks both before any call
// and reports a violation as discern.ErrUnsupported, so nothing is sent; in a
// dual run, neither provider is called. Question ID syntax and the option and
// level counts are checked by package discern.
//
// Prepare makes one more check of its own: a request whose shortest possible
// response is larger than the WithMaxResponseBytes limit (4 MiB by default), such
// as 64 Choice questions with 255 option IDs of 256 bytes each, also fails with
// discern.ErrUnsupported, because every answer to it would be billed and then
// fail as too large.
//
// # Calls and errors
//
// Each call makes exactly one POST attempt, bounded by WithTimeout including the
// full body read. There are no retries, and redirects are never followed: a 3xx
// status is a rejection, and the token is never sent to another URL. Retry hints
// such as retry-after are reported in discern.ProviderError, never acted on; a
// fractional retry-after-ms is rounded up to a whole millisecond, and a refusal
// keeps its hint even when its body is too large or cannot be read.
//
// A non-2xx status matches discern.ErrRejected, with Cloudflare's errors[].code
// values in ProviderError.Codes. That body is read best-effort and never fails
// the call a second time: an envelope missing "success" still yields its codes,
// and errors[] entries of an unexpected shape, or a body that is not JSON at
// all, simply contribute none. A 2xx envelope with success:false, or with any
// errors[] entry, is also a rejection: Cloudflare does not document whether it
// sends one, so it is never read as an answer. success is read first, so a 2xx
// envelope with success:false is a rejection whatever its errors[] entries
// hold. Cloudflare's errors[].message texts may echo request content, so they
// are available only through ProviderError.ProviderMessages, never in error
// strings. At most 64 codes and 64 messages are kept, each message cut to
// 1 KiB. The envelope's messages[] array is informational and ignored. A 2xx
// envelope without "success", without a result, or with a result that fails
// strict decoding matches discern.ErrMalformedResponse.
//
// # Request IDs and neurons
//
// Observation.RequestID comes from the cf-ai-req-id response header,
// Observation.RayID from cf-ray, and Usage.Neurons (Cloudflare's billing unit)
// from cf-ai-neurons. These headers are observed in practice but not documented,
// so each is best-effort: an absent or unparseable value, including a request or
// ray ID that is not 1 to 256 bytes of visible ASCII, is left empty, never
// guessed. The model the result reports, Observation.Model, must likewise be at
// most 256 bytes of visible ASCII, or the response is malformed.
//
// # Testing and transports
//
// WithTestEndpoint points a Client at a loopback https server, such as one from
// httptest.NewTLSServer; it accepts only 127.0.0.0/8 and [::1] and is meant for
// tests. WithHTTPClient injects an HTTP client, which is copied with redirects
// and the cookie jar disabled. An injected transport that retries or follows
// redirects on its own is outside this package's guarantees.
package clef

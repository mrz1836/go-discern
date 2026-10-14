package discern

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors. Match them with errors.Is.
//
// ErrInvalidConfig, ErrInvalidRequest, ErrInvalidPolicy and ErrUnsupported are
// local: an error matching one of them means no provider call was made. The error
// of a call that was made never matches them, nor ErrInvalidRecord, and matches
// ErrPartialFailure only as Evaluate reports it: an adapter call error that
// matches one of these sentinels is recorded with a static cause instead.
var (
	// ErrInvalidConfig reports an invalid constructor argument or option.
	ErrInvalidConfig = errors.New("discern: invalid configuration")

	// ErrInvalidRequest reports a request that fails local validation.
	ErrInvalidRequest = errors.New("discern: invalid request")

	// ErrInvalidPolicy reports a recommendation policy that fails validation.
	ErrInvalidPolicy = errors.New("discern: invalid policy")

	// ErrUnsupported reports a request that a selected provider cannot accept,
	// such as more questions than the provider allows.
	ErrUnsupported = errors.New("discern: unsupported by provider")

	// ErrRejected reports a provider that refused the call: a non-2xx HTTP
	// status (including a redirect), or a Cloudflare envelope with success:false
	// or any errors[] entry.
	ErrRejected = errors.New("discern: rejected by provider")

	// ErrMalformedResponse reports a provider response that could not be decoded
	// or failed validation, including one larger than the configured limit and
	// one whose model, request ID or ray ID is not a provider identifier (see
	// Observation).
	ErrMalformedResponse = errors.New("discern: malformed provider response")

	// ErrPartialFailure reports a dual run in which exactly one provider failed.
	// The Result still holds the surviving provider's observation.
	ErrPartialFailure = errors.New("discern: partial failure")

	// ErrInvalidRecord reports a stored result record that ParseResult rejects.
	ErrInvalidRecord = errors.New("discern: invalid result record")
)

// Static causes that replace an adapter's error where keeping it would be wrong.
var (
	// errNilProviderError is the cause recorded when an adapter returns a nil
	// *ProviderError as a non-nil error.
	errNilProviderError = errors.New("discern: adapter returned a nil *ProviderError")

	// errCallLocalSentinel replaces a call error that matches a local sentinel,
	// such as ErrInvalidRequest: a call was made, so the result must not claim
	// otherwise.
	errCallLocalSentinel = errors.New("discern: adapter call failed with an error reserved for local validation")

	// errUninspectableCause replaces a ProviderError cause that panics when
	// errors.Is inspects it, as one holding a nil *ProviderError does.
	errUninspectableCause = errors.New("discern: adapter error cause could not be inspected")
)

// FailureKind classifies a provider failure. The list is closed; parsers of stored
// records should treat an unknown kind as a failure of unknown cause.
type FailureKind string

// Failure kinds.
const (
	// FailureTimeout means the call's own timeout expired, such as the adapter's
	// timeout or its HTTP client's, rather than the caller's deadline.
	FailureTimeout FailureKind = "timeout"

	// FailureDeadlineExceeded means the caller's context deadline expired.
	FailureDeadlineExceeded FailureKind = "deadline_exceeded"

	// FailureCanceled means the caller's context was canceled.
	FailureCanceled FailureKind = "canceled"

	// FailureTransport means the request failed in transit (DNS, TLS, connection
	// reset, or an incomplete body). The provider may or may not have run it.
	FailureTransport FailureKind = "transport"

	// FailureRejected means the provider refused the call: a non-2xx HTTP status
	// (including a redirect), or a Cloudflare envelope with success:false or any
	// errors[] entry. It matches ErrRejected.
	FailureRejected FailureKind = "rejected"

	// FailureMalformed means the response failed decoding or validation. It matches
	// ErrMalformedResponse.
	FailureMalformed FailureKind = "malformed_response"

	// FailureTooLarge means the response exceeded the configured size limit. It
	// matches ErrMalformedResponse.
	FailureTooLarge FailureKind = "response_too_large"

	// FailureAdapter means an adapter returned an error that is neither a
	// *ProviderError nor a ProviderError value, such as a test fake's error, or a
	// ProviderError without a kind.
	FailureAdapter FailureKind = "adapter_error"
)

// ValidationError describes a local validation failure. Err is the matching
// sentinel (ErrInvalidConfig, ErrInvalidRequest, ErrInvalidPolicy, ErrUnsupported
// or ErrInvalidRecord). Field is a path such as "questions[1].options[0].id"; it
// uses indexes, not caller values. Reason never contains caller data.
type ValidationError struct {
	Err    error
	Field  string
	Reason string
}

// Error returns the sentinel message followed by the field and reason.
func (e ValidationError) Error() string {
	var b strings.Builder
	if e.Err != nil {
		b.WriteString(e.Err.Error())
	} else {
		b.WriteString("discern: validation failed")
	}
	if e.Field != "" {
		b.WriteString(": ")
		b.WriteString(e.Field)
	}
	if e.Reason != "" {
		b.WriteString(": ")
		b.WriteString(e.Reason)
	}
	return b.String()
}

// Unwrap returns the sentinel.
func (e ValidationError) Unwrap() error { return e.Err }

// ProviderError describes a failed provider call. Its Error string is built only
// from the provider, kind and HTTP status: it never contains credentials, URLs,
// headers, request state or response bodies. Tools that walk error chains, such
// as testify's failure messages, also print the cause returned by Unwrap. The jev
// and clef adapters store only sanitized causes, which never include credentials,
// URLs, payloads or provider messages; a custom adapter's cause is its own.
//
// Adapters may return a ProviderError as a pointer or as a value. Request and ray
// IDs that are not provider identifiers (see Observation) are dropped when the
// error is recorded.
//
// RetryAfter is a provider hint, not proof that repeating the call is safe or
// free. HasRetryAfter distinguishes an explicit zero from an absent hint.
type ProviderError struct {
	Provider      Provider
	Kind          FailureKind
	StatusCode    int
	RequestID     string
	RayID         string
	RetryAfter    time.Duration
	HasRetryAfter bool
	Codes         []int

	// private holds the cause and the provider messages. A func value is printed
	// as an address by every fmt verb and by reflection-based dumpers, so neither
	// can leak even through %t, %d or a struct that embeds the error.
	private func() providerDetail
}

// providerDetail is the part of a ProviderError that is never printed.
type providerDetail struct {
	cause    error
	messages []string
}

// NewProviderError returns a ProviderError for adapters. The cause is reachable
// through errors.Is and errors.As but never appears in Error; tools that walk
// error chains print it, so it must not carry secrets, URLs or payloads (store a
// *url.Error's inner error, not the *url.Error).
// The messages are provider-supplied texts, such as Cloudflare errors[].message,
// and are available only through ProviderMessages.
func NewProviderError(provider Provider, kind FailureKind, cause error, messages ...string) *ProviderError {
	d := providerDetail{cause: cause, messages: slices.Clone(messages)}
	return &ProviderError{
		Provider: provider,
		Kind:     kind,
		private:  func() providerDetail { return d },
	}
}

// Error returns "discern: <provider>: <kind>", with the HTTP status when known.
func (e ProviderError) Error() string {
	var b strings.Builder
	b.WriteString("discern: ")
	b.WriteString(string(e.Provider))
	b.WriteString(": ")
	b.WriteString(string(e.Kind))
	if e.StatusCode != 0 {
		b.WriteString(" (HTTP ")
		b.WriteString(strconv.Itoa(e.StatusCode))
		b.WriteString(")")
	}
	return b.String()
}

// Unwrap returns the sanitized cause, such as context.Canceled.
func (e ProviderError) Unwrap() error { return e.detail().cause }

// Is maps the failure kind onto the package sentinels.
func (e ProviderError) Is(target error) bool {
	switch e.Kind {
	case FailureRejected:
		return target == ErrRejected
	case FailureMalformed, FailureTooLarge:
		return target == ErrMalformedResponse
	case FailureTimeout, FailureDeadlineExceeded, FailureCanceled, FailureTransport, FailureAdapter:
		return false
	}
	return false
}

// GoString prints the metadata fields only; the cause and provider messages are
// omitted so that %#v cannot leak them.
func (e ProviderError) GoString() string {
	return fmt.Sprintf("&discern.ProviderError{Provider:%q, Kind:%q, StatusCode:%d, RequestID:%q, RayID:%q, RetryAfter:%d, HasRetryAfter:%t, Codes:%#v}",
		e.Provider, e.Kind, e.StatusCode, e.RequestID, e.RayID, int64(e.RetryAfter), e.HasRetryAfter, e.Codes)
}

// ProviderMessages returns provider-supplied error texts, such as Cloudflare
// errors[].message. They may echo request content, so they are excluded from
// Error, GoString and result records; logging them is the caller's decision.
func (e ProviderError) ProviderMessages() []string { return slices.Clone(e.detail().messages) }

func (e ProviderError) detail() providerDetail {
	if e.private == nil {
		return providerDetail{}
	}
	return e.private()
}

// failure converts the error to its storage-safe form. Empty codes are stored as
// nil, as a parsed record would hold them, since the JSON field omits both.
func (e ProviderError) failure() *Failure {
	f := &Failure{
		Kind:       e.Kind,
		StatusCode: e.StatusCode,
		RequestID:  e.RequestID,
		RayID:      e.RayID,
	}
	if len(e.Codes) > 0 {
		f.Codes = slices.Clone(e.Codes)
	}
	if e.HasRetryAfter {
		f.RetryAfter = new(e.RetryAfter)
	}
	return f
}

func invalid(sentinel error, field, reason string) error {
	return &ValidationError{Err: sentinel, Field: field, Reason: reason}
}

package discern

import "context"

// Call performs one prepared provider call. It must honor ctx, make at most one
// attempt, and return either an Observation or an error (preferably a
// *ProviderError). A call error that matches a local sentinel, such as
// ErrInvalidRequest, is recorded with a static cause, because those sentinels
// mean that no call was made.
type Call func(ctx context.Context) (*Observation, error)

// Adapter is the provider boundary. The jev and clef packages implement it, and
// discerntest provides fakes.
//
// Prepare validates the request against the provider's documented limits and
// encodes the exact bytes to send, without any I/O. It returns an error matching
// ErrInvalidRequest or ErrUnsupported when the request cannot be sent. Such errors
// are returned to callers unchanged, so they must not contain caller data (a
// *ValidationError, which names fields rather than values, fits). Any other
// Prepare error is reported as ErrUnsupported with a fixed message that names
// only the provider; the adapter's error stays reachable through errors.Is and
// errors.As. The request is the adapter's own copy, so the returned Call may keep
// it.
//
// New calls Provider and Model to check the adapter; one for which either panics,
// such as a typed nil pointer, is rejected with ErrInvalidConfig. Adapters must
// be safe for concurrent use.
type Adapter interface {
	Provider() Provider
	Model() string
	Prepare(req *Request) (Call, error)
}

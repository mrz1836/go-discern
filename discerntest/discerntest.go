// Package discerntest provides fake adapters for testing code that uses discern,
// without network access or provider credentials.
//
//	fake := discerntest.Static(discern.Jev, discern.Observation{Model: "jev-test", Answers: answers})
//	ev, err := discern.New(discern.WithAdapter(fake))
//	// ... exercise application code ...
//	calls := fake.Calls()
//
// The evaluator validates fake observations exactly like real ones, so answers
// must match the request's questions and options, and the model, request ID and
// ray ID must be empty or 1 to 256 bytes of visible ASCII. A fake observation that
// fails validation is reported as a FailureMalformed (malformed_response)
// *discern.ProviderError; errors.Unwrap on that ProviderError returns the reason,
// such as "answers do not match the request: choice is not one of the options".
//
// A nil *Fake is rejected by discern.New with an error matching
// discern.ErrInvalidConfig.
package discerntest

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/mrz1836/go-discern"
)

// errFailing is the default error of a Failing fake.
var errFailing = errors.New("discerntest: failing adapter")

// DefaultModel is the model a fake reports when none is given.
const DefaultModel = "fake"

// Fake is a discern.Adapter that never touches the network. It counts the calls
// made through it and is safe for concurrent use.
type Fake struct {
	provider discern.Provider
	model    string
	fn       func(ctx context.Context, req *discern.Request) (*discern.Observation, error)
	calls    atomic.Int64
}

// Static returns a fake that answers every call with a copy of obs. Its model is
// obs.Model, or DefaultModel when that is empty.
func Static(provider discern.Provider, obs discern.Observation) *Fake {
	snapshot := obs.Clone()
	return Func(provider, obs.Model, func(context.Context, *discern.Request) (*discern.Observation, error) {
		c := snapshot.Clone()
		return &c, nil
	})
}

// Func returns a fake that calls fn for every call. fn receives the caller's
// context and the fake's copy of the request; it runs on the evaluator's
// goroutines, so it must be safe for concurrent use.
func Func(provider discern.Provider, model string, fn func(ctx context.Context, req *discern.Request) (*discern.Observation, error)) *Fake {
	if model == "" {
		model = DefaultModel
	}
	return &Fake{provider: provider, model: model, fn: fn}
}

// Failing returns a fake whose every call fails with err. A nil err fails with a
// transport *discern.ProviderError.
func Failing(provider discern.Provider, err error) *Fake {
	if err == nil {
		err = discern.NewProviderError(provider, discern.FailureTransport, errFailing)
	}
	return Func(provider, DefaultModel, func(context.Context, *discern.Request) (*discern.Observation, error) {
		return nil, err
	})
}

// Provider returns the provider the fake stands in for.
func (f *Fake) Provider() discern.Provider { return f.provider }

// Model returns the fake's model name.
func (f *Fake) Model() string { return f.model }

// Prepare returns a call that counts itself and runs the fake's function.
func (f *Fake) Prepare(req *discern.Request) (discern.Call, error) {
	return func(ctx context.Context) (*discern.Observation, error) {
		f.calls.Add(1)
		return f.fn(ctx, req)
	}, nil
}

// Calls returns the number of calls made so far.
func (f *Fake) Calls() int { return int(f.calls.Load()) }

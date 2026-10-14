package testutil

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
)

// Evaluator returns an evaluator built with opts, failing tb when New fails.
func Evaluator(tb testing.TB, opts ...discern.Option) *discern.Evaluator {
	tb.Helper()
	ev, err := discern.New(opts...)
	require.NoError(tb, err)
	return ev
}

// Evaluate runs req once through a new evaluator over the adapter alone.
func Evaluate(tb testing.TB, a discern.Adapter, req discern.Request) (*discern.Result, error) {
	tb.Helper()
	return Evaluator(tb, discern.WithAdapter(a)).Evaluate(tb.Context(), req)
}

// ProviderError returns the *discern.ProviderError in err's chain, failing tb
// when there is none.
func ProviderError(tb testing.TB, err error) *discern.ProviderError {
	tb.Helper()
	var pe *discern.ProviderError
	require.ErrorAs(tb, err, &pe)
	return pe
}

// ValidationError returns the *discern.ValidationError in err's chain, failing
// tb when there is none.
func ValidationError(tb testing.TB, err error) *discern.ValidationError {
	tb.Helper()
	var ve *discern.ValidationError
	require.ErrorAs(tb, err, &ve)
	return ve
}

// Failure returns the failure recorded in a failed single-provider result,
// failing tb unless res failed with exactly one outcome.
func Failure(tb testing.TB, res *discern.Result) *discern.Failure {
	tb.Helper()
	require.NotNil(tb, res)
	require.Equal(tb, discern.Failed, res.Status)
	require.Len(tb, res.Outcomes, 1)
	require.NotNil(tb, res.Outcomes[0].Failure)
	return res.Outcomes[0].Failure
}

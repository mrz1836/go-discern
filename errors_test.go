package discern_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/testutil"
)

func TestProviderErrorFormatting(t *testing.T) {
	pe := discern.NewProviderError(discern.Clef, discern.FailureRejected, errFakeTransport, "Invalid input "+stateSentinel)
	pe.StatusCode, pe.RequestID, pe.Codes = 400, "req-1", []int{5004}
	assert.Equal(t, "discern: clef: rejected (HTTP 400)", pe.Error())
	require.ErrorIs(t, pe, discern.ErrRejected)
	require.ErrorIs(t, pe, errFakeTransport)
	require.NotErrorIs(t, pe, discern.ErrMalformedResponse)
	assert.Equal(t, []string{"Invalid input " + stateSentinel}, pe.ProviderMessages())

	// Bad verbs make fmt fall back to reflection, which dereferences pointers and
	// prints unexported fields; nothing private may surface through any of them,
	// in plain text or in hex, however the error is held.
	type holder struct {
		err  error
		list []*discern.ProviderError
	}
	h := holder{err: pe, list: []*discern.ProviderError{pe}}
	shapes := append(testutil.Shapes(pe),
		testutil.Shape{Name: "error field", Value: h},
		testutil.Shape{Name: "pointer to error field", Value: &h},
		testutil.Shape{Name: "slice of errors", Value: []any{pe}},
		testutil.Shape{Name: "map of errors", Value: map[string]any{"e": pe}},
		testutil.Shape{Name: "joined", Value: errors.Join(pe)},
	)
	for _, shape := range shapes {
		testutil.AssertNeverPrints(t, shape.Value, stateSentinel, errFakeTransport.Error())
	}

	tooLarge := discern.NewProviderError(discern.Jev, discern.FailureTooLarge, nil)
	require.ErrorIs(t, tooLarge, discern.ErrMalformedResponse)
	assert.Equal(t, "discern: jev: response_too_large", tooLarge.Error())

	ve := &discern.ValidationError{Err: discern.ErrInvalidRequest, Field: "questions[0].id", Reason: "duplicate question ID"}
	assert.Equal(t, "discern: invalid request: questions[0].id: duplicate question ID", ve.Error())
	require.ErrorIs(t, ve, discern.ErrInvalidRequest)
	assert.Equal(t, "discern: validation failed", discern.ValidationError{}.Error())
	assert.Equal(t, "discern: validation failed: state", discern.ValidationError{Field: "state"}.Error())

	// A zero ProviderError and an unknown kind match no sentinel and hold nothing.
	var zero discern.ProviderError
	assert.Equal(t, "discern: : ", zero.Error())
	require.NoError(t, zero.Unwrap())
	assert.Nil(t, zero.ProviderMessages())
	unknown := discern.NewProviderError(discern.Jev, "quota", nil)
	for _, target := range []error{discern.ErrRejected, discern.ErrMalformedResponse, discern.ErrPartialFailure} {
		require.NotErrorIs(t, unknown, target)
	}

	// Messages are copied in and out.
	msgs := []string{"first"}
	withMsgs := discern.NewProviderError(discern.Clef, discern.FailureRejected, nil, msgs...)
	msgs[0] = mutated
	got := withMsgs.ProviderMessages()
	got[0] = mutated
	assert.Equal(t, []string{"first"}, withMsgs.ProviderMessages())
}

package discern_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
)

func TestEvaluatePartialFailure(t *testing.T) {
	jev, _ := staticPair()
	clef := discerntest.Failing(discern.Clef, nil)
	ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))

	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevAndClef))
	require.ErrorIs(t, err, discern.ErrPartialFailure)
	require.NotNil(t, res)
	pe := testutil.ProviderError(t, err)
	assert.Equal(t, discern.Clef, pe.Provider)
	assert.Equal(t, discern.FailureTransport, pe.Kind)

	assert.Equal(t, discern.Partial, res.Status)
	_, ok := res.Observation(discern.Jev)
	assert.True(t, ok)
	_, ok = res.Observation(discern.Clef)
	assert.False(t, ok)
	o, ok := res.Outcome(discern.Clef)
	require.True(t, ok)
	assert.Equal(t, discern.OutcomeFailed, o.Status)
	require.NotNil(t, o.Failure)
	assert.Equal(t, discern.FailureTransport, o.Failure.Kind)
	assert.Equal(t, 1, jev.Calls())
	assert.Equal(t, 1, clef.Calls())
}

func TestEvaluateBothFail(t *testing.T) {
	jev := discerntest.Failing(discern.Jev, discern.NewProviderError(discern.Jev, discern.FailureRejected, nil))
	clef := discerntest.Failing(discern.Clef, nil)
	ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))

	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevAndClef))
	require.Error(t, err)
	require.NotErrorIs(t, err, discern.ErrPartialFailure)
	require.ErrorIs(t, err, discern.ErrRejected)
	require.NotNil(t, res)
	assert.Equal(t, discern.Failed, res.Status)
	require.Len(t, res.Outcomes, 2)
	require.NotNil(t, res.Outcomes[0].Failure)
	require.NotNil(t, res.Outcomes[1].Failure)
	assert.Equal(t, discern.FailureRejected, res.Outcomes[0].Failure.Kind)
	assert.Equal(t, discern.FailureTransport, res.Outcomes[1].Failure.Kind)
	assert.Contains(t, err.Error(), "discern: jev: rejected")
	assert.Contains(t, err.Error(), "discern: clef: transport")
}

func TestEvaluateSingleFailureReturnsProviderError(t *testing.T) {
	pe := discern.NewProviderError(discern.Jev, discern.FailureRejected, nil)
	pe.StatusCode, pe.RequestID, pe.RetryAfter, pe.HasRetryAfter = 429, "req-9", 2*time.Second, true
	jev := discerntest.Failing(discern.Jev, pe)
	ev := testutil.Evaluator(t, discern.WithAdapter(jev))

	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly))
	got := testutil.ProviderError(t, err)
	require.ErrorIs(t, err, discern.ErrRejected)
	assert.Equal(t, 429, got.StatusCode)
	f := testutil.Failure(t, res)
	assert.Equal(t, &discern.Failure{Kind: discern.FailureRejected, StatusCode: 429, RequestID: "req-9", RetryAfter: new(2 * time.Second)}, f)
}

func TestEvaluateClassifiesCallErrors(t *testing.T) {
	var nilProviderError *discern.ProviderError
	tests := []struct {
		name         string
		cancelFirst  bool // the fake cancels the caller's context before returning
		err          error
		kind         discern.FailureKind
		is           error
		notIs        error
		hiddenCauses []string // texts that must not appear in the error string
	}{
		{name: "caller cancel", cancelFirst: true, err: context.Canceled, kind: discern.FailureCanceled, is: context.Canceled},
		{
			name: "adapter error after caller cancel reports the cancellation", cancelFirst: true, err: errFakeAdapter,
			kind: discern.FailureCanceled, is: context.Canceled, notIs: errFakeAdapter, hiddenCauses: []string{errFakeAdapter.Error()},
		},
		{name: "adapter timer", err: fmt.Errorf("adapter: %w", context.DeadlineExceeded), kind: discern.FailureTimeout, is: context.DeadlineExceeded},
		{name: "adapter cancellation without caller cancel", err: fmt.Errorf("adapter: %w", context.Canceled), kind: discern.FailureCanceled, is: context.Canceled},
		{name: "plain adapter error keeps its cause", err: errFakeAdapter, kind: discern.FailureAdapter, is: errFakeAdapter, hiddenCauses: []string{errFakeAdapter.Error()}},
		{name: "provider error without kind", err: discern.NewProviderError(discern.Jev, "", errFakeAdapter), kind: discern.FailureAdapter, is: errFakeAdapter},
		{
			name: "provider error for another provider is attributed to the caller",
			err:  discern.NewProviderError(discern.Clef, discern.FailureRejected, nil), kind: discern.FailureRejected, is: discern.ErrRejected,
		},
		{name: "wrapped provider error", err: fmt.Errorf("adapter: %w", discern.NewProviderError(discern.Jev, discern.FailureMalformed, nil)), kind: discern.FailureMalformed, is: discern.ErrMalformedResponse},
		{name: "nil provider error in an error interface", err: nilProviderError, kind: discern.FailureAdapter, notIs: discern.ErrRejected},
		{name: "wrapped nil provider error", err: fmt.Errorf("adapter: %w", nilProviderError), kind: discern.FailureAdapter, notIs: discern.ErrRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			jev := discerntest.Func(discern.Jev, "", func(context.Context, *discern.Request) (*discern.Observation, error) {
				if tt.cancelFirst {
					cancel()
				}
				return nil, tt.err
			})
			ev := testutil.Evaluator(t, discern.WithAdapter(jev))
			var (
				res *discern.Result
				err error
			)
			require.NotPanics(t, func() { res, err = ev.Evaluate(ctx, ticketRequest(discern.JevOnly)) })
			if tt.is != nil {
				require.ErrorIs(t, err, tt.is)
			}
			if tt.notIs != nil {
				require.NotErrorIs(t, err, tt.notIs)
			}
			pe := testutil.ProviderError(t, err)
			require.NotNil(t, pe)
			assert.Equal(t, discern.Jev, pe.Provider)
			assert.Equal(t, tt.kind, pe.Kind)
			assert.Equal(t, "discern: jev: "+string(tt.kind), err.Error())
			assert.Equal(t, tt.kind, testutil.Failure(t, res).Kind)
			for _, hidden := range tt.hiddenCauses {
				assert.NotContains(t, fmt.Sprintf("%v %+v %#v", err, err, err), hidden)
			}
		})
	}
}

func TestEvaluateCallErrorsNeverMatchLocalSentinels(t *testing.T) {
	// A local sentinel promises that no call was made, so an adapter error that
	// matches one is recorded with a static cause instead.
	locals := []error{
		discern.ErrInvalidConfig, discern.ErrInvalidRequest, discern.ErrInvalidPolicy,
		discern.ErrUnsupported, discern.ErrInvalidRecord, discern.ErrPartialFailure,
	}
	for _, sentinel := range locals {
		wrapped := fmt.Errorf("x: %w", sentinel)
		tests := []struct {
			name string
			err  error
			kind discern.FailureKind
			is   error // a match that must survive the sanitizing, if any
		}{
			{"plain error", wrapped, discern.FailureAdapter, nil},
			{"provider error cause", discern.NewProviderError(discern.Jev, discern.FailureRejected, wrapped), discern.FailureRejected, discern.ErrRejected},
			{"adapter timeout", errors.Join(context.DeadlineExceeded, wrapped), discern.FailureTimeout, context.DeadlineExceeded},
			{"adapter cancellation", errors.Join(context.Canceled, wrapped), discern.FailureCanceled, context.Canceled},
		}
		for _, tt := range tests {
			t.Run(sentinel.Error()+" in "+tt.name, func(t *testing.T) {
				jev := discerntest.Failing(discern.Jev, tt.err)
				ev := testutil.Evaluator(t, discern.WithAdapter(jev))
				res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly))
				require.Error(t, err)
				require.NotNil(t, res, "a call was made")
				assert.Equal(t, 1, jev.Calls())
				for _, local := range locals {
					require.NotErrorIs(t, err, local)
				}
				if tt.is != nil {
					require.ErrorIs(t, err, tt.is)
				}
				assert.Equal(t, tt.kind, testutil.Failure(t, res).Kind)
			})
		}
	}

	// A caller cancellation keeps the context's error as the cause.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	jev := discerntest.Func(discern.Jev, "", func(context.Context, *discern.Request) (*discern.Observation, error) {
		cancel()
		return nil, errors.Join(context.Canceled, discern.ErrInvalidRequest)
	})
	res, err := testutil.Evaluator(t, discern.WithAdapter(jev)).Evaluate(ctx, ticketRequest(discern.JevOnly))
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, discern.ErrInvalidRequest)
	assert.Equal(t, discern.FailureCanceled, testutil.Failure(t, res).Kind)
}

func TestEvaluateAcceptsProviderErrorValues(t *testing.T) {
	value := discern.ProviderError{
		Provider: discern.Jev, Kind: discern.FailureRejected, StatusCode: 429,
		RequestID: "req-9", RetryAfter: 2 * time.Second, HasRetryAfter: true, Codes: []int{3040},
	}
	tests := []struct {
		name string
		err  error
	}{
		{"value", value},
		{"wrapped value", fmt.Errorf("adapter: %w", value)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := testutil.Evaluator(t, discern.WithAdapter(discerntest.Failing(discern.Jev, tt.err)))
			res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly))
			require.ErrorIs(t, err, discern.ErrRejected)
			pe := testutil.ProviderError(t, err)
			assert.Equal(t, discern.FailureRejected, pe.Kind)
			assert.Equal(t, 429, pe.StatusCode)
			assert.Equal(t, "discern: jev: rejected (HTTP 429)", err.Error())
			assert.Equal(t, &discern.Failure{
				Kind: discern.FailureRejected, StatusCode: 429, RequestID: "req-9", RetryAfter: new(2 * time.Second), Codes: []int{3040},
			}, testutil.Failure(t, res))
		})
	}
}

func TestEvaluateProviderErrorWithNilProviderErrorCause(t *testing.T) {
	// The cause's value-receiver methods would panic in errors.Is, so it is
	// replaced and the returned error can be inspected safely.
	var nilProviderError *discern.ProviderError
	pe := discern.NewProviderError(discern.Jev, discern.FailureRejected, fmt.Errorf("adapter: %w", nilProviderError))
	ev := testutil.Evaluator(t, discern.WithAdapter(discerntest.Failing(discern.Jev, pe)))
	var (
		res *discern.Result
		err error
	)
	require.NotPanics(t, func() { res, err = ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly)) })
	require.NotPanics(t, func() { _ = errors.Is(err, context.Canceled) })
	require.ErrorIs(t, err, discern.ErrRejected)
	assert.Equal(t, discern.FailureRejected, testutil.Failure(t, res).Kind)
}

func TestEvaluateDropsInvalidProviderErrorIDs(t *testing.T) {
	pe := discern.NewProviderError(discern.Jev, discern.FailureRejected, nil)
	pe.RequestID, pe.RayID = "req-1\nlevel=error msg=forged", strings.Repeat("r", 257)
	ev := testutil.Evaluator(t, discern.WithAdapter(discerntest.Failing(discern.Jev, pe)))
	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly))
	got := testutil.ProviderError(t, err)
	assert.Empty(t, got.RequestID)
	assert.Empty(t, got.RayID)
	assert.Equal(t, &discern.Failure{Kind: discern.FailureRejected}, testutil.Failure(t, res))
	assert.Equal(t, "req-1\nlevel=error msg=forged", pe.RequestID, "the adapter's error is not modified")
	_, err = discern.ParseResult([]byte(mustJSON(t, res)))
	require.NoError(t, err, "the record stays valid")
}

func TestEvaluateCallerErrorIsNotAliased(t *testing.T) {
	pe := discern.NewProviderError(discern.Clef, "", errFakeAdapter)
	pe.Codes = []int{7}
	jev := discerntest.Failing(discern.Jev, pe)
	ev := testutil.Evaluator(t, discern.WithAdapter(jev))
	_, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly))
	got := testutil.ProviderError(t, err)
	assert.NotSame(t, pe, got)
	got.Codes[0] = 99

	// The adapter's error value is never modified, so a shared error stays intact.
	assert.Equal(t, discern.Clef, pe.Provider)
	assert.Empty(t, pe.Kind)
	assert.Equal(t, []int{7}, pe.Codes)
}

func TestEvaluateCallerDeadlineIsDeadlineExceeded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		jev := discerntest.Func(discern.Jev, "", func(ctx context.Context, _ *discern.Request) (*discern.Observation, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
		ev := testutil.Evaluator(t, discern.WithAdapter(jev))
		ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
		defer cancel()
		res, err := ev.Evaluate(ctx, ticketRequest(discern.JevOnly))
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Equal(t, discern.FailureDeadlineExceeded, testutil.Failure(t, res).Kind)
		assert.Equal(t, 8*time.Second, res.Elapsed)
	})
}

func TestEvaluateOneProviderTimesOutOtherSucceeds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		jev, _ := staticPair()
		clef := discerntest.Func(discern.Clef, "clef-flash", func(ctx context.Context, _ *discern.Request) (*discern.Observation, error) {
			timer := time.NewTimer(15 * time.Second)
			defer timer.Stop()
			select {
			case <-timer.C:
				return nil, discern.NewProviderError(discern.Clef, discern.FailureTimeout, context.DeadlineExceeded)
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
		ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))
		res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevAndClef))
		require.ErrorIs(t, err, discern.ErrPartialFailure)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Equal(t, discern.Partial, res.Status)
		require.Len(t, res.Outcomes, 2)
		require.NotNil(t, res.Outcomes[1].Failure)
		assert.Equal(t, discern.FailureTimeout, res.Outcomes[1].Failure.Kind)
		assert.Equal(t, 15*time.Second, res.Outcomes[1].Elapsed)
		assert.Equal(t, discern.OutcomeSucceeded, res.Outcomes[0].Status)
	})
}

func TestEvaluateBothCanceledWithoutLeaks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := func(ctx context.Context, _ *discern.Request) (*discern.Observation, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		jev := discerntest.Func(discern.Jev, "", block)
		clef := discerntest.Func(discern.Clef, "", block)
		ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))
		ctx, cancel := context.WithCancel(t.Context())
		var (
			res *discern.Result
			err error
		)
		done := make(chan struct{})
		go func() {
			defer close(done)
			res, err = ev.Evaluate(ctx, ticketRequest(discern.JevAndClef))
		}()
		synctest.Wait()
		cancel()
		<-done
		require.ErrorIs(t, err, context.Canceled)
		require.NotNil(t, res)
		assert.Equal(t, discern.Failed, res.Status)
		require.Len(t, res.Outcomes, 2)
		for _, o := range res.Outcomes {
			require.NotNil(t, o.Failure)
			assert.Equal(t, discern.FailureCanceled, o.Failure.Kind)
		}
	})
}

func TestEvaluateAdapterPanicRepanicsAfterBothFinish(t *testing.T) {
	for _, panicking := range []discern.Provider{discern.Jev, discern.Clef} {
		t.Run(string(panicking)+" panics", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var otherFinished atomic.Bool
				adapter := func(p discern.Provider, obs discern.Observation) *discerntest.Fake {
					return discerntest.Func(p, "", func(context.Context, *discern.Request) (*discern.Observation, error) {
						if p == panicking {
							panic(string(p) + " adapter bug")
						}
						timer := time.NewTimer(time.Second) // fake time inside the bubble
						defer timer.Stop()
						<-timer.C
						otherFinished.Store(true)
						return &obs, nil
					})
				}
				jev := adapter(discern.Jev, jevAnswers().observation())
				clef := adapter(discern.Clef, clefAnswers().observation())
				ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))
				assert.PanicsWithValue(t, string(panicking)+" adapter bug", func() {
					_, _ = ev.Evaluate(t.Context(), ticketRequest(discern.JevAndClef))
				})
				assert.True(t, otherFinished.Load(), "the panic is re-raised only after the other call returns")
				assert.Equal(t, 1, jev.Calls())
				assert.Equal(t, 1, clef.Calls())
			})
		})
	}
}

func TestEvaluateInvalidObservationIsMalformed(t *testing.T) {
	good := clefAnswers().observation()
	missing := good.Clone()
	missing.Answers = missing.Answers[:2]
	wrongKind := good.Clone()
	wrongKind.Answers[1].Kind = discern.KindChoice
	tests := []struct {
		name string
		obs  *discern.Observation
	}{
		{"nil observation", nil},
		{"missing answer", &missing},
		{"kind mismatch", &wrongKind},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clef := discerntest.Func(discern.Clef, "", func(context.Context, *discern.Request) (*discern.Observation, error) {
				return tt.obs, nil
			})
			ev := testutil.Evaluator(t, discern.WithAdapter(clef))
			res, err := ev.Evaluate(t.Context(), ticketRequest(discern.ClefOnly))
			require.ErrorIs(t, err, discern.ErrMalformedResponse)
			assert.Equal(t, discern.FailureMalformed, testutil.Failure(t, res).Kind)
		})
	}
}

func TestEvaluateRecordsNeverHoldPayloads(t *testing.T) {
	jev, _ := staticPair()
	clef := discerntest.Failing(discern.Clef, discern.NewProviderError(discern.Clef, discern.FailureRejected, errFakeTransport, "echo "+stateSentinel))
	ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))
	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevAndClef))
	require.ErrorIs(t, err, discern.ErrPartialFailure)
	data, mErr := json.Marshal(res)
	require.NoError(t, mErr)
	for _, s := range []string{stateSentinel, instructionSentinel, descriptionSentinel} {
		assert.NotContains(t, string(data), s)
		assert.NotContains(t, err.Error(), s)
		assert.NotContains(t, fmt.Sprintf("%v %+v %#v", err, err, err), s)
	}
	pe := testutil.ProviderError(t, err)
	assert.Equal(t, []string{"echo " + stateSentinel}, pe.ProviderMessages())
	assert.NotContains(t, pe.GoString(), stateSentinel)
}

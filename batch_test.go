package discern_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
)

func TestEvaluateBatchStableOrder(t *testing.T) {
	var calls atomic.Int64
	jev := discerntest.Func(discern.Jev, "", func(_ context.Context, req *discern.Request) (*discern.Observation, error) {
		calls.Add(1)
		obs := jevAnswers().observation()
		obs.RequestID = req.CorrelationID
		return &obs, nil
	})
	ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithBatchConcurrency(3))
	reqs := make([]discern.Request, 10)
	for i := range reqs {
		reqs[i] = ticketRequest(discern.JevOnly)
		reqs[i].CorrelationID = fmt.Sprintf("ticket-%d", i)
	}
	items, err := ev.EvaluateBatch(t.Context(), reqs)
	require.NoError(t, err)
	require.Len(t, items, 10)
	for i, it := range items {
		require.NoError(t, it.Err)
		assert.Equal(t, fmt.Sprintf("ticket-%d", i), it.Result.CorrelationID)
		assert.Equal(t, fmt.Sprintf("ticket-%d", i), it.Result.Outcomes[0].Observation.RequestID)
	}
	assert.Equal(t, int64(10), calls.Load())
}

func TestEvaluateBatchConcurrencyBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var inflight, peak atomic.Int64
		release := make(chan struct{})
		jev := discerntest.Func(discern.Jev, "", func(ctx context.Context, _ *discern.Request) (*discern.Observation, error) {
			n := inflight.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			defer inflight.Add(-1)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			obs := jevAnswers().observation()
			return &obs, nil
		})
		ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithBatchConcurrency(2))
		reqs := make([]discern.Request, 6)
		for i := range reqs {
			reqs[i] = ticketRequest(discern.JevOnly)
		}
		var (
			items []discern.BatchItem
			err   error
		)
		done := make(chan struct{})
		go func() {
			defer close(done)
			items, err = ev.EvaluateBatch(t.Context(), reqs)
		}()
		for range 3 {
			synctest.Wait()
			assert.Equal(t, int64(2), inflight.Load())
			release <- struct{}{}
			release <- struct{}{}
		}
		<-done
		require.NoError(t, err)
		assert.Equal(t, int64(2), peak.Load())
		require.Len(t, items, 6)
		for _, it := range items {
			assert.NoError(t, it.Err)
		}
	})
}

func TestEvaluateBatchDualItemsCountTwoCalls(t *testing.T) {
	jev, clef := staticPair()
	ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))
	reqs := []discern.Request{ticketRequest(discern.JevAndClef), ticketRequest(discern.JevOnly), ticketRequest(discern.JevAndClef)}
	items, err := ev.EvaluateBatch(t.Context(), reqs)
	require.NoError(t, err)
	require.Len(t, items, 3)
	assert.Equal(t, 3, jev.Calls())
	assert.Equal(t, 2, clef.Calls())
}

func TestEvaluateBatchFailureIsolation(t *testing.T) {
	jev := discerntest.Func(discern.Jev, "", func(_ context.Context, req *discern.Request) (*discern.Observation, error) {
		if req.CorrelationID == "bad" {
			return nil, discern.NewProviderError(discern.Jev, discern.FailureRejected, nil)
		}
		obs := jevAnswers().observation()
		return &obs, nil
	})
	ev := testutil.Evaluator(t, discern.WithAdapter(jev))
	reqs := []discern.Request{ticketRequest(discern.JevOnly), ticketRequest(discern.JevOnly), ticketRequest(discern.JevOnly)}
	reqs[1].CorrelationID = "bad"
	items, err := ev.EvaluateBatch(t.Context(), reqs)
	require.NoError(t, err)
	require.Len(t, items, 3)
	require.NoError(t, items[0].Err)
	require.ErrorIs(t, items[1].Err, discern.ErrRejected)
	require.NotNil(t, items[1].Result)
	assert.Equal(t, discern.Failed, items[1].Result.Status)
	require.NoError(t, items[2].Err)
	require.NotNil(t, items[2].Result)
	assert.Equal(t, discern.Complete, items[2].Result.Status)
}

func TestEvaluateBatchCancelLeavesUnstartedItemsUnsent(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	jev := discerntest.Func(discern.Jev, "", func(ctx context.Context, _ *discern.Request) (*discern.Observation, error) {
		cancel()
		return nil, ctx.Err()
	})
	ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithBatchConcurrency(1))
	reqs := []discern.Request{ticketRequest(discern.JevOnly), ticketRequest(discern.JevOnly), ticketRequest(discern.JevOnly)}
	items, err := ev.EvaluateBatch(ctx, reqs)
	require.NoError(t, err)
	require.Len(t, items, 3)
	require.NotNil(t, items[0].Result)
	require.ErrorIs(t, items[0].Err, context.Canceled)
	for _, it := range items[1:] {
		assert.Nil(t, it.Result)
		require.ErrorIs(t, it.Err, context.Canceled)
	}
	assert.Equal(t, 1, jev.Calls())
}

func TestEvaluateBatchCancelWithItemsInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		block := func(ctx context.Context, _ *discern.Request) (*discern.Observation, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		jev := discerntest.Func(discern.Jev, "", block)
		clef := discerntest.Func(discern.Clef, "", block)
		ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef), discern.WithBatchConcurrency(2))
		reqs := []discern.Request{
			ticketRequest(discern.JevAndClef), ticketRequest(discern.JevOnly),
			ticketRequest(discern.JevAndClef), ticketRequest(discern.ClefOnly), ticketRequest(discern.JevOnly),
		}
		ctx, cancel := context.WithCancel(t.Context())
		var (
			items []discern.BatchItem
			err   error
		)
		done := make(chan struct{})
		go func() {
			defer close(done)
			items, err = ev.EvaluateBatch(ctx, reqs)
		}()
		synctest.Wait() // items 0 and 1 are in flight: three calls
		assert.Equal(t, 2, jev.Calls())
		assert.Equal(t, 1, clef.Calls())
		cancel()
		<-done

		require.NoError(t, err)
		require.Len(t, items, len(reqs))
		for i, it := range items[:2] {
			require.ErrorIs(t, it.Err, context.Canceled, "item %d", i)
			require.NotNil(t, it.Result, "item %d was sent", i)
			assert.Equal(t, discern.Failed, it.Result.Status)
			for _, o := range it.Result.Outcomes {
				assert.Equal(t, discern.FailureCanceled, o.Failure.Kind)
			}
		}
		for i, it := range items[2:] {
			assert.Nil(t, it.Result, "item %d was not sent", i+2)
			require.ErrorIs(t, it.Err, context.Canceled)
		}
		assert.Equal(t, 2, jev.Calls(), "no call starts after the cancellation")
		assert.Equal(t, 1, clef.Calls())
	})
}

func TestEvaluateBatchAlreadyCanceledMakesNoCalls(t *testing.T) {
	jev, clef := staticPair()
	ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	items, err := ev.EvaluateBatch(ctx, []discern.Request{ticketRequest(discern.JevAndClef), ticketRequest(discern.JevOnly)})
	require.NoError(t, err)
	require.Len(t, items, 2)
	for _, it := range items {
		assert.Nil(t, it.Result)
		require.ErrorIs(t, it.Err, context.Canceled)
	}
	assert.Equal(t, 0, jev.Calls()+clef.Calls())
}

func TestEvaluateBatchInvalidItemMakesNoCalls(t *testing.T) {
	kindMismatch := discern.WithPolicy(policyFor(qOutage, check(discern.MetricNoul, discern.AtMost, 0.5)))
	noulAsChoice := ticketRequest(discern.JevOnly)
	noulAsChoice.Questions[1] = discern.Choice(qOutage, discern.Text("Outage?"), discern.Opt("yes", discern.Value{}), discern.Opt("no", discern.Value{}))
	tests := []struct {
		name string
		bad  discern.Request
		opts []discern.EvalOption
		want error
	}{
		{"invalid request", ticketRequest(""), nil, discern.ErrInvalidRequest},
		{"unregistered provider", ticketRequest(discern.JevAndClef), nil, discern.ErrUnsupported},
		{"policy does not fit the item", noulAsChoice, []discern.EvalOption{kindMismatch}, discern.ErrInvalidPolicy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jev, _ := staticPair()
			ev := testutil.Evaluator(t, discern.WithAdapter(jev))
			reqs := []discern.Request{ticketRequest(discern.JevOnly), ticketRequest(discern.JevOnly), tt.bad}
			items, err := ev.EvaluateBatch(t.Context(), reqs, tt.opts...)
			require.ErrorIs(t, err, tt.want)
			assert.Contains(t, err.Error(), "batch item 2")
			assert.Nil(t, items)
			assert.Equal(t, 0, jev.Calls())
		})
	}
}

func TestEvaluateBatchEmpty(t *testing.T) {
	jev, _ := staticPair()
	ev := testutil.Evaluator(t, discern.WithAdapter(jev))
	items, err := ev.EvaluateBatch(t.Context(), nil)
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestEvaluateBatchAppliesPolicyOption(t *testing.T) {
	jev, _ := staticPair()
	ev := testutil.Evaluator(t, discern.WithAdapter(jev))
	pol := discern.Policy{Version: "batch-v1", Questions: []discern.QuestionPolicy{{
		Question: qOutage, Checks: []discern.Check{{Metric: discern.MetricNoul, Op: discern.AtMost, Threshold: 0.1}},
	}}}
	items, err := ev.EvaluateBatch(t.Context(), []discern.Request{ticketRequest(discern.JevOnly), ticketRequest(discern.JevOnly)}, discern.WithPolicy(pol))
	require.NoError(t, err)
	for _, it := range items {
		rec, ok := it.Result.Recommendation(qOutage)
		require.True(t, ok)
		assert.Equal(t, discern.Accepted, rec.Status)
		assert.Equal(t, discern.PolicyCallOverride, it.Result.Policy.Source)
	}
}

func TestEvaluateBatchPanicStopsFeedingItems(t *testing.T) {
	tests := []struct {
		name        string
		concurrency int
		panicAll    bool // every call panics; otherwise only the first item's call
		minCalls    int
		maxCalls    int
	}{
		// With one worker, item 0 panics before any other item is started.
		{"one worker, first item panics", 1, false, 1, 1},
		// Each worker runs at most one item before the first panic is recorded.
		{"three workers, every item panics", 3, true, 1, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jev := discerntest.Func(discern.Jev, "", func(_ context.Context, req *discern.Request) (*discern.Observation, error) {
				if tt.panicAll || req.CorrelationID == "item-0" {
					panic("batch adapter bug")
				}
				obs := jevAnswers().observation()
				return &obs, nil
			})
			ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithBatchConcurrency(tt.concurrency))
			reqs := make([]discern.Request, 50)
			for i := range reqs {
				reqs[i] = ticketRequest(discern.JevOnly)
				reqs[i].CorrelationID = fmt.Sprintf("item-%d", i)
			}
			assert.PanicsWithValue(t, "batch adapter bug", func() {
				_, _ = ev.EvaluateBatch(t.Context(), reqs)
			})
			assert.GreaterOrEqual(t, jev.Calls(), tt.minCalls)
			assert.LessOrEqual(t, jev.Calls(), tt.maxCalls, "no item starts after a panic is recorded")
		})
	}
}

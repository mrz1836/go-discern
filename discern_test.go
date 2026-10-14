package discern_test

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
)

func TestEvaluateSingleProviderComplete(t *testing.T) {
	jev, clef := staticPair()
	ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))

	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.ClefOnly))
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.Equal(t, discern.SchemaVersion, res.SchemaVersion)
	assert.Equal(t, "github.com/mrz1836/go-discern", res.Library.Module)
	assert.NotEmpty(t, res.Library.Version)
	assert.Equal(t, discern.Complete, res.Status)
	assert.Equal(t, discern.ClefOnly, res.Providers)
	assert.Equal(t, "ticket-1042", res.CorrelationID)
	assert.Equal(t, []string{"kb:article-17"}, res.EvidenceRefs)
	assert.Equal(t, time.UTC, res.StartedAt.Location())
	require.Len(t, res.Outcomes, 1)
	assert.Equal(t, discern.Clef, res.Outcomes[0].Provider)
	assert.Equal(t, "clef", res.Outcomes[0].RequestedModel)
	assert.Equal(t, discern.OutcomeSucceeded, res.Outcomes[0].Status)
	assert.Equal(t, 0, jev.Calls())
	assert.Equal(t, 1, clef.Calls())

	obs, ok := res.Observation(discern.Clef)
	require.True(t, ok)
	_, ok = res.Observation(discern.Jev)
	assert.False(t, ok)

	q, ok := obs.Choice(qQueue)
	require.True(t, ok)
	assert.Equal(t, optBilling, q.Option)
	assert.Equal(t, []discern.OptionProbability{{Option: optBilling, Probability: 0.87}, {Option: optTechnical, Probability: 0.1}, {Option: "account", Probability: 0.03}}, q.Probabilities)
	require.NotNil(t, q.Confidence)
	assert.InDelta(t, 0.8, *q.Confidence, 0)

	n, ok := obs.Noul(qOutage)
	require.True(t, ok)
	assert.InDelta(t, 0.1, n.Value, 0)

	s, ok := obs.Score(qSeverity)
	require.True(t, ok)
	assert.InDelta(t, 1.1, s.Value, 0)
	assert.Equal(t, "degraded", s.Nearest())

	_, ok = obs.Choice(qSeverity)
	assert.False(t, ok)
	_, ok = obs.Score("missing")
	assert.False(t, ok)

	assert.Equal(t, discern.PolicyNone, res.Policy.Source)
	assert.Nil(t, res.Policy.Policy)
	require.Len(t, res.Recommendations, 3)
	for _, rec := range res.Recommendations {
		assert.Equal(t, discern.NoRule, rec.Status)
		assert.Equal(t, []discern.ReasonCode{discern.ReasonNoPolicy}, rec.Reasons)
		assert.Nil(t, rec.Selected)
	}
	assert.Equal(t, discern.Overall{Status: discern.NoRule, Uncovered: []string{qQueue, qOutage, qSeverity}}, res.Overall)
}

func TestEvaluatePerCallProviderSelection(t *testing.T) {
	tests := []struct {
		set       discern.ProviderSet
		jev, clef int
		outcomes  []discern.Provider
	}{
		{discern.JevOnly, 1, 0, []discern.Provider{discern.Jev}},
		{discern.ClefOnly, 0, 1, []discern.Provider{discern.Clef}},
		{discern.JevAndClef, 1, 1, []discern.Provider{discern.Jev, discern.Clef}},
	}
	for _, tt := range tests {
		t.Run(string(tt.set), func(t *testing.T) {
			jev, clef := staticPair()
			ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))
			res, err := ev.Evaluate(t.Context(), ticketRequest(tt.set))
			require.NoError(t, err)
			got := make([]discern.Provider, 0, len(res.Outcomes))
			for _, o := range res.Outcomes {
				got = append(got, o.Provider)
			}
			assert.Equal(t, tt.outcomes, got)
			assert.Equal(t, tt.outcomes, tt.set.Providers())
			assert.Equal(t, tt.jev, jev.Calls())
			assert.Equal(t, tt.clef, clef.Calls())
		})
	}
}

func TestProviderSetScenarios(t *testing.T) {
	tests := []struct {
		set       discern.ProviderSet
		providers []discern.Provider
	}{
		{discern.JevOnly, []discern.Provider{discern.Jev}},
		{discern.ClefOnly, []discern.Provider{discern.Clef}},
		{discern.JevAndClef, []discern.Provider{discern.Jev, discern.Clef}},
		{"", nil},
		{"clef+jev", nil},
	}
	for _, tt := range tests {
		t.Run("set "+string(tt.set), func(t *testing.T) {
			assert.Equal(t, tt.providers, tt.set.Providers())
			for _, p := range []discern.Provider{discern.Jev, discern.Clef, "other"} {
				assert.Equal(t, slices.Contains(tt.providers, p), tt.set.Has(p), "Has(%s)", p)
			}
		})
	}

	// Providers returns a copy, so callers cannot change the set's order.
	ps := discern.JevAndClef.Providers()
	ps[0] = "other"
	assert.Equal(t, []discern.Provider{discern.Jev, discern.Clef}, discern.JevAndClef.Providers())
}

func TestEvaluateDualStableOrderUnderReversedCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		releaseJev, releaseClef := make(chan struct{}), make(chan struct{})
		clefDone := make(chan struct{})
		jev := discerntest.Func(discern.Jev, "jev-1.13.0", blockingCall(releaseJev, jevAnswers().observation()))
		clefInner := blockingCall(releaseClef, clefAnswers().observation())
		clef := discerntest.Func(discern.Clef, "clef", func(ctx context.Context, req *discern.Request) (*discern.Observation, error) {
			defer close(clefDone)
			return clefInner(ctx, req)
		})
		ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))

		var (
			res *discern.Result
			err error
		)
		done := make(chan struct{})
		go func() {
			defer close(done)
			res, err = ev.Evaluate(t.Context(), ticketRequest(discern.JevAndClef))
		}()
		synctest.Wait() // both calls are in flight
		close(releaseClef)
		<-clefDone
		close(releaseJev)
		<-done

		require.NoError(t, err)
		require.Len(t, res.Outcomes, 2)
		assert.Equal(t, discern.Jev, res.Outcomes[0].Provider)
		assert.Equal(t, discern.Clef, res.Outcomes[1].Provider)
		assert.Equal(t, "jev-1.13.0", res.Outcomes[0].Observation.Model)
		assert.Equal(t, "clef", res.Outcomes[1].Observation.Model)
		assert.Equal(t, 1, jev.Calls())
		assert.Equal(t, 1, clef.Calls())
	})
}

func TestEvaluateAdaptersOwnTheirRequests(t *testing.T) {
	// Each fake rewrites its request after looking at it. With independent copies
	// nothing leaks into the other adapter or the result; with shared slices the
	// race detector reports the concurrent writes.
	type seen struct {
		option, evidence, correlation string
	}
	mutating := func(p discern.Provider, obs discern.Observation, got *seen) *discerntest.Fake {
		return discerntest.Func(p, "", func(_ context.Context, req *discern.Request) (*discern.Observation, error) {
			*got = seen{req.Questions[0].Options[0].ID, req.EvidenceRefs[0], req.CorrelationID}
			req.Questions[0].Options[0].ID = "mutated-by-" + string(p)
			req.Questions[2].Levels[0].ID = "mutated-by-" + string(p)
			req.EvidenceRefs[0] = "mutated-by-" + string(p)
			req.CorrelationID = "mutated-by-" + string(p)
			c := obs.Clone()
			return &c, nil
		})
	}
	var jevSaw, clefSaw seen
	jev := mutating(discern.Jev, jevAnswers().observation(), &jevSaw)
	clef := mutating(discern.Clef, clefAnswers().observation(), &clefSaw)
	ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))

	for range 3 {
		req := ticketRequest(discern.JevAndClef)
		res, err := ev.Evaluate(t.Context(), req)
		require.NoError(t, err)
		want := seen{optBilling, "kb:article-17", "ticket-1042"}
		assert.Equal(t, want, jevSaw)
		assert.Equal(t, want, clefSaw)
		assert.Equal(t, "ticket-1042", res.CorrelationID)
		assert.Equal(t, []string{"kb:article-17"}, res.EvidenceRefs)
		assert.Equal(t, []string{optBilling, optTechnical, "account"}, res.Questions[0].Options)
		assert.Equal(t, []string{levelCosmetic, "degraded", levelBlocked}, res.Questions[2].Levels)
		assert.Equal(t, optBilling, req.Questions[0].Options[0].ID, "the caller's request is never modified")
	}
}

func TestEvaluateRecordsAreIndependentOfLaterCalls(t *testing.T) {
	pol := policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.8))
	ev := pairEvaluator(t, discern.WithDefaultPolicy(pol))
	first, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevAndClef))
	require.NoError(t, err)
	before, err := json.Marshal(first)
	require.NoError(t, err)

	second, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevAndClef))
	require.NoError(t, err)
	require.Len(t, second.Outcomes, 2)
	require.NotEmpty(t, second.Recommendations)
	second.Questions[0].Options[0] = mutated
	second.Policy.Policy.Questions[0].Checks[0].Threshold = 0.1
	second.Recommendations[0].Reasons[0] = mutated
	second.Outcomes[0].Observation.Answers[0].Choice.Probabilities[0].Probability = 0.5
	*second.Outcomes[0].Observation.Usage.InputTokens = 1

	after, err := json.Marshal(first)
	require.NoError(t, err)
	assert.JSONEq(t, string(before), string(after))
}

func TestEvaluateConcurrentUse(t *testing.T) {
	def := discern.Policy{Version: "routing-v3", Questions: []discern.QuestionPolicy{{
		Question: qQueue, Checks: []discern.Check{{Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.8}},
	}}}
	strict := discern.Policy{Version: "strict-v1", Questions: []discern.QuestionPolicy{{
		Question: qQueue, Checks: []discern.Check{{Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.99}},
	}}}
	ev := pairEvaluator(t, discern.WithDefaultPolicy(def))

	const n = 32
	results := make([]*discern.Result, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			sets := []discern.ProviderSet{discern.JevOnly, discern.ClefOnly, discern.JevAndClef}
			var opts []discern.EvalOption
			if i%2 == 1 {
				opts = append(opts, discern.WithPolicy(strict))
			}
			results[i], errs[i] = ev.Evaluate(t.Context(), ticketRequest(sets[i%3]), opts...)
		})
	}
	wg.Wait()
	for i := range n {
		require.NoError(t, errs[i])
		rec, ok := results[i].Recommendation(qQueue)
		require.True(t, ok)
		if i%2 == 1 {
			assert.Equal(t, discern.PolicyCallOverride, results[i].Policy.Source)
			assert.Equal(t, "strict-v1", results[i].Policy.Policy.Version)
			assert.Equal(t, discern.BelowThreshold, rec.Status)
		} else {
			assert.Equal(t, discern.PolicyClientDefault, results[i].Policy.Source)
			assert.Equal(t, "routing-v3", results[i].Policy.Policy.Version)
		}
	}
	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly))
	require.NoError(t, err)
	assert.Equal(t, "routing-v3", res.Policy.Policy.Version)
}

func TestEvaluateRequestMutationAfterCallHasNoEffect(t *testing.T) {
	var seen *discern.Request
	jev := discerntest.Func(discern.Jev, "", func(_ context.Context, req *discern.Request) (*discern.Observation, error) {
		seen = req
		obs := jevAnswers().observation()
		return &obs, nil
	})
	ev := testutil.Evaluator(t, discern.WithAdapter(jev))
	req := ticketRequest(discern.JevOnly)
	res, err := ev.Evaluate(t.Context(), req)
	require.NoError(t, err)
	before, err := json.Marshal(res)
	require.NoError(t, err)

	req.Questions[0].Options[0].ID = mutated
	req.Questions[0].ID = mutated
	req.EvidenceRefs[0] = mutated

	require.NotNil(t, seen)
	assert.Equal(t, qQueue, seen.Questions[0].ID)
	assert.Equal(t, optBilling, seen.Questions[0].Options[0].ID)
	after, err := json.Marshal(res)
	require.NoError(t, err)
	assert.JSONEq(t, string(before), string(after))
}

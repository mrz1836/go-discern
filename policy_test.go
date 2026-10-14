package discern_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
)

func check(metric discern.Metric, op discern.Op, threshold float64) discern.Check {
	return discern.Check{Metric: metric, Op: op, Threshold: threshold}
}

func policyFor(question string, checks ...discern.Check) discern.Policy {
	return discern.Policy{Version: "routing-v3", Questions: []discern.QuestionPolicy{{Question: question, Checks: checks}}}
}

// evaluatePolicy runs ticketRequest on fakes with the given answers and policy.
func evaluatePolicy(t *testing.T, set discern.ProviderSet, jev, clef *discerntest.Fake, pol discern.Policy) *discern.Result {
	t.Helper()
	ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))
	res, err := ev.Evaluate(t.Context(), ticketRequest(set), discern.WithPolicy(pol))
	require.NotNil(t, res, "evaluate error: %v", err)
	return res
}

func recommendation(t *testing.T, res *discern.Result, question string) discern.Recommendation {
	t.Helper()
	rec, ok := res.Recommendation(question)
	require.True(t, ok)
	return rec
}

func TestPolicyTierOneErrorsMakeNoCalls(t *testing.T) {
	prob := func(op discern.Op, v float64) discern.Policy {
		return policyFor(qQueue, check(discern.MetricChoiceProbability, op, v))
	}
	withQuestions := func(qs ...discern.QuestionPolicy) discern.Policy {
		return discern.Policy{Version: "v1", Questions: qs}
	}
	queueCheck := check(discern.MetricChoiceProbability, discern.AtLeast, 0.5)
	tests := []struct {
		name string
		pol  discern.Policy
	}{
		{"empty version", discern.Policy{}},
		{"version with control character", discern.Policy{Version: "v1\n"}},
		{"unknown metric", policyFor(qQueue, check("margin", discern.AtLeast, 0.5))},
		{"agreement metric in a check", policyFor(qQueue, check(discern.MetricAgreement, discern.AtLeast, 0.5))},
		{"unknown op", prob("approx", 0.5)},
		{"eq op in a check", prob(discern.Equal, 0.5)},
		{"NaN threshold", prob(discern.AtLeast, math.NaN())},
		{"infinite threshold", policyFor(qSeverity, check(discern.MetricScore, discern.AtMost, math.Inf(1)))},
		{"threshold above 1", prob(discern.AtLeast, 1.01)},
		{"negative threshold", prob(discern.AtMost, -0.1)},
		{"above 1 is unsatisfiable", prob(discern.Above, 1)},
		{"below 0 is unsatisfiable", prob(discern.Below, 0)},
		{"score threshold above top level", policyFor(qSeverity, check(discern.MetricScore, discern.AtMost, 2.5))},
		{"score above top level is unsatisfiable", policyFor(qSeverity, check(discern.MetricScore, discern.Above, 2))},
		{"noul metric on a choice question", policyFor(qQueue, check(discern.MetricNoul, discern.AtLeast, 0.5))},
		{"choice metric on a score question", policyFor(qSeverity, check(discern.MetricChoiceProbability, discern.AtLeast, 0.5))},
		{"confidence on a noul question", policyFor(qOutage, check(discern.MetricConfidence, discern.AtLeast, 0.5))},
		{"agreement on a choice question", withQuestions(discern.QuestionPolicy{Question: qQueue, Checks: []discern.Check{queueCheck}, Agreement: &discern.Agreement{}})},
		{"negative max difference", withQuestions(discern.QuestionPolicy{Question: qOutage, Checks: []discern.Check{check(discern.MetricNoul, discern.AtMost, 0.2)}, Agreement: &discern.Agreement{MaxDifference: -0.1}})},
		{"NaN max difference", withQuestions(discern.QuestionPolicy{Question: qOutage, Checks: []discern.Check{check(discern.MetricNoul, discern.AtMost, 0.2)}, Agreement: &discern.Agreement{MaxDifference: math.NaN()}})},
		{"duplicate question policy", withQuestions(discern.QuestionPolicy{Question: qQueue, Checks: []discern.Check{queueCheck}}, discern.QuestionPolicy{Question: qQueue, Checks: []discern.Check{queueCheck}})},
		{"duplicate check ID", withQuestions(discern.QuestionPolicy{Question: qQueue, Checks: []discern.Check{{ID: "a", Metric: discern.MetricChoiceProbability, Op: discern.AtLeast}, {ID: "a", Metric: discern.MetricConfidence, Op: discern.AtLeast}}})},
		{"explicit ID collides with generated ID", withQuestions(discern.QuestionPolicy{Question: qQueue, Checks: []discern.Check{queueCheck, {ID: "queue/checks/0", Metric: discern.MetricConfidence, Op: discern.AtLeast}}})},
		{"empty checks", withQuestions(discern.QuestionPolicy{Question: qQueue})},
		{"invalid question ID", withQuestions(discern.QuestionPolicy{Question: "the queue", Checks: []discern.Check{queueCheck}})},
		{"model with control character", policyFor(qQueue, discern.Check{Model: "jev\x00", Metric: discern.MetricChoiceProbability, Op: discern.AtLeast})},
		{"model too long", policyFor(qQueue, discern.Check{Model: strings.Repeat("m", 257), Metric: discern.MetricChoiceProbability, Op: discern.AtLeast})},
		{"check ID with control character", policyFor(qQueue, discern.Check{ID: "queue\tmin", Metric: discern.MetricChoiceProbability, Op: discern.AtLeast})},
		{"check ID too long", policyFor(qQueue, discern.Check{ID: strings.Repeat("c", 257), Metric: discern.MetricChoiceProbability, Op: discern.AtLeast})},
		{"version too long", discern.Policy{Version: strings.Repeat("v", 257)}},
		{"version with right-to-left override", discern.Policy{Version: "routing-v3\u202e"}},
		{"check ID with zero-width space", policyFor(qQueue, discern.Check{ID: "queue\u200bmin", Metric: discern.MetricChoiceProbability, Op: discern.AtLeast})},
		{"model with soft hyphen", policyFor(qQueue, discern.Check{Model: "jev-1.13\u00ad.0", Metric: discern.MetricChoiceProbability, Op: discern.AtLeast})},
		{"unknown check provider", policyFor(qQueue, discern.Check{Provider: "other", Metric: discern.MetricChoiceProbability, Op: discern.AtLeast})},
		{"unknown preferred provider", func() discern.Policy {
			p := policyFor(qQueue, queueCheck)
			p.PreferOnConflict = "other"
			return p
		}()},
		{"empty question ID", withQuestions(discern.QuestionPolicy{Checks: []discern.Check{queueCheck}})},
		{"score metric on a noul question", policyFor(qOutage, check(discern.MetricScore, discern.AtMost, 1))},
		{"score below 0 is unsatisfiable", policyFor(qSeverity, check(discern.MetricScore, discern.Below, 0))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jev, clef := staticPair()
			ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))
			res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevAndClef), discern.WithPolicy(tt.pol))
			require.ErrorIs(t, err, discern.ErrInvalidPolicy)
			assert.Nil(t, res)
			assert.Equal(t, 0, jev.Calls()+clef.Calls())
		})
	}
}

func TestPolicyUnregisteredProviderIsInvalid(t *testing.T) {
	jev, _ := staticPair()
	ev := testutil.Evaluator(t, discern.WithAdapter(jev))
	scoped := policyFor(qQueue, discern.Check{Provider: discern.Clef, Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.5})
	_, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly), discern.WithPolicy(scoped))
	require.ErrorIs(t, err, discern.ErrInvalidPolicy)
	prefer := policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.5))
	prefer.PreferOnConflict = discern.Clef
	_, err = ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly), discern.WithPolicy(prefer))
	require.ErrorIs(t, err, discern.ErrInvalidPolicy)
	assert.Equal(t, 0, jev.Calls())
}

func TestPolicySingleProviderStatuses(t *testing.T) {
	noConf := jevAnswers()
	noConf.conf = nil
	tests := []struct {
		name    string
		answers ticketAnswers
		pol     discern.Policy
		status  discern.RecommendationStatus
		reasons []discern.ReasonCode
		summary string
	}{
		{
			"accepted", jevAnswers(), policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.8)),
			discern.Accepted,
			[]discern.ReasonCode{discern.ReasonThresholdsMet},
			"accepted billing: jev choice.probability 0.91 ≥ 0.8",
		},
		{
			"below threshold", jevAnswers(), policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.95)),
			discern.BelowThreshold,
			[]discern.ReasonCode{discern.ReasonBelowThreshold},
			"below threshold: jev choice.probability 0.91 is not ≥ 0.95",
		},
		{
			"metric absent", noConf, policyFor(qQueue, check(discern.MetricConfidence, discern.AtLeast, 0.5)),
			discern.Incomplete,
			[]discern.ReasonCode{discern.ReasonMetricAbsent},
			"no recommendation: jev confidence absent",
		},
		{
			"noul summary is numeric", jevAnswers(), policyFor(qOutage, check(discern.MetricNoul, discern.AtMost, 0.1)),
			discern.Accepted,
			[]discern.ReasonCode{discern.ReasonThresholdsMet},
			"accepted: jev noul 0.07 ≤ 0.1",
		},
		{
			"score lower is better", jevAnswers(), policyFor(qSeverity, check(discern.MetricScore, discern.AtMost, 1.0)),
			discern.BelowThreshold,
			[]discern.ReasonCode{discern.ReasonBelowThreshold},
			"below threshold: jev score 1.05 is not ≤ 1",
		},
		{
			"score confidence", jevAnswers(), policyFor(qSeverity, check(discern.MetricConfidence, discern.AtLeast, 0.9)),
			discern.Accepted,
			[]discern.ReasonCode{discern.ReasonThresholdsMet},
			"accepted: jev confidence 0.92 ≥ 0.9",
		},
		{
			"no applicable check", jevAnswers(), policyFor(qQueue, discern.Check{Provider: discern.Clef, Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.5}),
			discern.NotApplicable,
			[]discern.ReasonCode{discern.ReasonNoApplicableCheck},
			"not applicable: no check applies to jev",
		},
		{
			"model mismatch", jevAnswers(), policyFor(qQueue, discern.Check{Provider: discern.Jev, Model: "jev-1.12.0", Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.5}),
			discern.NotApplicable,
			[]discern.ReasonCode{discern.ReasonModelMismatch},
			"not applicable: no check matches jev model jev-1.13.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jev := discerntest.Static(discern.Jev, tt.answers.observation())
			_, clef := staticPair()
			res := evaluatePolicy(t, discern.JevOnly, jev, clef, tt.pol)
			rec := recommendation(t, res, tt.pol.Questions[0].Question)
			assert.Equal(t, tt.status, rec.Status)
			assert.Equal(t, tt.reasons, rec.Reasons)
			assert.Equal(t, tt.summary, rec.Summary)
			if tt.status == discern.Accepted {
				require.NotNil(t, rec.Selected)
				assert.Equal(t, []discern.Provider{discern.Jev}, rec.Selected.Providers)
				assert.Len(t, res.SelectedAnswers(rec.QuestionID), 1)
			} else {
				assert.Nil(t, rec.Selected)
				assert.Nil(t, res.SelectedAnswers(rec.QuestionID))
			}
			assert.Equal(t, 0, clef.Calls())
		})
	}
}

func TestPolicyModelScopeFallsBackToRequestedModel(t *testing.T) {
	obs := jevAnswers().observation()
	obs.Model = ""
	jev := discerntest.Func(discern.Jev, "jev-1.13.0", func(context.Context, *discern.Request) (*discern.Observation, error) {
		c := obs.Clone()
		return &c, nil
	})
	_, clef := staticPair()
	pol := policyFor(qQueue, discern.Check{Provider: discern.Jev, Model: "jev-1.13.0", Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.8})
	res := evaluatePolicy(t, discern.JevOnly, jev, clef, pol)
	rec := recommendation(t, res, qQueue)
	assert.Equal(t, discern.Accepted, rec.Status)
	require.Len(t, rec.Checks, 1)
	assert.Equal(t, "jev-1.13.0", rec.Checks[0].ObservedModel)
}

func TestPolicySkippedModelChecksAreRecorded(t *testing.T) {
	jev, clef := staticPair()
	pol := policyFor(qQueue,
		discern.Check{ID: "pinned", Provider: discern.Jev, Model: "jev-1.12.0", Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.99},
		discern.Check{ID: "any", Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.8},
	)
	res := evaluatePolicy(t, discern.JevOnly, jev, clef, pol)
	rec := recommendation(t, res, qQueue)
	assert.Equal(t, discern.Accepted, rec.Status)
	assert.Equal(t, []discern.ReasonCode{discern.ReasonModelCheckSkipped, discern.ReasonThresholdsMet}, rec.Reasons)
	require.Len(t, rec.Checks, 2)
	assert.Equal(t, discern.ReasonModelMismatch, rec.Checks[0].Skipped)
	assert.Nil(t, rec.Checks[0].Observed)
	assert.False(t, rec.Checks[0].Passed)
	assert.Empty(t, rec.Checks[1].Skipped)
	assert.True(t, rec.Checks[1].Passed)
}

func TestPolicyModelCheckSkippedReason(t *testing.T) {
	// A model-scoped check that no longer matches, next to checks that still
	// apply, is reported so that a model change does not pass unnoticed.
	pinned := func(provider discern.Provider, model string, threshold float64) discern.Check {
		return discern.Check{Provider: provider, Model: model, Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: threshold}
	}
	general := check(discern.MetricChoiceProbability, discern.AtLeast, 0.8)
	tests := []struct {
		name    string
		set     discern.ProviderSet
		clef    *discerntest.Fake
		pol     discern.Policy
		status  discern.RecommendationStatus
		reasons []discern.ReasonCode
	}{
		{
			"single run, pinned check skipped", discern.JevOnly, nil,
			policyFor(qQueue, pinned(discern.Jev, "jev-1.12.0", 0.99), general),
			discern.Accepted,
			[]discern.ReasonCode{discern.ReasonModelCheckSkipped, discern.ReasonThresholdsMet},
		},
		{
			"single run, failing check still applies", discern.JevOnly, nil,
			policyFor(qQueue, pinned(discern.Jev, "jev-1.12.0", 0.5), check(discern.MetricChoiceProbability, discern.AtLeast, 0.95)),
			discern.BelowThreshold,
			[]discern.ReasonCode{discern.ReasonModelCheckSkipped, discern.ReasonBelowThreshold},
		},
		{
			"single run, pinned check matches", discern.JevOnly, nil,
			policyFor(qQueue, pinned(discern.Jev, "jev-1.13.0", 0.8), general),
			discern.Accepted,
			[]discern.ReasonCode{discern.ReasonThresholdsMet},
		},
		{
			"single run, every check skipped", discern.JevOnly, nil,
			policyFor(qQueue, pinned(discern.Jev, "jev-1.12.0", 0.8)),
			discern.NotApplicable,
			[]discern.ReasonCode{discern.ReasonModelMismatch},
		},
		{
			"dual run, one provider's pinned check skipped", discern.JevAndClef, nil,
			policyFor(qQueue, pinned(discern.Clef, "clef-2", 0.99), general),
			discern.Accepted,
			[]discern.ReasonCode{discern.ReasonModelCheckSkipped, discern.ReasonProvidersAgree},
		},
		{
			"dual run, both providers' pinned checks skipped", discern.JevAndClef, nil,
			policyFor(qQueue, pinned(discern.Jev, "jev-1.12.0", 0.99), pinned(discern.Clef, "clef-2", 0.99), general),
			discern.Accepted,
			[]discern.ReasonCode{discern.ReasonModelCheckSkipped, discern.ReasonProvidersAgree},
		},
		{
			"dual run, survivor evaluated alone", discern.JevAndClef, discerntest.Failing(discern.Clef, nil),
			func() discern.Policy {
				p := policyFor(qQueue, pinned(discern.Jev, "jev-1.12.0", 0.99), general)
				p.AllowPartial = true
				return p
			}(),
			discern.Accepted,
			[]discern.ReasonCode{discern.ReasonModelCheckSkipped, discern.ReasonPartialSingleProvider, discern.ReasonThresholdsMet},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jev, clef := staticPair()
			if tt.clef != nil {
				clef = tt.clef
			}
			res := evaluatePolicy(t, tt.set, jev, clef, tt.pol)
			rec := recommendation(t, res, qQueue)
			assert.Equal(t, tt.status, rec.Status)
			assert.Equal(t, tt.reasons, rec.Reasons)
			restored, err := discern.ParseResult([]byte(mustJSON(t, res)))
			require.NoError(t, err)
			assert.Equal(t, tt.reasons, recommendation(t, restored, qQueue).Reasons)
		})
	}
}

func TestPolicyOpBoundaries(t *testing.T) {
	tests := []struct {
		op        discern.Op
		threshold float64
		passed    bool
	}{
		{discern.AtLeast, 0.07, true},
		{discern.Above, 0.07, false},
		{discern.AtMost, 0.07, true},
		{discern.Below, 0.07, false},
		{discern.AtLeast, 0, true},
		{discern.Above, 0, true},
		{discern.AtMost, 0, false},
		{discern.AtMost, 1, true},
		{discern.Below, 1, true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("noul 0.07 %s %v", tt.op, tt.threshold), func(t *testing.T) {
			jev, clef := staticPair()
			res := evaluatePolicy(t, discern.JevOnly, jev, clef, policyFor(qOutage, check(discern.MetricNoul, tt.op, tt.threshold)))
			rec := recommendation(t, res, qOutage)
			require.Len(t, rec.Checks, 1)
			assert.Equal(t, tt.passed, rec.Checks[0].Passed)
			require.NotNil(t, rec.Checks[0].Observed)
			assert.InDelta(t, 0.07, *rec.Checks[0].Observed, 0)
		})
	}
}

func TestPolicyZeroThresholdIsValid(t *testing.T) {
	zero := jevAnswers()
	zero.noul = 0
	jev := discerntest.Static(discern.Jev, zero.observation())
	_, clef := staticPair()
	res := evaluatePolicy(t, discern.JevOnly, jev, clef, policyFor(qOutage, check(discern.MetricNoul, discern.AtMost, 0)))
	assert.Equal(t, discern.Accepted, recommendation(t, res, qOutage).Status)
	res = evaluatePolicy(t, discern.JevOnly, jev, clef, policyFor(qOutage, check(discern.MetricNoul, discern.Above, 0)))
	assert.Equal(t, discern.BelowThreshold, recommendation(t, res, qOutage).Status)
}

func TestPolicyDualChoice(t *testing.T) {
	technical := clefAnswers()
	technical.option, technical.probs = optTechnical, [3]float64{0.3, 0.6, 0.1}
	perProvider := []discern.Check{
		{ID: "queue-jev", Provider: discern.Jev, Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.8},
		{ID: "queue-clef", Provider: discern.Clef, Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.75},
	}
	pol := func(prefer discern.Provider, checks ...discern.Check) discern.Policy {
		return discern.Policy{Version: "routing-v3", PreferOnConflict: prefer, Questions: []discern.QuestionPolicy{{Question: qQueue, Checks: checks}}}
	}
	tests := []struct {
		name     string
		clef     ticketAnswers
		pol      discern.Policy
		status   discern.RecommendationStatus
		reasons  []discern.ReasonCode
		selected *discern.Selected
		summary  string
	}{
		{
			"agree and pass with per-provider thresholds", clefAnswers(), pol("", perProvider...),
			discern.Accepted,
			[]discern.ReasonCode{discern.ReasonProvidersAgree},
			&discern.Selected{Providers: []discern.Provider{discern.Jev, discern.Clef}, Option: optBilling},
			"accepted billing: jev choice.probability 0.91 ≥ 0.8; clef choice.probability 0.87 ≥ 0.75; both chose billing",
		},
		{
			"agree but one below threshold", clefAnswers(), pol("", check(discern.MetricChoiceProbability, discern.AtLeast, 0.9)),
			discern.BelowThreshold,
			[]discern.ReasonCode{discern.ReasonBelowThreshold},
			nil,
			"below threshold: jev choice.probability 0.91 ≥ 0.9; clef choice.probability 0.87 is not ≥ 0.9; both chose billing",
		},
		{
			"disagree defaults to review", technical, pol("", check(discern.MetricChoiceProbability, discern.AtLeast, 0.5)),
			discern.ReviewRequired,
			[]discern.ReasonCode{discern.ReasonProvidersDisagree},
			nil,
			"review required: jev choice.probability 0.91 ≥ 0.5; clef choice.probability 0.6 ≥ 0.5; jev chose billing, clef chose technical",
		},
		{
			"disagree with preferred passing", technical, pol(discern.Jev, check(discern.MetricChoiceProbability, discern.AtLeast, 0.8)),
			discern.Accepted,
			[]discern.ReasonCode{discern.ReasonProvidersDisagree, discern.ReasonPreferredOnConflict},
			&discern.Selected{Providers: []discern.Provider{discern.Jev}, Option: optBilling},
			"accepted billing: jev choice.probability 0.91 ≥ 0.8; clef choice.probability 0.6 is not ≥ 0.8; jev chose billing, clef chose technical; preferred jev on conflict",
		},
		{
			"disagree with preferred failing", technical, pol(discern.Clef, check(discern.MetricChoiceProbability, discern.AtLeast, 0.8)),
			discern.ReviewRequired,
			[]discern.ReasonCode{discern.ReasonProvidersDisagree, discern.ReasonBelowThreshold},
			nil,
			"review required: jev choice.probability 0.91 ≥ 0.8; clef choice.probability 0.6 is not ≥ 0.8; jev chose billing, clef chose technical",
		},
		{
			"one provider has no applicable check", clefAnswers(), pol("", perProvider[0]),
			discern.NotApplicable,
			[]discern.ReasonCode{discern.ReasonNoApplicableCheck},
			nil,
			"not applicable: jev choice.probability 0.91 ≥ 0.8; no check applies to clef; both chose billing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jev := discerntest.Static(discern.Jev, jevAnswers().observation())
			clef := discerntest.Static(discern.Clef, tt.clef.observation())
			res := evaluatePolicy(t, discern.JevAndClef, jev, clef, tt.pol)
			rec := recommendation(t, res, qQueue)
			assert.Equal(t, tt.status, rec.Status)
			assert.Equal(t, tt.reasons, rec.Reasons)
			assert.Equal(t, tt.selected, rec.Selected)
			assert.Equal(t, tt.summary, rec.Summary)
			require.NotEmpty(t, rec.Checks)
			last := rec.Checks[len(rec.Checks)-1]
			assert.Equal(t, "queue/agreement", last.RuleID)
			assert.Equal(t, discern.MetricAgreement, last.Metric)
			assert.Equal(t, discern.Equal, last.Op)
			assert.Zero(t, last.Threshold, "a choice agreement compares options, not numbers")
			assert.Nil(t, last.Observed)
			assert.Equal(t, 1, jev.Calls())
			assert.Equal(t, 1, clef.Calls())
		})
	}
}

func TestPolicyDualNumericAgreement(t *testing.T) {
	far := clefAnswers()
	far.noul = 0.4
	floaty := jevAnswers()
	floaty.noul = 0.3
	sum := clefAnswers()
	sum.noul = 0.1 + 0.2
	noulPolicy := func(agreement *discern.Agreement) discern.Policy {
		return discern.Policy{Version: "v1", Questions: []discern.QuestionPolicy{{
			Question: qOutage, Checks: []discern.Check{check(discern.MetricNoul, discern.AtMost, 0.5)}, Agreement: agreement,
		}}}
	}
	tests := []struct {
		name       string
		jev, clef  ticketAnswers
		agreement  *discern.Agreement
		status     discern.RecommendationStatus
		reasons    []discern.ReasonCode
		difference float64
	}{
		{"within tolerance", jevAnswers(), clefAnswers(), &discern.Agreement{MaxDifference: 0.05}, discern.Accepted, []discern.ReasonCode{discern.ReasonProvidersAgree}, 0.03},
		{"beyond tolerance", jevAnswers(), far, &discern.Agreement{MaxDifference: 0.25}, discern.ReviewRequired, []discern.ReasonCode{discern.ReasonProvidersDisagree}, 0.33},
		{"zero tolerance absorbs float error", floaty, sum, &discern.Agreement{}, discern.Accepted, []discern.ReasonCode{discern.ReasonProvidersAgree}, 0},
		{"missing agreement", jevAnswers(), clefAnswers(), nil, discern.NotApplicable, []discern.ReasonCode{discern.ReasonAgreementMissing}, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jev := discerntest.Static(discern.Jev, tt.jev.observation())
			clef := discerntest.Static(discern.Clef, tt.clef.observation())
			res := evaluatePolicy(t, discern.JevAndClef, jev, clef, noulPolicy(tt.agreement))
			rec := recommendation(t, res, qOutage)
			assert.Equal(t, tt.status, rec.Status)
			assert.Equal(t, tt.reasons, rec.Reasons)
			if tt.difference < 0 {
				assert.Len(t, rec.Checks, 2)
				return
			}
			require.NotEmpty(t, rec.Checks)
			last := rec.Checks[len(rec.Checks)-1]
			assert.Equal(t, "outage/agreement", last.RuleID)
			assert.Equal(t, discern.AtMost, last.Op)
			require.NotNil(t, last.Observed)
			assert.InDelta(t, tt.difference, *last.Observed, 1e-9)
			if tt.status == discern.Accepted {
				assert.Equal(t, &discern.Selected{Providers: []discern.Provider{discern.Jev, discern.Clef}}, rec.Selected)
				assert.Len(t, res.SelectedAnswers(qOutage), 2)
			}
		})
	}
}

func TestPolicyDualScoreSummary(t *testing.T) {
	jev, clef := staticPair()
	pol := discern.Policy{Version: "v1", Questions: []discern.QuestionPolicy{{
		Question: qSeverity, Checks: []discern.Check{check(discern.MetricScore, discern.AtMost, 1.5)}, Agreement: &discern.Agreement{MaxDifference: 0.25},
	}}}
	res := evaluatePolicy(t, discern.JevAndClef, jev, clef, pol)
	rec := recommendation(t, res, qSeverity)
	assert.Equal(t, discern.Accepted, rec.Status)
	assert.Equal(t, "accepted: jev score 1.05 ≤ 1.5; clef score 1.1 ≤ 1.5; jev score 1.05 and clef score 1.1 differ by 0.05 <= 0.25", rec.Summary)
}

func TestPolicyDualBlockingAndConflictPaths(t *testing.T) {
	noConf := clefAnswers()
	noConf.conf, noConf.scoreConf = nil, nil
	far := clefAnswers()
	far.noul = 0.4
	confidence := func(provider discern.Provider, model string) discern.Check {
		return discern.Check{Provider: provider, Model: model, Metric: discern.MetricConfidence, Op: discern.AtLeast, Threshold: 0.5}
	}
	tests := []struct {
		name     string
		clef     ticketAnswers
		pol      discern.Policy
		question string
		status   discern.RecommendationStatus
		reasons  []discern.ReasonCode
		selected *discern.Selected
		summary  string
	}{
		{
			"metric absent for one provider", noConf, policyFor(qQueue, confidence("", "")), qQueue,
			discern.Incomplete,
			[]discern.ReasonCode{discern.ReasonMetricAbsent},
			nil,
			"no recommendation: jev confidence 0.87 ≥ 0.5; clef confidence absent; both chose billing",
		},
		{
			"model mismatch for one provider", clefAnswers(), policyFor(qQueue, confidence(discern.Jev, ""), confidence(discern.Clef, "clef-2")), qQueue,
			discern.NotApplicable,
			[]discern.ReasonCode{discern.ReasonModelMismatch},
			nil,
			"not applicable: jev confidence 0.87 ≥ 0.5; no check matches clef model clef; both chose billing",
		},
		{
			"absent outranks not applicable", noConf,
			discern.Policy{Version: "v1", Questions: []discern.QuestionPolicy{{
				Question: qSeverity, Checks: []discern.Check{confidence(discern.Clef, "")},
			}}},
			qSeverity,
			discern.Incomplete,
			[]discern.ReasonCode{discern.ReasonNoApplicableCheck, discern.ReasonMetricAbsent, discern.ReasonAgreementMissing},
			nil,
			"no recommendation: no check applies to jev; clef confidence absent; a dual score question needs an agreement tolerance",
		},
		{
			"numeric conflict resolved by the preferred provider", far,
			discern.Policy{Version: "v1", PreferOnConflict: discern.Jev, Questions: []discern.QuestionPolicy{{
				Question: qOutage, Checks: []discern.Check{check(discern.MetricNoul, discern.AtMost, 0.5)}, Agreement: &discern.Agreement{MaxDifference: 0.1},
			}}},
			qOutage,
			discern.Accepted,
			[]discern.ReasonCode{discern.ReasonProvidersDisagree, discern.ReasonPreferredOnConflict},
			&discern.Selected{Providers: []discern.Provider{discern.Jev}},
			"accepted: jev noul 0.07 ≤ 0.5; clef noul 0.4 ≤ 0.5; jev noul 0.07 and clef noul 0.4 differ by 0.33 > 0.1; preferred jev on conflict",
		},
		{
			"numeric agreement with one provider below threshold", clefAnswers(),
			discern.Policy{Version: "v1", Questions: []discern.QuestionPolicy{{
				Question: qOutage, Checks: []discern.Check{check(discern.MetricNoul, discern.AtMost, 0.08)}, Agreement: &discern.Agreement{MaxDifference: 0.05},
			}}},
			qOutage,
			discern.BelowThreshold,
			[]discern.ReasonCode{discern.ReasonBelowThreshold},
			nil,
			"below threshold: jev noul 0.07 ≤ 0.08; clef noul 0.1 is not ≤ 0.08; jev noul 0.07 and clef noul 0.1 differ by 0.03 <= 0.05",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jev := discerntest.Static(discern.Jev, jevAnswers().observation())
			clef := discerntest.Static(discern.Clef, tt.clef.observation())
			res := evaluatePolicy(t, discern.JevAndClef, jev, clef, tt.pol)
			rec := recommendation(t, res, tt.question)
			assert.Equal(t, tt.status, rec.Status)
			assert.Equal(t, tt.reasons, rec.Reasons)
			assert.Equal(t, tt.selected, rec.Selected)
			assert.Equal(t, tt.summary, rec.Summary)
			assert.Equal(t, tt.status, res.Overall.Status)
		})
	}
}

func TestPolicyDualOverallAndRepeatedReasons(t *testing.T) {
	technical := clefAnswers()
	technical.option, technical.probs = optTechnical, [3]float64{0.3, 0.6, 0.1}
	technical.conf, technical.scoreConf = nil, nil
	noConf := jevAnswers()
	noConf.conf, noConf.scoreConf = nil, nil
	pol := discern.Policy{Version: "v1", Questions: []discern.QuestionPolicy{
		{Question: qQueue, Checks: []discern.Check{check(discern.MetricChoiceProbability, discern.AtLeast, 0.5)}},
		{Question: qOutage, Checks: []discern.Check{check(discern.MetricNoul, discern.AtLeast, 0.5)}, Agreement: &discern.Agreement{MaxDifference: 0.1}},
		{Question: qSeverity, Checks: []discern.Check{check(discern.MetricConfidence, discern.AtLeast, 0.5)}, Agreement: &discern.Agreement{MaxDifference: 0.5}},
	}}
	jev := discerntest.Static(discern.Jev, noConf.observation())
	clef := discerntest.Static(discern.Clef, technical.observation())
	res := evaluatePolicy(t, discern.JevAndClef, jev, clef, pol)

	assert.Equal(t, discern.ReviewRequired, recommendation(t, res, qQueue).Status)
	assert.Equal(t, discern.BelowThreshold, recommendation(t, res, qOutage).Status)
	severity := recommendation(t, res, qSeverity)
	assert.Equal(t, discern.Incomplete, severity.Status)
	assert.Equal(t, []discern.ReasonCode{discern.ReasonMetricAbsent}, severity.Reasons, "a reason shared by both providers is listed once")
	assert.Equal(t, discern.Overall{Status: discern.Incomplete, Uncovered: []string{}}, res.Overall)

	withoutSeverity := pol
	withoutSeverity.Questions = pol.Questions[:2]
	again, err := discern.Recommend(res, withoutSeverity)
	require.NoError(t, err)
	assert.Equal(t, discern.Overall{Status: discern.ReviewRequired, Uncovered: []string{qSeverity}}, again.Overall, "review outranks below threshold")
}

func TestPolicyPartialFailure(t *testing.T) {
	queue := check(discern.MetricChoiceProbability, discern.AtLeast, 0.8)
	tests := []struct {
		name         string
		allowPartial bool
		prefer       discern.Provider
		threshold    float64
		status       discern.RecommendationStatus
		reasons      []discern.ReasonCode
		summary      string
	}{
		{
			"default requires both", false, "", 0.8, discern.Incomplete,
			[]discern.ReasonCode{discern.ReasonProviderFailed},
			"no recommendation: jev choice.probability 0.91 ≥ 0.8; clef failed (transport); policy requires both providers",
		},
		{
			"allow partial accepts survivor", true, "", 0.8, discern.Accepted,
			[]discern.ReasonCode{discern.ReasonPartialSingleProvider, discern.ReasonThresholdsMet},
			"accepted billing: jev choice.probability 0.91 ≥ 0.8; clef failed (transport); evaluated jev alone",
		},
		{
			"allow partial survivor below threshold", true, "", 0.95, discern.BelowThreshold,
			[]discern.ReasonCode{discern.ReasonPartialSingleProvider, discern.ReasonBelowThreshold},
			"below threshold: jev choice.probability 0.91 is not ≥ 0.95; clef failed (transport); evaluated jev alone",
		},
		{
			"preference for the failed provider is not a fallback", true, discern.Clef, 0.8, discern.Accepted,
			[]discern.ReasonCode{discern.ReasonPartialSingleProvider, discern.ReasonThresholdsMet},
			"accepted billing: jev choice.probability 0.91 ≥ 0.8; clef failed (transport); evaluated jev alone",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jev, _ := staticPair()
			clef := discerntest.Failing(discern.Clef, nil)
			c := queue
			c.Threshold = tt.threshold
			pol := policyFor(qQueue, c)
			pol.AllowPartial, pol.PreferOnConflict = tt.allowPartial, tt.prefer
			res := evaluatePolicy(t, discern.JevAndClef, jev, clef, pol)
			assert.Equal(t, discern.Partial, res.Status)
			rec := recommendation(t, res, qQueue)
			assert.Equal(t, tt.status, rec.Status)
			assert.Equal(t, tt.reasons, rec.Reasons)
			assert.Equal(t, tt.summary, rec.Summary)
			require.Len(t, rec.Checks, 1)
			assert.Equal(t, discern.Jev, rec.Checks[0].Provider)
			if tt.status == discern.Accepted {
				assert.Equal(t, &discern.Selected{Providers: []discern.Provider{discern.Jev}, Option: optBilling}, rec.Selected)
			}
		})
	}
}

func TestPolicyFailedResultIsIncomplete(t *testing.T) {
	jev := discerntest.Failing(discern.Jev, nil)
	clef := discerntest.Failing(discern.Clef, nil)
	pol := discern.Policy{Version: "v1", AllowPartial: true, Questions: []discern.QuestionPolicy{
		{Question: qQueue, Checks: []discern.Check{check(discern.MetricChoiceProbability, discern.AtLeast, 0.5)}},
		{Question: qOutage, Checks: []discern.Check{check(discern.MetricNoul, discern.AtMost, 0.5)}},
	}}
	res := evaluatePolicy(t, discern.JevAndClef, jev, clef, pol)
	assert.Equal(t, discern.Failed, res.Status)
	for _, id := range []string{qQueue, qOutage} {
		rec := recommendation(t, res, id)
		assert.Equal(t, discern.Incomplete, rec.Status)
		assert.Equal(t, []discern.ReasonCode{discern.ReasonProviderFailed}, rec.Reasons)
		assert.Equal(t, "no recommendation: jev failed (transport); clef failed (transport)", rec.Summary)
	}
	assert.Equal(t, discern.Overall{Status: discern.Incomplete, Uncovered: []string{qSeverity}}, res.Overall)
}

func TestPolicyOverall(t *testing.T) {
	qp := func(id string, c discern.Check) discern.QuestionPolicy {
		return discern.QuestionPolicy{Question: id, Checks: []discern.Check{c}}
	}
	accept := qp(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.5))
	below := qp(qOutage, check(discern.MetricNoul, discern.AtLeast, 0.5))
	absent := qp(qSeverity, discern.Check{Provider: discern.Clef, Metric: discern.MetricScore, Op: discern.AtMost, Threshold: 2})
	tests := []struct {
		name      string
		questions []discern.QuestionPolicy
		status    discern.RecommendationStatus
		uncovered []string
		unmatched []string
	}{
		{"all covered accepted", []discern.QuestionPolicy{accept}, discern.Accepted, []string{qOutage, qSeverity}, []string{}},
		{"one below threshold", []discern.QuestionPolicy{accept, below}, discern.BelowThreshold, []string{qSeverity}, []string{}},
		{"not applicable outranks below threshold", []discern.QuestionPolicy{accept, below, absent}, discern.NotApplicable, []string{}, []string{}},
		{"nothing covered", []discern.QuestionPolicy{qp("priority", check(discern.MetricNoul, discern.AtLeast, 0.5))}, discern.NoRule, []string{qQueue, qOutage, qSeverity}, []string{"priority"}},
		{"unmatched entries keep policy order", []discern.QuestionPolicy{qp("priority", check(discern.MetricNoul, discern.AtLeast, 0.5)), accept, qp("language", check(discern.MetricNoul, discern.AtLeast, 0.5))}, discern.Accepted, []string{qOutage, qSeverity}, []string{"priority", "language"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jev, clef := staticPair()
			res := evaluatePolicy(t, discern.JevOnly, jev, clef, discern.Policy{Version: "v1", Questions: tt.questions})
			assert.Equal(t, discern.Overall{Status: tt.status, Uncovered: tt.uncovered}, res.Overall)
			assert.Equal(t, tt.unmatched, res.Policy.Unmatched)
			for _, id := range tt.uncovered {
				rec := recommendation(t, res, id)
				assert.Equal(t, discern.NoRule, rec.Status)
				assert.Equal(t, []discern.ReasonCode{discern.ReasonQuestionNotInPolicy}, rec.Reasons)
			}
		})
	}
}

func TestPolicyPrecedence(t *testing.T) {
	def := policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.8))
	override := policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.95))
	override.Version = "strict-v1"
	tests := []struct {
		name    string
		def     *discern.Policy
		opts    []discern.EvalOption
		source  discern.PolicySource
		version string
		status  discern.RecommendationStatus
	}{
		{"library default is no policy", nil, nil, discern.PolicyNone, "", discern.NoRule},
		{"client default", &def, nil, discern.PolicyClientDefault, "routing-v3", discern.Accepted},
		{"call override", &def, []discern.EvalOption{discern.WithPolicy(override)}, discern.PolicyCallOverride, "strict-v1", discern.BelowThreshold},
		{"override without default", nil, []discern.EvalOption{discern.WithPolicy(override)}, discern.PolicyCallOverride, "strict-v1", discern.BelowThreshold},
		{"disabled", &def, []discern.EvalOption{discern.WithoutPolicy()}, discern.PolicyDisabled, "", discern.NoRule},
		{"last option wins: disable", &def, []discern.EvalOption{discern.WithPolicy(override), discern.WithoutPolicy()}, discern.PolicyDisabled, "", discern.NoRule},
		{"last option wins: override", &def, []discern.EvalOption{discern.WithoutPolicy(), discern.WithPolicy(override)}, discern.PolicyCallOverride, "strict-v1", discern.BelowThreshold},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jev, _ := staticPair()
			opts := []discern.Option{discern.WithAdapter(jev)}
			if tt.def != nil {
				opts = append(opts, discern.WithDefaultPolicy(*tt.def))
			}
			ev := testutil.Evaluator(t, opts...)
			res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly), tt.opts...)
			require.NoError(t, err)
			assert.Equal(t, tt.source, res.Policy.Source)
			if tt.version == "" {
				assert.Nil(t, res.Policy.Policy)
			} else {
				require.NotNil(t, res.Policy.Policy)
				assert.Equal(t, tt.version, res.Policy.Policy.Version)
			}
			assert.Equal(t, tt.status, recommendation(t, res, qQueue).Status)

			again, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly))
			require.NoError(t, err)
			if tt.def != nil {
				assert.Equal(t, discern.PolicyClientDefault, again.Policy.Source)
				assert.Equal(t, "routing-v3", again.Policy.Policy.Version)
			}
		})
	}
}

func TestPolicyIsCopiedWhenSet(t *testing.T) {
	jev, _ := staticPair()
	def := policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.8))
	ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithDefaultPolicy(def))
	def.Version = mutated
	def.Questions[0].Checks[0].Threshold = 0.99

	override := policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.8))
	opt := discern.WithPolicy(override)
	override.Questions[0].Checks[0].Threshold = 0.99

	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly))
	require.NoError(t, err)
	assert.Equal(t, "routing-v3", res.Policy.Policy.Version)
	assert.Equal(t, discern.Accepted, recommendation(t, res, qQueue).Status)

	res, err = ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly), opt)
	require.NoError(t, err)
	assert.Equal(t, discern.Accepted, recommendation(t, res, qQueue).Status)

	res.Policy.Policy.Questions[0].Checks[0].Threshold = 0.99
	res, err = ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly))
	require.NoError(t, err)
	assert.InDelta(t, 0.8, res.Policy.Policy.Questions[0].Checks[0].Threshold, 0)
}

func TestPolicyAgreementIsCopiedWhenSet(t *testing.T) {
	agreement := &discern.Agreement{MaxDifference: 0.05}
	pol := discern.Policy{Version: "v1", Questions: []discern.QuestionPolicy{{
		Question: qOutage, Checks: []discern.Check{check(discern.MetricNoul, discern.AtMost, 0.5)}, Agreement: agreement,
	}}}
	ev := pairEvaluator(t, discern.WithDefaultPolicy(pol))
	opt := discern.WithPolicy(pol)
	agreement.MaxDifference = 0 // after both options copied the policy

	for _, opts := range [][]discern.EvalOption{nil, {opt}} {
		res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevAndClef), opts...)
		require.NoError(t, err)
		require.NotNil(t, res.Policy.Policy.Questions[0].Agreement)
		assert.InDelta(t, 0.05, res.Policy.Policy.Questions[0].Agreement.MaxDifference, 0)
		assert.NotSame(t, agreement, res.Policy.Policy.Questions[0].Agreement)
		assert.Equal(t, discern.Accepted, recommendation(t, res, qOutage).Status)
	}

	again, err := discern.Recommend(mustEvaluate(t, ev, discern.JevAndClef), pol)
	require.NoError(t, err)
	assert.Equal(t, discern.ReviewRequired, recommendation(t, again, qOutage).Status, "Recommend reads the policy as passed")
	assert.NotSame(t, agreement, again.Policy.Policy.Questions[0].Agreement)
}

func TestPolicySnapshotIsNormalized(t *testing.T) {
	jev, _ := staticPair()
	ev := testutil.Evaluator(t, discern.WithAdapter(jev))
	pol := policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.8), check(discern.MetricConfidence, discern.AtLeast, 0.5))
	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly), discern.WithPolicy(pol))
	require.NoError(t, err)
	assert.Equal(t, "queue/checks/0", res.Policy.Policy.Questions[0].Checks[0].ID)
	assert.Equal(t, "queue/checks/1", res.Policy.Policy.Questions[0].Checks[1].ID)
	rec := recommendation(t, res, qQueue)
	assert.Equal(t, "queue/checks/0", rec.Checks[0].RuleID)

	data, err := json.Marshal(res.Policy)
	require.NoError(t, err)
	assert.JSONEq(t, `{"source":"call_override","unmatched":[],"policy":{"version":"routing-v3","prefer_on_conflict":"","allow_partial":false,
		"questions":[{"question":"queue","agreement":null,"checks":[
		{"id":"queue/checks/0","provider":"","model":"","metric":"choice.probability","op":"gte","threshold":0.8},
		{"id":"queue/checks/1","provider":"","model":"","metric":"confidence","op":"gte","threshold":0.5}]}]}}`, string(data))
}

func TestRecommendIsPure(t *testing.T) {
	jev, clef := staticPair()
	ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))
	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevAndClef))
	require.NoError(t, err)
	before, err := json.Marshal(res)
	require.NoError(t, err)

	pol := policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.8))
	again, err := discern.Recommend(res, pol)
	require.NoError(t, err)
	assert.NotSame(t, res, again)
	assert.Equal(t, discern.PolicyRecommend, again.Policy.Source)
	assert.Equal(t, discern.Accepted, recommendation(t, again, qQueue).Status)
	assert.Equal(t, discern.Overall{Status: discern.Accepted, Uncovered: []string{qOutage, qSeverity}}, again.Overall)
	assert.Equal(t, 1, jev.Calls())
	assert.Equal(t, 1, clef.Calls())

	after, err := json.Marshal(res)
	require.NoError(t, err)
	assert.JSONEq(t, string(before), string(after))
	assert.Equal(t, res.Outcomes, again.Outcomes)
}

func TestRecommendTwoSidedNoulGate(t *testing.T) {
	high := jevAnswers()
	high.noul = 0.93
	ev := testutil.Evaluator(t, discern.WithAdapter(discerntest.Static(discern.Jev, high.observation())))
	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly))
	require.NoError(t, err)

	escalate, err := discern.Recommend(res, policyFor(qOutage, check(discern.MetricNoul, discern.AtLeast, 0.9)))
	require.NoError(t, err)
	closeTicket, err := discern.Recommend(res, policyFor(qOutage, check(discern.MetricNoul, discern.AtMost, 0.1)))
	require.NoError(t, err)
	assert.Equal(t, discern.Accepted, recommendation(t, escalate, qOutage).Status)
	assert.Equal(t, "accepted: jev noul 0.93 ≥ 0.9", recommendation(t, escalate, qOutage).Summary)
	assert.Equal(t, discern.BelowThreshold, recommendation(t, closeTicket, qOutage).Status)
}

func TestRecommendErrors(t *testing.T) {
	_, err := discern.Recommend(nil, policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.8)))
	require.ErrorIs(t, err, discern.ErrInvalidRecord)

	jev, _ := staticPair()
	ev := testutil.Evaluator(t, discern.WithAdapter(jev))
	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly))
	require.NoError(t, err)
	_, err = discern.Recommend(res, discern.Policy{})
	require.ErrorIs(t, err, discern.ErrInvalidPolicy)
	_, err = discern.Recommend(res, policyFor(qQueue, check(discern.MetricNoul, discern.AtLeast, 0.8)))
	require.ErrorIs(t, err, discern.ErrInvalidPolicy)

	broken := res.Clone()
	broken.Status = discern.Failed
	_, err = discern.Recommend(broken, policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.8)))
	require.ErrorIs(t, err, discern.ErrInvalidRecord)

	reordered := res.Clone()
	answers := reordered.Outcomes[0].Observation.Answers
	answers[0], answers[1] = answers[1], answers[0]
	_, err = discern.Recommend(reordered, policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.8)))
	require.ErrorIs(t, err, discern.ErrInvalidRecord, "answers must stay in request order")
}

func TestRecommendChecksOnlyThePartsItKeeps(t *testing.T) {
	// A result built by hand, or one whose verdicts are stale, may leave the
	// policy snapshot, recommendations and overall verdict zero: Recommend
	// replaces them.
	jev, _ := staticPair()
	res := mustEvaluate(t, testutil.Evaluator(t, discern.WithAdapter(jev)), discern.JevOnly)
	bare := res.Clone()
	bare.Policy, bare.Recommendations, bare.Overall = discern.PolicySnapshot{}, nil, discern.Overall{}
	stale := res.Clone()
	stale.Recommendations[0].Status = discern.Accepted
	stale.Overall.Status = "stale"

	pol := policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.8))
	for _, in := range []*discern.Result{bare, stale} {
		again, err := discern.Recommend(in, pol)
		require.NoError(t, err)
		assert.Equal(t, discern.PolicyRecommend, again.Policy.Source)
		assert.Equal(t, discern.Accepted, recommendation(t, again, qQueue).Status)
		assert.Equal(t, discern.Overall{Status: discern.Accepted, Uncovered: []string{qOutage, qSeverity}}, again.Overall)
		_, err = discern.ParseResult([]byte(mustJSON(t, again)))
		require.NoError(t, err, "the new result is a valid record")
	}

	// The parts it keeps are still checked.
	tests := []struct {
		name string
		edit func(r *discern.Result)
	}{
		{"schema", func(r *discern.Result) { r.SchemaVersion = "" }},
		{"provider set", func(r *discern.Result) { r.Providers = "" }},
		{"questions", func(r *discern.Result) { r.Questions = nil }},
		{"outcome status", func(r *discern.Result) { r.Outcomes[0].Status = "retried" }},
		{"result status", func(r *discern.Result) { r.Status = discern.Partial }},
		{"redactions", func(r *discern.Result) { r.Redacted = []string{"correlation_id"} }},
		{"correlation ID", func(r *discern.Result) { r.CorrelationID = "ticket\n1042" }},
		{"elapsed time", func(r *discern.Result) { r.Elapsed = -1 }},
		{"observed model", func(r *discern.Result) { r.Outcomes[0].Observation.Model = "jev 1" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			broken := bare.Clone()
			tt.edit(broken)
			_, err := discern.Recommend(broken, pol)
			require.ErrorIs(t, err, discern.ErrInvalidRecord)
		})
	}
}

func TestRecommendLeavesInputUntouchedOnError(t *testing.T) {
	res := mustEvaluate(t, testutil.Evaluator(t, discern.WithAdapter(discerntest.Static(discern.Jev, jevAnswers().observation()))), discern.JevOnly)
	before, err := json.Marshal(res)
	require.NoError(t, err)
	again, err := discern.Recommend(res, policyFor(qQueue, check(discern.MetricNoul, discern.AtLeast, 0.8)))
	require.ErrorIs(t, err, discern.ErrInvalidPolicy)
	assert.Nil(t, again)
	after, err := json.Marshal(res)
	require.NoError(t, err)
	assert.JSONEq(t, string(before), string(after))
}

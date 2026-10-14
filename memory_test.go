package discern_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
)

// Allocation ceilings sit about 25% above the counts measured on the current
// implementation, so that a regression (such as per-option formatting or an extra
// copy of every answer) fails loudly while toolchain noise does not. Every guard
// logs what it measured. The race detector changes allocation counts, so the
// guards skip under it.

// largeState returns a synthetic JSON state of about n bytes.
func largeState(n int) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"messages":[`)
	for i := 0; buf.Len() < n; i++ {
		if i > 0 {
			buf.WriteByte(',')
		}
		fmt.Fprintf(&buf, `{"id":%d,"text":"I was charged twice for invoice %d, please refund the duplicate."}`, i, i)
	}
	buf.WriteString(`]}`)
	return buf.Bytes()
}

func TestEvaluateDoesNotCopyLargeState(t *testing.T) {
	testutil.SkipUnderRace(t)
	const size = 4 << 20
	raw := largeState(size)
	fake := discerntest.Static(discern.Jev, jevAnswers().observation())
	ev := testutil.Evaluator(t, discern.WithAdapter(fake))
	req := ticketRequest(discern.JevOnly)
	req.State = discern.JSON(raw)

	_, perOp := testutil.AllocStats(20, func() { _, _ = ev.Evaluate(t.Context(), req) })
	t.Logf("%.0f bytes per evaluation of a %d-byte state", perOp, len(raw))
	assert.Less(t, perOp, float64(64<<10), "an evaluation must not copy or rescan-allocate its state")

	// Building the Value copies the caller's bytes exactly once.
	_, build := testutil.AllocStats(5, func() { _ = discern.JSON(raw) })
	assert.Less(t, build, 1.25*float64(len(raw)))
}

func TestEvaluateAllocationsStayBounded(t *testing.T) {
	testutil.SkipUnderRace(t)
	jev, clef := staticPair()
	ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))
	pol := policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.8))
	ctx := t.Context()
	single := ticketRequest(discern.JevOnly)
	dual := ticketRequest(discern.JevAndClef)
	failing := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(discerntest.Failing(discern.Clef, nil)))

	testutil.CheckCeilings(t, []testutil.Ceiling{
		{Name: "single provider", Max: 41, Run: func() { _, _ = ev.Evaluate(ctx, single) }},
		{Name: "dual provider with policy", Max: 98, Run: func() { _, _ = ev.Evaluate(ctx, dual, discern.WithPolicy(pol)) }},
		{Name: "dual provider with one failure", Max: 88, Run: func() { _, _ = failing.Evaluate(ctx, dual, discern.WithPolicy(pol)) }},
		{Name: "invalid request", Max: 13, Run: func() { _, _ = ev.Evaluate(ctx, ticketRequest("")) }},
	})
}

func TestRecordAllocationsStayBounded(t *testing.T) {
	testutil.SkipUnderRace(t)
	res := mustEvaluate(t, pairEvaluator(t), discern.JevAndClef, discern.WithPolicy(benchPolicy()))
	data, err := json.Marshal(res)
	require.NoError(t, err)
	pol := benchPolicy()
	all := discern.Redaction{CorrelationID: true, EvidenceRefs: true, RequestIDs: true, Usage: true}

	// ParseResult is bounded by encoding/json; its own validation allocates nothing.
	testutil.CheckCeilings(t, []testutil.Ceiling{
		{Name: "Recommend", Max: 50, Run: func() { _, _ = discern.Recommend(res, pol) }},
		{Name: "ParseResult", Max: 255, Run: func() { _, _ = discern.ParseResult(data) }},
		{Name: "Clone", Max: 39, Run: func() { _ = res.Clone() }},
		{Name: "Redact", Max: 43, Run: func() { _ = res.Redact(all) }},
		{Name: "Observation", Max: 9, Run: func() { _, _ = res.Observation(discern.Jev) }},
		{Name: "SelectedAnswers", Max: 8, Run: func() { _ = res.SelectedAnswers(qQueue) }},
	})
}

func TestEvaluateBatchMemoryScalesLinearly(t *testing.T) {
	testutil.SkipUnderRace(t)
	ev := pairEvaluator(t, discern.WithBatchConcurrency(4))
	batch := func(n int) []discern.Request {
		reqs := make([]discern.Request, n)
		for i := range reqs {
			reqs[i] = ticketRequest(discern.JevAndClef)
		}
		return reqs
	}
	perItem := func(n int) (allocs, allocated float64) {
		reqs := batch(n)
		failed := false
		a, b := testutil.AllocStats(5, func() {
			items, err := ev.EvaluateBatch(t.Context(), reqs)
			failed = failed || err != nil || len(items) != n
		})
		require.False(t, failed, "every batch of %d items succeeds", n)
		return a / float64(n), b / float64(n)
	}
	smallAllocs, smallBytes := perItem(10)
	largeAllocs, largeBytes := perItem(200)
	t.Logf("per item: %.1f allocations and %.0f bytes for 10 items, %.1f allocations and %.0f bytes for 200 items",
		smallAllocs, smallBytes, largeAllocs, largeBytes)
	assert.LessOrEqual(t, largeAllocs, 77.0, "allocations per dual item (ceiling)")
	assert.InDelta(t, smallAllocs, largeAllocs, 0.25*smallAllocs, "allocations per item do not grow with the batch")
	assert.InDelta(t, smallBytes, largeBytes, 0.25*smallBytes, "bytes per item do not grow with the batch")
}

func TestLargeQuestionSetsScaleLinearly(t *testing.T) {
	testutil.SkipUnderRace(t)
	type costs struct{ evaluate, recommend, clone, parse float64 }
	measure := func(n int) costs {
		req, obs := largeRequest(n, discern.MaxChoiceOptions)
		req.Providers = discern.JevAndClef
		ev := testutil.Evaluator(t, discern.WithAdapter(discerntest.Static(discern.Jev, obs)), discern.WithAdapter(discerntest.Static(discern.Clef, obs)))
		pol := largePolicy(n)
		res, err := ev.Evaluate(t.Context(), req)
		require.NoError(t, err)
		data, err := json.Marshal(res)
		require.NoError(t, err)
		var c costs
		c.evaluate, _ = testutil.AllocStats(5, func() { _, _ = ev.Evaluate(t.Context(), req) })
		c.recommend, _ = testutil.AllocStats(5, func() { _, _ = discern.Recommend(res, pol) })
		c.clone, _ = testutil.AllocStats(5, func() { _ = res.Clone() })
		c.parse, _ = testutil.AllocStats(5, func() { _, _ = discern.ParseResult(data) })
		return c
	}
	small, large := measure(8), measure(64)
	t.Logf("allocations for 8 and 64 questions of 255 options: evaluate %v -> %v, recommend %v -> %v, clone %v -> %v, parse %v -> %v",
		small.evaluate, large.evaluate, small.recommend, large.recommend, small.clone, large.clone, small.parse, large.parse)

	// Copies of observations, policies and recommendations share one backing
	// array per part type, and validation reuses one set of member IDs, so an
	// evaluation or a copy costs about the same for 8 or 64 questions. Recommend
	// adds a summary, an agreement rule ID and a default check ID per question,
	// and nothing per option. Parsing is dominated by encoding/json, which
	// allocates per decoded value.
	const more = 64 - 8
	assert.Less(t, large.evaluate-small.evaluate, 0.25*more, "evaluate")
	assert.Less(t, large.recommend-small.recommend, 4.0*more, "recommend")
	assert.Less(t, large.clone-small.clone, 0.1*more, "clone")
	assert.Less(t, large.parse/small.parse, 8.0*1.25, "parse grows at most linearly")
}

func TestValidationAllocationsDoNotGrowPerOption(t *testing.T) {
	testutil.SkipUnderRace(t)
	measure := func(n int) float64 {
		opts := make([]discern.ChoiceOption, n)
		probs := make([]discern.OptionProbability, n)
		for i := range opts {
			id := fmt.Sprintf("o%d", i)
			opts[i] = discern.Opt(id, discern.Value{})
			probs[i] = discern.OptionProbability{Option: id}
		}
		probs[0].Probability = 1
		obs := discern.Observation{Answers: []discern.Answer{{
			QuestionID: qQueue, Kind: discern.KindChoice,
			Choice: &discern.ChoiceAnswer{Option: "o0", Probabilities: probs},
		}}}
		ev := testutil.Evaluator(t, discern.WithAdapter(discerntest.Static(discern.Jev, obs)))
		req := discern.Request{
			Providers: discern.JevOnly, State: discern.Text("ticket"),
			Questions: []discern.Question{discern.Choice(qQueue, discern.Text("Which queue?"), opts...)},
		}
		allocs, _ := testutil.AllocStats(20, func() { _, _ = ev.Evaluate(t.Context(), req) })
		return allocs
	}
	small, large := measure(4), measure(255)
	t.Logf("allocations: %v with 4 options, %v with 255 options", small, large)
	// The duplicate check switches to one map beyond 16 options, which costs a
	// few allocations; nothing is allocated per option.
	assert.Less(t, large-small, 8.0)
}

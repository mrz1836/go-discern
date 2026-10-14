//go:build live

package livetest_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
)

const clefAuthError = 10000

// dualTests runs requests on both providers: policies, records, partial and
// total failures, batches and concurrent calls.
func (s *suite) dualTests(t *testing.T) {
	s.run(t, "json-state", needBoth, s.dualJSONState)
	s.run(t, "unicode", needBoth, s.dualUnicode)
	s.run(t, "policy", needBoth, s.dualPolicy)
	s.run(t, "record", 0, s.dualRecord)
	s.run(t, "ambiguous", needBoth, s.dualAmbiguous)
	s.run(t, "partial", needBoth, s.dualPartial)
	s.run(t, "both-fail", needBoth, s.dualBothFail)
	s.run(t, "batch", needBoth, s.dualBatch)
	s.run(t, "concurrency", needBoth, s.dualConcurrency)
}

func (s *suite) dualJSONState(t *testing.T) {
	state, err := discern.Marshal(map[string]any{
		"order_id": "A-1001", "status": "in_transit", "promised_by": "2026-10-01", "today": "2026-10-14",
		"items": []map[string]any{{"sku": "KB-1", "qty": 1}, {"sku": "MS-2", "qty": 2}}, "delivered_at": nil,
	})
	require.NoError(t, err)
	ev := evaluator(t, s.jev(t), s.clef(t, clef.ModelFlash))
	res, err := s.evaluate(t, ev, discern.Request{
		Providers: discern.JevAndClef,
		State:     state,
		Questions: []discern.Question{discern.Noul("overdue", discern.Text("Is this order past its promised delivery date?"))},
	})
	requireComplete(t, res, err)
	assertYes(t, res, discern.Jev, "overdue")
	assertYes(t, res, discern.Clef, "overdue")
}

func (s *suite) dualUnicode(t *testing.T) {
	state := "Kunde 😡: Ich wurde doppelt belastet. Bitte erstatten Sie die doppelte Zahlung sofort! " +
		"مرحبا, شكراً. Ευχαριστώ.\u200b Zero-width, \"quotes\" and \\backslashes\\."
	ev := evaluator(t, s.jev(t), s.clef(t, clef.ModelFlash))
	res, err := s.evaluate(t, ev, discern.Request{
		Providers: discern.JevAndClef,
		State:     discern.Text(state),
		Questions: []discern.Question{
			discern.Choice("lang", discern.Text("Welche Sprache überwiegt? 🤔"),
				discern.Opt("de", discern.Text("Deutsch")),
				discern.Opt("ar", discern.Text("العربية")),
				discern.Opt("el", discern.Text("Ελληνικά"))),
			discern.Noul(qRefund, discern.Text("Möchte der Kunde eine Rückerstattung?")),
		},
	})
	requireComplete(t, res, err)
	for _, p := range []discern.Provider{discern.Jev, discern.Clef} {
		obs := requireObservation(t, res, p)
		lang, ok := obs.Choice("lang")
		require.True(t, ok, changed(res, err))
		assert.Equal(t, "de", lang.Option, changed(res, err))
		assertYes(t, res, p, qRefund)
	}
}

// dualPolicy runs the support policy on both providers with clef, whose refund
// answer is steadier than clef-flash's, and keeps the result for dual/record.
func (s *suite) dualPolicy(t *testing.T) {
	ev := evaluator(t, s.jev(t), s.clef(t, clef.ModelClef))
	req := ticketRequest(discern.JevAndClef)
	req.CorrelationID, req.EvidenceRefs = "live-dual-1", []string{"ticket:2291"}
	res, err := s.evaluate(t, ev, req, discern.WithPolicy(supportPolicy(false)))
	requireComplete(t, res, err)
	s.policyResult = res
	t.Log(describeRecommendations(res))
	msg := changed(res, err) + " recommendations: " + describeRecommendations(res)
	assert.Equal(t, discern.Accepted, res.Overall.Status, msg)
	require.Len(t, res.Recommendations, 3, msg)
	for _, r := range res.Recommendations {
		assert.Equal(t, discern.Accepted, r.Status, msg)
		assert.Contains(t, r.Reasons, discern.ReasonProvidersAgree, msg)
	}
	q, ok := res.Recommendation(qQueue)
	require.True(t, ok, msg)
	require.NotNil(t, q.Selected, msg)
	assert.Equal(t, optBilling, q.Selected.Option, msg)
	assertJevMeta(t, res, pinnedJev)
	assertClefMeta(t, res, clef.ModelClef)
}

// dualRecord replays the dual/policy result offline: the record round-trips
// byte for byte, a stricter policy re-recommends it, and Redact strips it.
func (s *suite) dualRecord(t *testing.T) {
	res := s.policyResult
	if res == nil {
		t.Skip("needs the result of dual/policy")
	}
	b, err := json.Marshal(res)
	require.NoError(t, err)
	parsed, err := discern.ParseResult(b)
	require.NoError(t, err)
	again, err := json.Marshal(parsed)
	require.NoError(t, err)
	assert.Equal(t, b, again, "a parsed record must marshal to the same bytes")

	unmeetable := supportPolicy(false)
	unmeetable.Questions[0].Checks = []discern.Check{
		{ID: "queue-unmeetable", Metric: discern.MetricChoiceProbability, Op: discern.AtMost, Threshold: 0.01},
	}
	re, err := discern.Recommend(parsed, unmeetable)
	require.NoError(t, err)
	t.Log("re-recommended: " + describeRecommendations(re))
	q, ok := re.Recommendation(qQueue)
	require.True(t, ok)
	assert.Equal(t, discern.BelowThreshold, q.Status)
	assert.Equal(t, discern.BelowThreshold, re.Overall.Status)

	red := parsed.Redact(discern.Redaction{CorrelationID: true, EvidenceRefs: true, RequestIDs: true, Usage: true})
	rb, err := json.Marshal(red)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"correlation_id", "evidence_refs", "request_ids", "usage"}, red.Redacted)
	for _, o := range res.Outcomes {
		if o.Observation == nil {
			continue
		}
		for _, id := range []string{o.Observation.RequestID, o.Observation.RayID} {
			if id != "" {
				assert.NotContains(t, string(rb), id, "a redacted record must not hold provider IDs")
			}
		}
	}
}

// dualAmbiguous sends a vague ticket; the providers may disagree, so it checks
// only that every question gets a policy outcome.
func (s *suite) dualAmbiguous(t *testing.T) {
	ev := evaluator(t, s.jev(t), s.clef(t, clef.ModelFlash))
	req := ticketRequest(discern.JevAndClef)
	req.State = discern.Text("it's broken again. fix it or I'm done paying for this.")
	res, err := s.evaluate(t, ev, req, discern.WithPolicy(supportPolicy(false)))
	requireComplete(t, res, err)
	t.Log(describeRecommendations(res))
	require.Len(t, res.Recommendations, 3)
	valid := []discern.RecommendationStatus{
		discern.Accepted, discern.BelowThreshold, discern.ReviewRequired, discern.NotApplicable, discern.Incomplete,
	}
	for _, r := range res.Recommendations {
		assert.Contains(t, valid, r.Status)
	}
}

// dualPartial pairs a refused Jev key with a working Clef: the result is
// partial, accepted with AllowPartial and incomplete without it.
func (s *suite) dualPartial(t *testing.T) {
	ev := evaluator(t, newJev(t, badJevKey), s.clef(t, clef.ModelClef))
	res, err := s.evaluate(t, ev, ticketRequest(discern.JevAndClef), discern.WithPolicy(supportPolicy(true)))
	msg := changed(res, err)
	require.ErrorIs(t, err, discern.ErrPartialFailure, msg)
	require.ErrorIs(t, err, discern.ErrRejected, msg)
	require.NotNil(t, res, msg)
	assert.Equal(t, discern.Partial, res.Status, msg)
	jo, ok := res.Outcome(discern.Jev)
	require.True(t, ok, msg)
	require.NotNil(t, jo.Failure, msg)
	assert.Equal(t, http.StatusUnauthorized, jo.Failure.StatusCode, msg)
	assertClefMeta(t, res, clef.ModelClef)

	t.Log("allow_partial: " + describeRecommendations(res))
	assert.Equal(t, discern.Accepted, res.Overall.Status, msg)
	for _, r := range res.Recommendations {
		assert.Contains(t, r.Reasons, discern.ReasonPartialSingleProvider, msg)
	}
	strict, err := discern.Recommend(res, supportPolicy(false))
	require.NoError(t, err)
	t.Log("strict: " + describeRecommendations(strict))
	assert.Equal(t, discern.Incomplete, strict.Overall.Status)
	for _, r := range strict.Recommendations {
		assert.Contains(t, r.Reasons, discern.ReasonProviderFailed)
	}
}

func (s *suite) dualBothFail(t *testing.T) {
	ev := evaluator(t, newJev(t, badJevKey), newClef(t, s.account, badClefToken, clef.ModelFlash))
	res, err := s.evaluate(t, ev, refundRequest(discern.JevAndClef, ticket))
	msg := changed(res, err)
	require.ErrorIs(t, err, discern.ErrRejected, msg)
	require.NotErrorIs(t, err, discern.ErrPartialFailure, msg)
	require.NotNil(t, res, msg)
	assert.Equal(t, discern.Failed, res.Status, msg)
	require.Len(t, res.Outcomes, 2, msg)
	for _, o := range res.Outcomes {
		require.NotNil(t, o.Failure, msg)
		assert.Equal(t, http.StatusUnauthorized, o.Failure.StatusCode, msg)
	}
	co, ok := res.Outcome(discern.Clef)
	require.True(t, ok, msg)
	assert.Contains(t, co.Failure.Codes, clefAuthError, msg)
}

// dualBatch evaluates six single-provider requests, alternating Jev and
// clef-flash, through EvaluateBatch.
func (s *suite) dualBatch(t *testing.T) {
	ev := evaluator(t, s.jev(t), s.clef(t, clef.ModelFlash))
	reqs := make([]discern.Request, 6)
	for i := range reqs {
		reqs[i] = refundRequest(alternate(i), fmt.Sprintf("Ticket %d: %s", i, ticket))
	}
	items, err := ev.EvaluateBatch(t.Context(), reqs)
	require.NoError(t, err)
	require.Len(t, items, len(reqs))
	for i, it := range items {
		s.record(it.Result)
		s.inspect(t, it.Result, it.Err)
		requireComplete(t, it.Result, it.Err)
		assert.Equal(t, reqs[i].Providers, it.Result.Providers)
		obs := requireObservation(t, it.Result, reqs[i].Providers.Providers()[0])
		_, ok := obs.Noul(qRefund)
		assert.True(t, ok, changed(it.Result, it.Err))
	}
}

// dualConcurrency makes ten concurrent calls to each provider through one
// evaluator and logs the latencies.
func (s *suite) dualConcurrency(t *testing.T) {
	ev := evaluator(t, s.jev(t), s.clef(t, clef.ModelFlash))
	type outcome struct {
		res *discern.Result
		err error
		ms  int64
	}
	outs := make([]outcome, 20)
	var wg sync.WaitGroup
	for i := range outs {
		wg.Go(func() {
			start := time.Now()
			res, err := s.call(t.Context(), ev, refundRequest(alternate(i), fmt.Sprintf("#%d %s", i, ticket)))
			outs[i] = outcome{res: res, err: err, ms: time.Since(start).Milliseconds()}
		})
	}
	wg.Wait()
	latencies := map[discern.ProviderSet][]int64{}
	for i, o := range outs {
		s.inspect(t, o.res, o.err)
		requireComplete(t, o.res, o.err)
		latencies[alternate(i)] = append(latencies[alternate(i)], o.ms)
	}
	for _, ps := range []discern.ProviderSet{discern.JevOnly, discern.ClefOnly} {
		l := latencies[ps]
		slices.Sort(l)
		t.Logf("%s: %d concurrent calls, p50 %dms, max %dms", ps, len(l), l[len(l)/2], l[len(l)-1])
	}
}

// alternate returns Jev for even i and Clef for odd i.
func alternate(i int) discern.ProviderSet {
	if i%2 == 1 {
		return discern.ClefOnly
	}
	return discern.JevOnly
}

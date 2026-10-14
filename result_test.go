package discern_test

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
)

// partialResult is a dual result in which Clef failed with a retry hint and the
// policy accepted Jev alone.
func partialResult(t *testing.T) *discern.Result {
	t.Helper()
	answers := jevAnswers()
	answers.scoreConf = nil
	jev := discerntest.Static(discern.Jev, answers.observation())
	pe := discern.NewProviderError(discern.Clef, discern.FailureRejected, nil)
	pe.StatusCode, pe.RequestID, pe.RayID, pe.Codes = 429, "cf-req-1", "8f00-SJC", []int{3040}
	pe.RetryAfter, pe.HasRetryAfter = 0, true
	clef := discerntest.Failing(discern.Clef, pe)
	pol := discern.Policy{Version: "routing-v3", AllowPartial: true, Questions: []discern.QuestionPolicy{
		{Question: qQueue, Checks: []discern.Check{{ID: "queue-jev", Provider: discern.Jev, Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.8}}},
		{Question: qSeverity, Checks: []discern.Check{{Metric: discern.MetricConfidence, Op: discern.AtLeast, Threshold: 0}}},
	}}
	ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef), discern.WithDefaultPolicy(pol))
	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevAndClef))
	require.ErrorIs(t, err, discern.ErrPartialFailure)
	return res
}

func TestResultJSONRoundTrip(t *testing.T) {
	complete := mustEvaluate(t, pairEvaluator(t), discern.JevAndClef, discern.WithPolicy(policyFor(qQueue,
		discern.Check{Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.75})))

	tests := []struct {
		name string
		res  *discern.Result
	}{
		{"complete dual with selection", complete},
		{"partial with failure and absent values", partialResult(t)},
		{"redacted", partialResult(t).Redact(discern.Redaction{CorrelationID: true, EvidenceRefs: true, RequestIDs: true, Usage: true})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.res)
			require.NoError(t, err)
			restored, err := discern.ParseResult(data)
			require.NoError(t, err)
			assert.Equal(t, tt.res, restored)
			again, err := json.Marshal(restored)
			require.NoError(t, err)
			assert.JSONEq(t, string(data), string(again))
		})
	}
}

func TestResultJSONKeepsAbsentDistinctFromZero(t *testing.T) {
	res := partialResult(t)
	data, err := json.Marshal(res)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))

	assert.Equal(t, "discern.result/v1", doc["schema_version"])
	assert.Equal(t, "partial", doc["status"])
	assert.Equal(t, []any{}, doc["redacted"])
	outcomes := doc["outcomes"].([]any)
	jevOutcome := outcomes[0].(map[string]any)
	obs := jevOutcome["observation"].(map[string]any)
	assert.Equal(t, map[string]any{"input_tokens": float64(304), "output_tokens": float64(0)}, obs["usage"])
	answers := obs["answers"].([]any)
	severity := answers[2].(map[string]any)["score"].(map[string]any)
	assert.NotContains(t, severity, "confidence")
	queue := answers[0].(map[string]any)["choice"].(map[string]any)
	assert.InDelta(t, 0.87, queue["confidence"], 0)

	failure := outcomes[1].(map[string]any)["failure"].(map[string]any)
	assert.Equal(t, map[string]any{
		"kind": "rejected", "status_code": float64(429), "request_id": "cf-req-1", "ray_id": "8f00-SJC",
		"retry_after_ns": float64(0), "codes": []any{float64(3040)},
	}, failure)
	assert.Contains(t, jevOutcome, "elapsed_ns")

	restored, err := discern.ParseResult(data)
	require.NoError(t, err)
	f := restored.Outcomes[1].Failure
	require.NotNil(t, f.RetryAfter)
	assert.Equal(t, time.Duration(0), *f.RetryAfter)
	s, ok := restored.Outcomes[0].Observation.Score(qSeverity)
	require.True(t, ok)
	assert.Nil(t, s.Confidence)
	assert.Equal(t, int64(0), *restored.Outcomes[0].Observation.Usage.OutputTokens)

	rec := recommendation(t, restored, qSeverity)
	assert.Equal(t, discern.Incomplete, rec.Status)
	assert.Equal(t, []discern.ReasonCode{discern.ReasonPartialSingleProvider, discern.ReasonMetricAbsent}, rec.Reasons)
	assert.Nil(t, rec.Checks[0].Observed)
}

func TestResultSelectedReferencesSurviveRoundTrip(t *testing.T) {
	res := partialResult(t)
	data, err := json.Marshal(res)
	require.NoError(t, err)
	restored, err := discern.ParseResult(data)
	require.NoError(t, err)
	rec := recommendation(t, restored, qQueue)
	assert.Equal(t, discern.Accepted, rec.Status)
	assert.Equal(t, &discern.Selected{Providers: []discern.Provider{discern.Jev}, Option: optBilling}, rec.Selected)
	picked := restored.SelectedAnswers(qQueue)
	require.Len(t, picked, 1)
	assert.Equal(t, optBilling, picked[0].Choice.Option)
}

func TestParseResultRejectsInconsistentRecords(t *testing.T) {
	base := partialResult(t)
	edit := func(f func(doc map[string]any)) []byte {
		data, err := json.Marshal(base)
		require.NoError(t, err)
		var doc map[string]any
		require.NoError(t, json.Unmarshal(data, &doc))
		f(doc)
		out, err := json.Marshal(doc)
		require.NoError(t, err)
		return out
	}
	outcome := func(doc map[string]any, i int) map[string]any { return doc["outcomes"].([]any)[i].(map[string]any) }
	rec := func(doc map[string]any, i int) map[string]any {
		return doc["recommendations"].([]any)[i].(map[string]any)
	}
	question := func(doc map[string]any, i int) map[string]any { return doc["questions"].([]any)[i].(map[string]any) }
	answer := func(doc map[string]any, i int) map[string]any {
		return outcome(doc, 0)["observation"].(map[string]any)["answers"].([]any)[i].(map[string]any)
	}
	overall := func(doc map[string]any) map[string]any { return doc["overall"].(map[string]any) }
	observation := func(doc map[string]any) map[string]any { return outcome(doc, 0)["observation"].(map[string]any) }
	failure := func(doc map[string]any) map[string]any { return outcome(doc, 1)["failure"].(map[string]any) }
	tests := []struct {
		name string
		data []byte
	}{
		{"not JSON", []byte(`{"schema_version":`)},
		{"another schema", edit(func(d map[string]any) { d["schema_version"] = "discern.result/v2" })},
		{"requested model with a line break", edit(func(d map[string]any) { outcome(d, 0)["requested_model"] = "jev\nforged" })},
		{"missing schema", edit(func(d map[string]any) { delete(d, "schema_version") })},
		{"unknown provider set", edit(func(d map[string]any) { d["providers"] = "jev+other" })},
		{"status does not match outcomes", edit(func(d map[string]any) { d["status"] = "complete" })},
		{"outcomes out of order", edit(func(d map[string]any) {
			o := d["outcomes"].([]any)
			o[0], o[1] = o[1], o[0]
		})},
		{"missing outcome", edit(func(d map[string]any) { d["outcomes"] = d["outcomes"].([]any)[:1] })},
		{"succeeded without observation", edit(func(d map[string]any) { delete(outcome(d, 0), "observation") })},
		{"failed with observation", edit(func(d map[string]any) { outcome(d, 1)["observation"] = outcome(d, 0)["observation"] })},
		{"answers do not match questions", edit(func(d map[string]any) {
			d["questions"].([]any)[0].(map[string]any)["options"] = []any{optBilling, optTechnical, "sales"}
		})},
		{"duplicate question", edit(func(d map[string]any) {
			d["questions"].([]any)[1].(map[string]any)["id"] = qQueue
		})},
		{"selection on a failed provider", edit(func(d map[string]any) {
			rec(d, 0)["selected"] = map[string]any{"providers": []any{"clef"}, "option": optBilling}
		})},
		{"selection with another option", edit(func(d map[string]any) {
			rec(d, 0)["selected"] = map[string]any{"providers": []any{"jev"}, "option": optTechnical}
		})},
		{"selection without option on choice", edit(func(d map[string]any) {
			rec(d, 0)["selected"] = map[string]any{"providers": []any{"jev"}}
		})},
		{"selection on a non-accepted recommendation", edit(func(d map[string]any) {
			rec(d, 1)["selected"] = map[string]any{"providers": []any{"jev"}}
		})},
		{"recommendations out of order", edit(func(d map[string]any) {
			r := d["recommendations"].([]any)
			r[0], r[1] = r[1], r[0]
		})},
		{"redacted field present", edit(func(d map[string]any) { d["redacted"] = []any{"correlation_id"} })},
		{"unknown redaction", edit(func(d map[string]any) {
			d["redacted"] = []any{"state"}
		})},
		{"unknown policy source", edit(func(d map[string]any) { d["policy"].(map[string]any)["source"] = "caller_selector" })},
		{"policy missing for client default", edit(func(d map[string]any) { d["policy"].(map[string]any)["policy"] = nil })},
		{"invalid stored policy", edit(func(d map[string]any) {
			d["policy"].(map[string]any)["policy"].(map[string]any)["version"] = ""
		})},
		{"answers out of request order", edit(func(d map[string]any) {
			a := outcome(d, 0)["observation"].(map[string]any)["answers"].([]any)
			a[0], a[1] = a[1], a[0]
		})},
		{"probabilities out of option order", edit(func(d map[string]any) {
			p := answer(d, 0)["choice"].(map[string]any)["probabilities"].([]any)
			p[0], p[1] = p[1], p[0]
		})},
		{"probabilities out of level order", edit(func(d map[string]any) {
			p := answer(d, 2)["score"].(map[string]any)["probabilities"].([]any)
			p[1], p[2] = p[2], p[1]
		})},
		{"probabilities that do not sum to one", edit(func(d map[string]any) {
			answer(d, 0)["choice"].(map[string]any)["probabilities"].([]any)[1].(map[string]any)["probability"] = 0.5
		})},
		{"answer missing", edit(func(d map[string]any) {
			o := outcome(d, 0)["observation"].(map[string]any)
			o["answers"] = o["answers"].([]any)[:2]
		})},
		{"observation without answers", edit(func(d map[string]any) { delete(observation(d), "answers") })},
		{"observation with null answers", edit(func(d map[string]any) { observation(d)["answers"] = nil })},
		{"negative usage", edit(func(d map[string]any) {
			outcome(d, 0)["observation"].(map[string]any)["usage"] = map[string]any{"input_tokens": -1}
		})},
		{"unknown outcome status", edit(func(d map[string]any) { outcome(d, 1)["status"] = "retried" })},
		{"failure without kind", edit(func(d map[string]any) { delete(outcome(d, 1)["failure"].(map[string]any), "kind") })},
		{"no questions", edit(func(d map[string]any) { d["questions"] = []any{} })},
		{"invalid question ID", edit(func(d map[string]any) { question(d, 1)["id"] = "the outage" })},
		{"unknown kind", edit(func(d map[string]any) { question(d, 1)["kind"] = "rank" })},
		{"choice with levels", edit(func(d map[string]any) { question(d, 0)["levels"] = []any{"a", "b"} })},
		{"score with options", edit(func(d map[string]any) { question(d, 2)["options"] = []any{"a", "b"} })},
		{"noul with options", edit(func(d map[string]any) { question(d, 1)["options"] = []any{"a", "b"} })},
		{"one option", edit(func(d map[string]any) { question(d, 0)["options"] = []any{optBilling} })},
		{"eleven levels", edit(func(d map[string]any) {
			levels := make([]any, 11)
			for i := range levels {
				levels[i] = fmt.Sprintf("l%d", i)
			}
			question(d, 2)["levels"] = levels
		})},
		{"duplicate option", edit(func(d map[string]any) { question(d, 0)["options"] = []any{optBilling, optBilling, "account"} })},
		{"option ID with control character", edit(func(d map[string]any) { question(d, 0)["options"] = []any{optBilling, "tech\u0000", "account"} })},
		{"duplicate redaction", edit(func(d map[string]any) {
			d["correlation_id"] = ""
			d["redacted"] = []any{"correlation_id", "correlation_id"}
		})},
		{"redacted request IDs present", edit(func(d map[string]any) { d["redacted"] = []any{"request_ids"} })},
		{"redacted failure request ID present", edit(func(d map[string]any) {
			delete(outcome(d, 0)["observation"].(map[string]any), "request_id")
			d["redacted"] = []any{"request_ids"}
		})},
		{"probabilities missing an option", edit(func(d map[string]any) {
			c := answer(d, 0)["choice"].(map[string]any)
			c["probabilities"] = c["probabilities"].([]any)[:2]
		})},
		{"probabilities missing a level", edit(func(d map[string]any) {
			sc := answer(d, 2)["score"].(map[string]any)
			sc["probabilities"] = sc["probabilities"].([]any)[:2]
		})},
		{"score probability out of range", edit(func(d map[string]any) {
			answer(d, 2)["score"].(map[string]any)["probabilities"].([]any)[0].(map[string]any)["probability"] = 1.5
		})},
		{"duplicate option among many", edit(func(d map[string]any) {
			opts := make([]any, 40)
			for i := range opts {
				opts[i] = fmt.Sprintf("o%d", i)
			}
			opts[39] = "o3"
			question(d, 0)["options"] = opts
		})},
		{"redacted usage present", edit(func(d map[string]any) { d["redacted"] = []any{"usage"} })},
		{"redacted evidence present", edit(func(d map[string]any) { d["redacted"] = []any{"evidence_refs"} })},
		{"policy present without a source policy", edit(func(d map[string]any) { d["policy"].(map[string]any)["source"] = "none" })},
		{"stored policy does not fit the questions", edit(func(d map[string]any) {
			pq := d["policy"].(map[string]any)["policy"].(map[string]any)["questions"].([]any)
			pq[0].(map[string]any)["checks"].([]any)[0].(map[string]any)["metric"] = "noul"
		})},
		{"missing recommendation", edit(func(d map[string]any) { d["recommendations"] = d["recommendations"].([]any)[:2] })},
		{"accepted without selection", edit(func(d map[string]any) { delete(rec(d, 0), "selected") })},
		{"selection without providers", edit(func(d map[string]any) {
			rec(d, 0)["selected"] = map[string]any{"providers": []any{}, "option": optBilling}
		})},
		{"selection repeats a provider", edit(func(d map[string]any) {
			rec(d, 0)["selected"] = map[string]any{"providers": []any{"jev", "jev"}, "option": optBilling}
		})},
		{"selection of an unselected provider", edit(func(d map[string]any) {
			rec(d, 0)["selected"] = map[string]any{"providers": []any{"other"}, "option": optBilling}
		})},
		{"selection with an option on a numeric question", edit(func(d map[string]any) {
			rec(d, 2)["status"] = "accepted"
			rec(d, 2)["selected"] = map[string]any{"providers": []any{"jev"}, "option": "degraded"}
		})},
		{"no overall", edit(func(d map[string]any) { delete(d, "overall") })},
		{"overall without status", edit(func(d map[string]any) { overall(d)["status"] = "" })},
		{"overall status that does not aggregate", edit(func(d map[string]any) { overall(d)["status"] = "accepted" })},
		{"overall missing an uncovered question", edit(func(d map[string]any) { overall(d)["uncovered"] = []any{} })},
		{"overall listing a covered question", edit(func(d map[string]any) { overall(d)["uncovered"] = []any{qOutage, qSeverity} })},
		{"overall listing another question", edit(func(d map[string]any) { overall(d)["uncovered"] = []any{qQueue} })},
		{"recommendation without status", edit(func(d map[string]any) { rec(d, 2)["status"] = "" })},
		{"covered question with no rule", edit(func(d map[string]any) {
			rec(d, 2)["status"] = "no_rule"
			overall(d)["uncovered"] = []any{qOutage, qSeverity}
			overall(d)["status"] = "accepted"
		})},
		{"covered question with the question_not_in_policy reason", edit(func(d map[string]any) {
			rec(d, 2)["reasons"] = []any{"metric_absent", "question_not_in_policy"}
		})},
		{"covered question with the no_policy reason", edit(func(d map[string]any) {
			rec(d, 0)["reasons"] = []any{"no_policy", "thresholds_met"}
		})},
		{"uncovered question with a policy status", edit(func(d map[string]any) {
			rec(d, 1)["status"] = "incomplete"
			overall(d)["uncovered"] = []any{}
		})},
		{"uncovered question with the no_policy reason", edit(func(d map[string]any) { rec(d, 1)["reasons"] = []any{"no_policy"} })},
		{"uncovered question with an extra reason", edit(func(d map[string]any) {
			rec(d, 1)["reasons"] = []any{"question_not_in_policy", "metric_absent"}
		})},
		{"uncovered question without its reason", edit(func(d map[string]any) { rec(d, 1)["reasons"] = []any{} })},
		{"negative elapsed time", edit(func(d map[string]any) { d["elapsed_ns"] = -1 })},
		{"negative outcome elapsed time", edit(func(d map[string]any) { outcome(d, 1)["elapsed_ns"] = -5 })},
		{"correlation ID with a line break", edit(func(d map[string]any) { d["correlation_id"] = "ticket\nlevel=error" })},
		{"correlation ID too long", edit(func(d map[string]any) { d["correlation_id"] = strings.Repeat("c", 257) })},
		{"correlation ID with a format character", edit(func(d map[string]any) { d["correlation_id"] = "ticket\u202e" })},
		{"too many evidence refs", edit(func(d map[string]any) {
			refs := make([]any, 33)
			for i := range refs {
				refs[i] = fmt.Sprintf("kb:%d", i)
			}
			d["evidence_refs"] = refs
		})},
		{"evidence ref with a control character", edit(func(d map[string]any) { d["evidence_refs"] = []any{"kb:\u0007"} })},
		{"empty evidence ref", edit(func(d map[string]any) { d["evidence_refs"] = []any{""} })},
		{"observation model with a line break", edit(func(d map[string]any) { observation(d)["model"] = "jev\nlevel=error" })},
		{"observation model too long", edit(func(d map[string]any) { observation(d)["model"] = strings.Repeat("m", 257) })},
		{"observation request ID with a space", edit(func(d map[string]any) { observation(d)["request_id"] = "req jev" })},
		{"observation ray ID with non-ASCII", edit(func(d map[string]any) { observation(d)["ray_id"] = "8f00-S\u00e9C" })},
		{"failure request ID with a line break", edit(func(d map[string]any) { failure(d)["request_id"] = "cf\nreq" })},
		{"failure ray ID too long", edit(func(d map[string]any) { failure(d)["ray_id"] = strings.Repeat("r", 257) })},
	}
	valid, err := json.Marshal(base)
	require.NoError(t, err)
	_, err = discern.ParseResult(valid)
	require.NoError(t, err, "the unedited record is valid")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := discern.ParseResult(tt.data)
			require.ErrorIs(t, err, discern.ErrInvalidRecord)
			assert.Nil(t, res)
			ve := testutil.ValidationError(t, err)
			assert.NotEmpty(t, ve.Field)
			for _, s := range []string{"ticket-1042", "kb:article-17", "req-jev", "cf-req-1"} {
				assert.NotContains(t, err.Error(), s, "record errors name fields, never values")
			}
		})
	}
}

// editRecord returns res encoded as JSON after f has edited the decoded document.
func editRecord(t *testing.T, res *discern.Result, f func(doc map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(mustJSON(t, res)), &doc))
	f(doc)
	return []byte(mustJSON(t, doc))
}

func TestParseResultChecksVerdictsAgainstThePolicy(t *testing.T) {
	jev, _ := staticPair()
	ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithDefaultPolicy(policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.8))))
	none := mustEvaluate(t, testutil.Evaluator(t, discern.WithAdapter(jev)), discern.JevOnly)
	disabled := mustEvaluate(t, ev, discern.JevOnly, discern.WithoutPolicy())
	elsewhere := mustEvaluate(t, ev, discern.JevOnly, discern.WithPolicy(policyFor("other", check(discern.MetricNoul, discern.AtMost, 0.1))))
	require.Equal(t, discern.PolicyNone, none.Policy.Source)
	require.Equal(t, discern.PolicyDisabled, disabled.Policy.Source)

	rec := func(doc map[string]any, i int) map[string]any {
		return doc["recommendations"].([]any)[i].(map[string]any)
	}
	accept := func(doc map[string]any) {
		r := rec(doc, 0)
		r["status"], r["reasons"] = "accepted", []any{"thresholds_met"}
		r["selected"] = map[string]any{"providers": []any{"jev"}, "option": optBilling}
		doc["overall"] = map[string]any{"status": "accepted", "uncovered": []any{qOutage, qSeverity}}
	}
	tests := []struct {
		name string
		res  *discern.Result
		edit func(doc map[string]any)
	}{
		{"no policy with an accepted recommendation", none, accept},
		{"disabled policy with an accepted recommendation", disabled, accept},
		{"no policy without the no_policy reason", none, func(d map[string]any) { rec(d, 0)["reasons"] = []any{} }},
		{"no policy with the question_not_in_policy reason", none, func(d map[string]any) { rec(d, 1)["reasons"] = []any{"question_not_in_policy"} }},
		{"no policy with an extra reason", disabled, func(d map[string]any) { rec(d, 2)["reasons"] = []any{"no_policy", "metric_absent"} }},
		{"no policy with an accepted overall", none, func(d map[string]any) { d["overall"].(map[string]any)["status"] = "accepted" }},
		{"accepted overall while every question is uncovered", elsewhere, func(d map[string]any) {
			d["overall"] = map[string]any{"status": "accepted", "uncovered": []any{}}
		}},
		{"no rule question covered by the policy", elsewhere, func(d map[string]any) {
			d["policy"].(map[string]any)["policy"].(map[string]any)["questions"].([]any)[0].(map[string]any)["question"] = qOutage
		}},
	}
	for _, base := range []*discern.Result{none, disabled, elsewhere} {
		_, err := discern.ParseResult([]byte(mustJSON(t, base)))
		require.NoError(t, err, "the unedited %s record is valid", base.Policy.Source)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := discern.ParseResult(editRecord(t, tt.res, tt.edit))
			require.ErrorIs(t, err, discern.ErrInvalidRecord)
			assert.Nil(t, res)
		})
	}
}

func TestParseResultToleratesUnknownStatusesAndReasons(t *testing.T) {
	// Codes from a later v1 version are kept, and unknown statuses rank above
	// every known one when the overall verdict is checked.
	rec := func(doc map[string]any, i int) map[string]any {
		return doc["recommendations"].([]any)[i].(map[string]any)
	}
	base := partialResult(t)
	tests := []struct {
		name    string
		edit    func(doc map[string]any)
		ok      bool
		status  discern.RecommendationStatus // the restored severity status
		reasons []discern.ReasonCode         // the restored severity reasons
		overall discern.RecommendationStatus
	}{
		{
			name: "unknown reason code", ok: true,
			edit: func(d map[string]any) {
				rec(d, 2)["reasons"] = []any{"partial_single_provider", "metric_absent", "future_reason"}
			},
			status:  discern.Incomplete,
			reasons: []discern.ReasonCode{discern.ReasonPartialSingleProvider, discern.ReasonMetricAbsent, "future_reason"},
			overall: discern.Incomplete,
		},
		{
			name: "unknown status aggregated", ok: true,
			edit: func(d map[string]any) {
				rec(d, 2)["status"] = "escalated"
				d["overall"].(map[string]any)["status"] = "escalated"
			},
			status:  "escalated",
			reasons: []discern.ReasonCode{discern.ReasonPartialSingleProvider, discern.ReasonMetricAbsent},
			overall: "escalated",
		},
		{name: "unknown status not aggregated", edit: func(d map[string]any) { rec(d, 2)["status"] = "escalated" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := discern.ParseResult(editRecord(t, base, tt.edit))
			if !tt.ok {
				require.ErrorIs(t, err, discern.ErrInvalidRecord)
				return
			}
			require.NoError(t, err)
			severity := recommendation(t, res, qSeverity)
			assert.Equal(t, tt.status, severity.Status)
			assert.Equal(t, tt.reasons, severity.Reasons)
			assert.Equal(t, tt.overall, res.Overall.Status)
		})
	}
}

func TestParseResultOutOfOrderRecordWouldMisreportNearest(t *testing.T) {
	// A record whose level probabilities are out of order would make Nearest pick
	// the wrong level, so ParseResult rejects it instead of restoring it.
	res := partialResult(t)
	s := res.Outcomes[0].Observation.Answers[2].Score
	assert.Equal(t, "degraded", s.Nearest())
	s.Probabilities[1], s.Probabilities[2] = s.Probabilities[2], s.Probabilities[1]
	assert.Equal(t, levelBlocked, s.Nearest())
	data, err := json.Marshal(res)
	require.NoError(t, err)
	_, err = discern.ParseResult(data)
	require.ErrorIs(t, err, discern.ErrInvalidRecord)
}

func TestResultFailureCodesRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		codes []int
		want  []int
	}{
		{"absent", nil, nil},
		{"empty", []int{}, nil},
		{"present", []int{3040, 5004}, []int{3040, 5004}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pe := discern.NewProviderError(discern.Jev, discern.FailureRejected, nil)
			pe.Codes = tt.codes
			ev := testutil.Evaluator(t, discern.WithAdapter(discerntest.Failing(discern.Jev, pe)))
			res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly))
			require.ErrorIs(t, err, discern.ErrRejected)
			assert.Equal(t, tt.want, res.Outcomes[0].Failure.Codes)
			data, err := json.Marshal(res)
			require.NoError(t, err)
			restored, err := discern.ParseResult(data)
			require.NoError(t, err)
			assert.Equal(t, res, restored)
		})
	}
}

func TestParseResultRestoresAbsentListsAsEmpty(t *testing.T) {
	res := partialResult(t)
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(mustJSON(t, res)), &doc))
	for _, key := range []string{"evidence_refs", "redacted"} {
		delete(doc, key)
	}
	delete(doc["policy"].(map[string]any), "unmatched")
	// A NoRule recommendation needs its one reason; the covered ones may omit
	// theirs.
	for _, r := range doc["recommendations"].([]any) {
		if r.(map[string]any)["status"] != "no_rule" {
			delete(r.(map[string]any), "reasons")
		}
		delete(r.(map[string]any), "checks")
	}
	data, err := json.Marshal(doc)
	require.NoError(t, err)
	restored, err := discern.ParseResult(data)
	require.NoError(t, err)
	assert.Equal(t, []string{}, restored.Redacted)
	assert.Equal(t, []string{}, restored.Policy.Unmatched)
	assert.Equal(t, []string{qOutage}, restored.Overall.Uncovered)
	assert.Equal(t, []string{"kb:article-17"}, res.EvidenceRefs)
	assert.Equal(t, []string{}, restored.EvidenceRefs)
	for _, rec := range restored.Recommendations {
		if rec.Status != discern.NoRule {
			assert.Equal(t, []discern.ReasonCode{}, rec.Reasons)
		}
		assert.Equal(t, []discern.CheckResult{}, rec.Checks)
	}
	assert.JSONEq(t, `[]`, mustJSON(t, restored.Recommendations[0].Checks))

	// An absent uncovered list is restored as empty, which a record without
	// NoRule questions needs.
	covered, err := discern.Recommend(res, discern.Policy{Version: "v1", Questions: []discern.QuestionPolicy{
		{Question: qQueue, Checks: []discern.Check{check(discern.MetricChoiceProbability, discern.AtLeast, 0.5)}},
		{Question: qOutage, Checks: []discern.Check{check(discern.MetricNoul, discern.AtMost, 0.5)}},
		{Question: qSeverity, Checks: []discern.Check{check(discern.MetricScore, discern.AtMost, 1.5)}},
	}})
	require.NoError(t, err)
	var coveredDoc map[string]any
	require.NoError(t, json.Unmarshal([]byte(mustJSON(t, covered)), &coveredDoc))
	delete(coveredDoc["overall"].(map[string]any), "uncovered")
	data, err = json.Marshal(coveredDoc)
	require.NoError(t, err)
	restoredCovered, err := discern.ParseResult(data)
	require.NoError(t, err)
	assert.Equal(t, []string{}, restoredCovered.Overall.Uncovered)
}

func TestResultMethodsOnHandBuiltResults(t *testing.T) {
	var zero discern.Result
	require.NotPanics(t, func() {
		assert.Equal(t, &zero, zero.Clone())
		assert.Equal(t, []string{"usage"}, zero.Redact(discern.Redaction{Usage: true}).Redacted)
		assert.Nil(t, zero.SelectedAnswers(qQueue))
	})
	_, err := discern.Recommend(&zero, policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.5)))
	require.ErrorIs(t, err, discern.ErrInvalidRecord)

	// A selection that points at a failed or missing provider yields no answers.
	res := partialResult(t)
	res.Recommendations[0].Selected = &discern.Selected{Providers: []discern.Provider{discern.Clef, "other"}, Option: optBilling}
	assert.Nil(t, res.SelectedAnswers(qQueue))

	// Copies keep nil, empty and long selections as they are.
	for _, providers := range [][]discern.Provider{nil, {}, {discern.Jev, discern.Clef, discern.Jev}} {
		res.Recommendations[0].Selected = &discern.Selected{Providers: providers, Option: optBilling}
		c := res.Clone()
		assert.Equal(t, providers, c.Recommendations[0].Selected.Providers)
		rec, ok := res.Recommendation(qQueue)
		require.True(t, ok)
		assert.Equal(t, providers, rec.Selected.Providers)
		if len(providers) > 0 {
			c.Recommendations[0].Selected.Providers[0] = mutated
			assert.Equal(t, discern.Jev, res.Recommendations[0].Selected.Providers[0])
		}
	}
}

func TestParseResultIgnoresUnknownFields(t *testing.T) {
	data, err := json.Marshal(partialResult(t))
	require.NoError(t, err)
	extended := strings.Replace(string(data), `"schema_version"`, `"future_field":{"x":1},"schema_version"`, 1)
	res, err := discern.ParseResult([]byte(extended))
	require.NoError(t, err)
	assert.Equal(t, discern.Partial, res.Status)
}

func TestResultRedact(t *testing.T) {
	res := partialResult(t)
	redacted := res.Redact(discern.Redaction{CorrelationID: true, RequestIDs: true})
	assert.Equal(t, []string{"correlation_id", "request_ids"}, redacted.Redacted)
	assert.Empty(t, redacted.CorrelationID)
	assert.Empty(t, redacted.Outcomes[0].Observation.RequestID)
	assert.Empty(t, redacted.Outcomes[1].Failure.RequestID)
	assert.Empty(t, redacted.Outcomes[1].Failure.RayID)
	assert.NotNil(t, redacted.Outcomes[0].Observation.Usage)
	assert.Equal(t, []string{"kb:article-17"}, redacted.EvidenceRefs)

	assert.Equal(t, "ticket-1042", res.CorrelationID)
	assert.Equal(t, "req-jev-1.13.0", res.Outcomes[0].Observation.RequestID)
	assert.Empty(t, res.Redacted)

	twice := redacted.Redact(discern.Redaction{CorrelationID: true, Usage: true, EvidenceRefs: true})
	assert.Equal(t, []string{"correlation_id", "request_ids", "evidence_refs", "usage"}, twice.Redacted)
	assert.Nil(t, twice.Outcomes[0].Observation.Usage)
	assert.Empty(t, twice.EvidenceRefs)

	data, err := json.Marshal(twice)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "ticket-1042")
	assert.NotContains(t, string(data), "kb:article-17")
	assert.NotContains(t, string(data), "cf-req-1")
	_, err = discern.ParseResult(data)
	require.NoError(t, err)
}

func TestResultRedactAllCombinations(t *testing.T) {
	complete := mustEvaluate(t, pairEvaluator(t), discern.JevAndClef)
	failed, _ := testutil.Evaluator(t, discern.WithAdapter(discerntest.Failing(discern.Jev, nil))).Evaluate(t.Context(), ticketRequest(discern.JevOnly))
	require.NotNil(t, failed)
	results := []struct {
		name string
		res  *discern.Result
	}{{"complete", complete}, {"partial", partialResult(t)}, {"failed", failed}}
	for _, r := range results {
		for mask := range 16 {
			opts := discern.Redaction{CorrelationID: mask&1 != 0, EvidenceRefs: mask&2 != 0, RequestIDs: mask&4 != 0, Usage: mask&8 != 0}
			t.Run(fmt.Sprintf("%s %+v", r.name, opts), func(t *testing.T) {
				before := mustJSON(t, r.res)
				red := r.res.Redact(opts)
				assert.JSONEq(t, before, mustJSON(t, r.res), "Redact never modifies its receiver")
				assert.Equal(t, redactionNames(opts), red.Redacted)
				assertRedacted(t, opts, red)

				restored, err := discern.ParseResult([]byte(mustJSON(t, red)))
				require.NoError(t, err)
				assert.Equal(t, red, restored)
				assert.Equal(t, red, red.Redact(opts), "redacting twice changes nothing")
				assert.Equal(t, r.res.Recommendations, red.Recommendations)
			})
		}
	}
}

// redactionNames lists the names Redact records for opts, in its order.
func redactionNames(opts discern.Redaction) []string {
	names := []string{}
	for _, f := range []struct {
		on   bool
		name string
	}{{opts.CorrelationID, "correlation_id"}, {opts.EvidenceRefs, "evidence_refs"}, {opts.RequestIDs, "request_ids"}, {opts.Usage, "usage"}} {
		if f.on {
			names = append(names, f.name)
		}
	}
	return names
}

// assertRedacted checks that every field opts selects is absent from red.
func assertRedacted(t *testing.T, opts discern.Redaction, red *discern.Result) {
	t.Helper()
	if opts.CorrelationID {
		assert.Empty(t, red.CorrelationID)
	}
	if opts.EvidenceRefs {
		assert.Empty(t, red.EvidenceRefs)
	}
	for _, o := range red.Outcomes {
		if opts.RequestIDs && o.Observation != nil {
			assert.Empty(t, o.Observation.RequestID+o.Observation.RayID)
		}
		if opts.RequestIDs && o.Failure != nil {
			assert.Empty(t, o.Failure.RequestID+o.Failure.RayID)
		}
		if opts.Usage && o.Observation != nil {
			assert.Nil(t, o.Observation.Usage)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return string(data)
}

func TestResultAccessorsReturnCopies(t *testing.T) {
	res := partialResult(t)
	obs, ok := res.Observation(discern.Jev)
	require.True(t, ok)
	q, ok := obs.Choice(qQueue)
	require.True(t, ok)
	q.Probabilities[0].Probability = 0.01
	*q.Confidence = 0.01
	obs.Answers[0].QuestionID = mutated

	rec := recommendation(t, res, qQueue)
	rec.Selected.Providers[0] = discern.Clef
	rec.Reasons[0] = mutated

	picked := res.SelectedAnswers(qQueue)
	picked[0].Choice.Option = mutated

	again, ok := res.Observation(discern.Jev)
	require.True(t, ok)
	q2, ok := again.Choice(qQueue)
	require.True(t, ok)
	assert.InDelta(t, 0.91, q2.Probabilities[0].Probability, 0)
	assert.InDelta(t, 0.87, *q2.Confidence, 0)
	rec2 := recommendation(t, res, qQueue)
	assert.Equal(t, discern.Jev, rec2.Selected.Providers[0])
	assert.Equal(t, discern.ReasonPartialSingleProvider, rec2.Reasons[0])
	assert.Equal(t, optBilling, res.SelectedAnswers(qQueue)[0].Choice.Option)

	clone := res.Clone()
	clone.Outcomes[0].Observation.Answers[0].Choice.Option = mutated
	assert.Equal(t, optBilling, res.Outcomes[0].Observation.Answers[0].Choice.Option)

	_, ok = res.Recommendation("missing")
	assert.False(t, ok)
	_, ok = res.Outcome("other")
	assert.False(t, ok)
	assert.Nil(t, res.SelectedAnswers(qOutage))

	o, ok := res.Outcome(discern.Clef)
	require.True(t, ok)
	*o.Failure.RetryAfter = time.Hour
	o.Failure.Codes[0] = 1
	again2, ok := res.Outcome(discern.Clef)
	require.True(t, ok)
	assert.Equal(t, time.Duration(0), *again2.Failure.RetryAfter)
	assert.Equal(t, []int{3040}, again2.Failure.Codes)

	n, ok := obs.Noul(qOutage)
	require.True(t, ok)
	assert.InDelta(t, 0.07, n.Value, 0)
	_, ok = obs.Noul(qQueue)
	assert.False(t, ok)
	_, ok = obs.Noul("missing")
	assert.False(t, ok)
	_, ok = obs.Answer("missing")
	assert.False(t, ok)
}

// mutateEverything writes through every pointer and slice of a result and
// appends to the slices that share a backing array with their neighbors.
func mutateEverything(c *discern.Result) {
	c.EvidenceRefs[0] = mutated
	c.Questions[0].Options[0] = mutated
	c.Questions[0].Options = append(c.Questions[0].Options, "appended")
	c.Questions[2].Levels = append(c.Questions[2].Levels, "appended")
	for _, o := range c.Outcomes {
		*o.Observation.Usage.InputTokens = -1
		for _, a := range o.Observation.Answers {
			switch {
			case a.Choice != nil:
				*a.Choice.Confidence = -1
				a.Choice.Probabilities[0].Probability = -1
				a.Choice.Probabilities = append(a.Choice.Probabilities, discern.OptionProbability{Option: "appended"})
			case a.Score != nil:
				*a.Score.Confidence = -1
				a.Score.Probabilities[0].Probability = -1
			case a.Noul != nil:
				a.Noul.Value = -1
			}
		}
	}
	c.Policy.Policy.Questions[0].Checks[0].Threshold = -1
	c.Policy.Policy.Questions[1].Agreement.MaxDifference = -1
	for i := range c.Recommendations {
		rec := &c.Recommendations[i]
		rec.Reasons = append(rec.Reasons, "appended")
		rec.Checks = append(rec.Checks, discern.CheckResult{RuleID: "appended"})
		if rec.Checks[0].Observed != nil {
			*rec.Checks[0].Observed = -1
		}
		if rec.Selected != nil {
			rec.Selected.Providers[0] = mutated
			rec.Selected.Providers = append(rec.Selected.Providers, "appended")
		}
	}
	c.Overall.Uncovered = append(c.Overall.Uncovered, "appended")
}

func TestResultCloneIsDeepAndIsolated(t *testing.T) {
	req, obs := largeRequest(6, 4)
	obs.Usage = &discern.Usage{InputTokens: new(int64(12))}
	req.Providers = discern.JevAndClef
	req.CorrelationID, req.EvidenceRefs = "ticket-7", []string{"kb:1", "kb:2"}
	clefObs := obs.Clone()
	clefObs.Usage = &discern.Usage{InputTokens: new(int64(10)), OutputTokens: new(int64(1)), Neurons: new(1.5)}
	pol := largePolicy(6)
	pol.Questions[1].Agreement = &discern.Agreement{MaxDifference: 0.1}
	pol.Questions[2].Agreement = &discern.Agreement{MaxDifference: 0.1}
	ev := testutil.Evaluator(t, discern.WithAdapter(discerntest.Static(discern.Jev, obs)), discern.WithAdapter(discerntest.Static(discern.Clef, clefObs)))
	res, err := ev.Evaluate(t.Context(), req, discern.WithPolicy(pol))
	require.NoError(t, err)
	before, err := json.Marshal(res)
	require.NoError(t, err)

	for _, tt := range []struct {
		name string
		copy func() *discern.Result
	}{
		{"clone", res.Clone},
		{"redact nothing", func() *discern.Result { return res.Redact(discern.Redaction{}) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.copy()
			require.Equal(t, res, c)

			mutateEverything(c)

			after, err := json.Marshal(res)
			require.NoError(t, err)
			assert.JSONEq(t, string(before), string(after), "the original is untouched")

			// Had an append spilled into a neighbor's backing array, that neighbor's
			// first element would read "appended".
			for i, rec := range c.Recommendations {
				orig := res.Recommendations[i]
				assert.Equal(t, append(slices.Clone(orig.Reasons), "appended"), rec.Reasons)
				assert.Equal(t, orig.Checks[0].RuleID, rec.Checks[0].RuleID)
				assert.Len(t, rec.Checks, len(orig.Checks)+1)
			}
			assert.Equal(t, res.Questions[2].Levels[0], c.Questions[2].Levels[0])
			assert.Equal(t, res.Questions[3].Options[0], c.Questions[3].Options[0])
			assert.Equal(t, "option-000", c.Outcomes[0].Observation.Answers[3].Choice.Probabilities[0].Option)
		})
	}

	// The records Evaluate builds share backing arrays between questions too.
	for i := range res.Recommendations[:len(res.Recommendations)-1] {
		next := slices.Clone(res.Recommendations[i+1].Reasons)
		res.Recommendations[i].Reasons = append(res.Recommendations[i].Reasons, "appended")
		res.Recommendations[i].Checks = append(res.Recommendations[i].Checks, discern.CheckResult{RuleID: "appended"})
		assert.Equal(t, next, res.Recommendations[i+1].Reasons)
		assert.NotEqual(t, "appended", res.Recommendations[i+1].Checks[0].RuleID)
	}
	q0 := res.Outcomes[0].Observation.Answers[0].Choice
	q0.Probabilities = append(q0.Probabilities, discern.OptionProbability{Option: "appended"})
	assert.Equal(t, "option-000", res.Outcomes[0].Observation.Answers[3].Choice.Probabilities[0].Option)
}

func TestScoreAnswerNearest(t *testing.T) {
	levels := []discern.LevelProbability{{Level: levelCosmetic}, {Level: "degraded"}, {Level: levelBlocked}}
	tests := []struct {
		value float64
		want  string
	}{
		{0, levelCosmetic},
		{0.49, levelCosmetic},
		{0.49999999999999994, levelCosmetic}, // the largest float64 below 0.5; Floor(v+0.5) rounds it up
		{0.5, "degraded"},
		{1.05, "degraded"},
		{1.5, levelBlocked},
		{2, levelBlocked},
		{2.4, levelBlocked},
		{-0.2, levelCosmetic},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, discern.ScoreAnswer{Value: tt.value, Probabilities: levels}.Nearest(), "value %v", tt.value)
	}
	assert.Empty(t, discern.ScoreAnswer{Value: 1}.Nearest())
}

func TestScoreAnswerNearestClampsOutOfRangeValues(t *testing.T) {
	// Converting these to int directly is implementation-defined and differs
	// between platforms; Nearest must clamp first.
	levels := []discern.LevelProbability{{Level: levelCosmetic}, {Level: "degraded"}, {Level: levelBlocked}}
	tests := []struct {
		name  string
		value float64
		want  string
	}{
		{"just above the top", 2.5, levelBlocked},
		{"beyond int64", 1e19, levelBlocked},
		{"huge", math.MaxFloat64, levelBlocked},
		{"positive infinity", math.Inf(1), levelBlocked},
		{"beyond negative int64", -1e19, levelCosmetic},
		{"negative infinity", math.Inf(-1), levelCosmetic},
		{"NaN", math.NaN(), levelCosmetic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, discern.ScoreAnswer{Value: tt.value, Probabilities: levels}.Nearest())
		})
	}
}

package discern_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
)

// seedResult returns a valid encoded record for fuzz seeds.
func seedResult(f *testing.F) []byte {
	f.Helper()
	pol := policyFor(qQueue, check(discern.MetricChoiceProbability, discern.AtLeast, 0.8))
	res := mustEvaluate(f, pairEvaluator(f), discern.JevAndClef, discern.WithPolicy(pol))
	data, err := json.Marshal(res)
	require.NoError(f, err)
	return data
}

func FuzzParseResult(f *testing.F) {
	f.Add(seedResult(f))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"schema_version":"discern.result/v1","providers":"jev","status":"complete","questions":[],"outcomes":[]}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, data []byte) {
		require.NotPanics(t, func() {
			res, err := discern.ParseResult(data)
			if err != nil {
				require.ErrorIs(t, err, discern.ErrInvalidRecord)
				require.Nil(t, res)
				return
			}
			// An accepted record re-encodes to a fixed point and replays policy.
			again, err := json.Marshal(res)
			require.NoError(t, err)
			restored, err := discern.ParseResult(again)
			require.NoError(t, err)
			third, err := json.Marshal(restored)
			require.NoError(t, err)
			require.Equal(t, string(again), string(third))
			require.Equal(t, again, mustMarshalJSON(t, res.Clone()))
			if res.Policy.Policy != nil {
				_, err = discern.Recommend(res, *res.Policy.Policy)
				require.NoError(t, err)
			}
		})
	})
}

func FuzzObservationValidation(f *testing.F) {
	f.Add(0.91, 0.06, 0.03, 0.87, 1.05, 0.0, 0.95, 0.05, uint8(0))
	f.Add(0.5, 0.5, 0.0, 1.0, 2.0, 0.0, 0.0, 1.0, uint8(0xff))
	f.Add(-0.1, 1.1, 0.0, 2.0, -1.0, 0.5, 0.5, 0.0, uint8(0x2a))
	f.Fuzz(func(t *testing.T, b, tech, acct, conf, score, l0, l1, l2 float64, order uint8) {
		answers := jevAnswers()
		answers.probs = [3]float64{b, tech, acct}
		answers.conf = &conf
		answers.score = score
		answers.levels = [3]float64{l0, l1, l2}
		inOrder := answers.observation()

		// The same answers delivered out of order must be judged identically.
		shuffled := shuffledObservation(inOrder, order)

		var results [2]*discern.Result
		var errs [2]error
		for i, obs := range []discern.Observation{inOrder, shuffled} {
			ev, err := discern.New(discern.WithAdapter(discerntest.Static(discern.Jev, obs)))
			require.NoError(t, err)
			require.NotPanics(t, func() { results[i], errs[i] = ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly)) })
			require.NotNil(t, results[i])
		}
		if errs[0] != nil {
			require.ErrorIs(t, errs[0], discern.ErrMalformedResponse)
			require.ErrorIs(t, errs[1], discern.ErrMalformedResponse, "a reordered observation is not more acceptable")
			return
		}
		require.NoError(t, errs[1], "a reordered observation is not less acceptable")
		got := results[0].Outcomes[0].Observation
		assert.Equal(t, got.Answers, results[1].Outcomes[0].Observation.Answers, "both arrive in request order")
		for _, a := range got.Answers {
			if a.Choice != nil {
				for _, p := range a.Choice.Probabilities {
					require.GreaterOrEqual(t, p.Probability, 0.0)
					require.LessOrEqual(t, p.Probability, 1.0)
				}
			}
		}
		data, err := json.Marshal(results[0])
		require.NoError(t, err)
		_, err = discern.ParseResult(data)
		require.NoError(t, err, "an accepted observation is a valid record")
	})
}

// permute reorders s by the swaps that seed selects, read as mixed-radix digits.
func permute[T any](s []T, seed uint8) {
	rest := int(seed)
	for i := len(s) - 1; i > 0; i-- {
		j := rest % (i + 1)
		s[i], s[j] = s[j], s[i]
		rest /= i + 1
	}
}

// shuffledObservation returns a copy of obs with its answers and their
// probabilities reordered by seed.
func shuffledObservation(obs discern.Observation, seed uint8) discern.Observation {
	c := obs.Clone()
	permute(c.Answers, seed)
	for i := range c.Answers {
		if ch := c.Answers[i].Choice; ch != nil {
			permute(ch.Probabilities, seed>>3)
		}
		if s := c.Answers[i].Score; s != nil {
			permute(s.Probabilities, seed>>5)
		}
	}
	return c
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

func FuzzRecommendThresholds(f *testing.F) {
	f.Add(0.8, "gte", 0.5, "lte", 0.25)
	f.Add(1.0, "gt", 0.0, "lt", 0.0)
	f.Add(-1.0, "eq", 2.0, "approx", -0.5)
	res := mustEvaluate(f, pairEvaluator(f), discern.JevAndClef)
	f.Fuzz(func(t *testing.T, qThreshold float64, qOp string, nThreshold float64, nOp string, maxDiff float64) {
		pol := discern.Policy{Version: "fuzz", Questions: []discern.QuestionPolicy{
			{Question: qQueue, Checks: []discern.Check{{Metric: discern.MetricChoiceProbability, Op: discern.Op(qOp), Threshold: qThreshold}}},
			{Question: qOutage, Checks: []discern.Check{{Metric: discern.MetricNoul, Op: discern.Op(nOp), Threshold: nThreshold}}, Agreement: &discern.Agreement{MaxDifference: maxDiff}},
		}}
		require.NotPanics(t, func() {
			again, err := discern.Recommend(res, pol)
			if err != nil {
				require.ErrorIs(t, err, discern.ErrInvalidPolicy)
				return
			}
			for _, rec := range again.Recommendations {
				if rec.Status != discern.Accepted {
					require.Nil(t, rec.Selected)
					continue
				}
				for _, c := range rec.Checks {
					require.True(t, c.Passed, "accepted with failing check %s", c.RuleID)
				}
			}
			data, err := json.Marshal(again)
			require.NoError(t, err)
			_, err = discern.ParseResult(data)
			require.NoError(t, err)
		})
	})
}

func FuzzRequestValidation(f *testing.F) {
	f.Add("queue", "billing", "Which queue?", `{"ticket":1}`, "ticket-1")
	f.Add("", "", "", `null`, "")
	f.Add("q id", "a\x00", " ", `{"a":`, "bad\n")
	f.Fuzz(func(t *testing.T, questionID, optionID, instructions, state, correlation string) {
		fake := discerntest.Static(discern.Jev, discern.Observation{})
		ev := testutil.Evaluator(t, discern.WithAdapter(fake))
		req := discern.Request{
			Providers: discern.JevOnly,
			State:     discern.JSON([]byte(state)),
			Questions: []discern.Question{discern.Choice(questionID, discern.Text(instructions),
				discern.Opt(optionID, discern.Value{}), discern.Opt(optionID+"-other", discern.Value{}))},
			CorrelationID: correlation,
		}
		require.NotPanics(t, func() {
			res, err := ev.Evaluate(t.Context(), req)
			if res == nil {
				// A local error: nothing was sent.
				require.Error(t, err)
				require.True(t, errors.Is(err, discern.ErrInvalidRequest) || errors.Is(err, discern.ErrUnsupported), "%v", err)
				require.Equal(t, 0, fake.Calls())
				return
			}
			// The fake's empty observation never matches a valid request, and the
			// failure is still a valid record.
			require.ErrorIs(t, err, discern.ErrMalformedResponse)
			require.Equal(t, 1, fake.Calls())
			_, err = discern.ParseResult(mustMarshalJSON(t, res))
			require.NoError(t, err)
		})
	})
}

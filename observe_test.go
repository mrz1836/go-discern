package discern_test

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
)

// evaluateAnswer runs a one-question request whose single answer is supplied by a fake.
func evaluateAnswer(t *testing.T, q discern.Question, a discern.Answer, usage *discern.Usage) (*discern.Result, error) {
	t.Helper()
	fake := discerntest.Func(discern.Jev, "", func(context.Context, *discern.Request) (*discern.Observation, error) {
		return &discern.Observation{Usage: usage, Answers: []discern.Answer{a}}, nil
	})
	ev := testutil.Evaluator(t, discern.WithAdapter(fake))
	return ev.Evaluate(t.Context(), discern.Request{
		Providers: discern.JevOnly,
		State:     discern.Text("ticket"),
		Questions: []discern.Question{q},
	})
}

func choiceQuestion(n int) discern.Question {
	opts := make([]discern.ChoiceOption, n)
	for i := range opts {
		opts[i] = discern.Opt(fmt.Sprintf("o%d", i), discern.Value{})
	}
	return discern.Choice(qQueue, discern.Text("Which queue?"), opts...)
}

func choiceWith(option string, probs ...float64) discern.Answer {
	c := &discern.ChoiceAnswer{Option: option}
	for i, p := range probs {
		c.Probabilities = append(c.Probabilities, discern.OptionProbability{Option: fmt.Sprintf("o%d", i), Probability: p})
	}
	return discern.Answer{QuestionID: qQueue, Kind: discern.KindChoice, Choice: c}
}

func scoreQuestion(n int) discern.Question {
	levels := make([]discern.ScoreLevel, n)
	for i := range levels {
		levels[i] = discern.Level(fmt.Sprintf("l%d", i), discern.Text("level"))
	}
	return discern.Score(qSeverity, discern.Text("How severe?"), levels...)
}

func scoreWith(value float64, probs ...float64) discern.Answer {
	s := &discern.ScoreAnswer{Value: value}
	for i, p := range probs {
		s.Probabilities = append(s.Probabilities, discern.LevelProbability{Level: fmt.Sprintf("l%d", i), Probability: p})
	}
	return discern.Answer{QuestionID: qSeverity, Kind: discern.KindScore, Score: s}
}

func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestObservationToleranceBoundaries(t *testing.T) {
	spread255 := append([]float64{1.0}, repeat(0, 254)...)
	tests := []struct {
		name string
		q    discern.Question
		a    discern.Answer
		ok   bool
	}{
		{"2 options exact", choiceQuestion(2), choiceWith("o0", 0.5, 0.5), true},
		{"2 options 2 decimals rounded up within one unit each", choiceQuestion(2), choiceWith("o0", 0.51, 0.51), true},
		{"2 options 2 decimals above nonzero units", choiceQuestion(2), choiceWith("o0", 0.52, 0.51), false},
		{"2 options 2 decimals truncated within one unit each", choiceQuestion(2), choiceWith("o0", 0.49, 0.49), true},
		{"2 options 2 decimals below n units", choiceQuestion(2), choiceWith("o0", 0.49, 0.48), false},
		{"2 options 4 decimals within", choiceQuestion(2), choiceWith("o0", 0.5001, 0.5001), true},
		{"2 options 4 decimals above", choiceQuestion(2), choiceWith("o0", 0.5002, 0.5001), false},
		{"zero entries cannot round up", choiceQuestion(3), choiceWith("o0", 0.52, 0.51, 0), false},
		{"255 options one-hot", choiceQuestion(255), choiceWith("o0", spread255...), true},
		{"255 options 4 decimals within", choiceQuestion(255), choiceWith("o0", repeat(0.0039, 255)...), true},
		{"255 options 4 decimals below", choiceQuestion(255), choiceWith("o0", repeat(0.0038, 255)...), false},
		{"255 options 2 decimals within nonzero units", choiceQuestion(255), choiceWith("o0", append([]float64{0.99, 0.03}, repeat(0, 253)...)...), true},
		{"255 options 2 decimals above nonzero units", choiceQuestion(255), choiceWith("o0", append([]float64{0.99, 0.04}, repeat(0, 253)...)...), false},
		{"255 options 2 decimals truncated to zero", choiceQuestion(255), choiceWith("o0", repeat(0, 255)...), true},
		{"argmax tie accepted", choiceQuestion(2), choiceWith("o1", 0.5, 0.5), true},
		{"choice not argmax", choiceQuestion(2), choiceWith("o1", 0.6, 0.4), false},
		{"choice not an option", choiceQuestion(2), choiceWith("other", 0.6, 0.4), false},
		{"probability above 1", choiceQuestion(2), choiceWith("o0", 1.2, -0.2), false},
		{"probability NaN", choiceQuestion(2), choiceWith("o0", math.NaN(), 0.5), false},
		{"missing option probability", choiceQuestion(3), choiceWith("o0", 0.6, 0.4), false},
		{"extra option probability", choiceQuestion(2), choiceWith("o0", 0.5, 0.5, 0), false},
		{"score probability above one", scoreQuestion(2), scoreWith(0, 1.5, -0.5), false},
		{"score consistent", scoreQuestion(3), scoreWith(1.05, 0, 0.95, 0.05), true},
		{"score within rounding", scoreQuestion(3), scoreWith(1.08, 0, 0.95, 0.05), true},
		{"score inconsistent", scoreQuestion(3), scoreWith(1.2, 0, 0.95, 0.05), false},
		{"score 4 decimals consistent", scoreQuestion(3), scoreWith(1.21, 0.1234, 0.5432, 0.3334), true},
		{"score 4 decimals inconsistent", scoreQuestion(3), scoreWith(1.2105, 0.1234, 0.5432, 0.3334), false},
		{"score top level", scoreQuestion(3), scoreWith(2, 0, 0, 1), true},
		{"score below range", scoreQuestion(3), scoreWith(-0.02, 1, 0, 0), false},
		{"score above range", scoreQuestion(2), scoreWith(1.05, 0, 1), false},
		{"score infinite", scoreQuestion(2), scoreWith(math.Inf(1), 0, 1), false},
		{"score 10 levels consistent", scoreQuestion(10), scoreWith(9, append(repeat(0, 9), 1)...), true},
		// A float32 widened to float64 reads as 0.33000001311302185, but its
		// precision is that of 0.33.
		{"3 options 2 decimals truncated", choiceQuestion(3), choiceWith("o0", repeat(0.33, 3)...), true},
		{"3 options widened float32 2 decimals truncated", choiceQuestion(3), choiceWith("o0", repeat(float64(float32(0.33)), 3)...), true},
		{"3 options 2 decimals below n units", choiceQuestion(3), choiceWith("o0", repeat(0.32, 3)...), false},
		{"3 options widened float32 2 decimals below n units", choiceQuestion(3), choiceWith("o0", repeat(float64(float32(0.32)), 3)...), false},
		{"score widened float32 consistent", scoreQuestion(3), scoreWith(float64(float32(1.05)), 0, float64(float32(0.95)), float64(float32(0.05))), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := evaluateAnswer(t, tt.q, tt.a, nil)
			if tt.ok {
				require.NoError(t, err)
				assert.Equal(t, discern.Complete, res.Status)
				return
			}
			require.ErrorIs(t, err, discern.ErrMalformedResponse)
			assert.Equal(t, discern.FailureMalformed, testutil.Failure(t, res).Kind)
		})
	}
}

func TestObservationRangeChecks(t *testing.T) {
	noul := discern.Noul(qOutage, discern.Text("Outage?"))
	tests := []struct {
		name  string
		q     discern.Question
		a     discern.Answer
		usage *discern.Usage
	}{
		{"noul below zero", noul, discern.Answer{QuestionID: qOutage, Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: -0.1}}, nil},
		{"noul NaN", noul, discern.Answer{QuestionID: qOutage, Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: math.NaN()}}, nil},
		{"two typed answers", noul, discern.Answer{QuestionID: qOutage, Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: 0.5}, Score: &discern.ScoreAnswer{}}, nil},
		{"choice confidence above 1", choiceQuestion(2), func() discern.Answer {
			a := choiceWith("o0", 0.5, 0.5)
			a.Choice.Confidence = new(1.5)
			return a
		}(), nil},
		{"duplicate probability", choiceQuestion(2), func() discern.Answer {
			a := choiceWith("o0", 0.5, 0.5)
			a.Choice.Probabilities[1].Option = "o0"
			return a
		}(), nil},
		{"negative tokens", noul, discern.Answer{QuestionID: qOutage, Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: 0.5}}, &discern.Usage{InputTokens: new(int64(-1))}},
		{"infinite neurons", noul, discern.Answer{QuestionID: qOutage, Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: 0.5}}, &discern.Usage{Neurons: new(math.Inf(1))}},
		{"wrong question", noul, discern.Answer{QuestionID: "other", Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: 0.5}}, nil},
		{"noul above one", noul, discern.Answer{QuestionID: qOutage, Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: 1.0001}}, nil},
		{"noul infinite", noul, discern.Answer{QuestionID: qOutage, Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: math.Inf(-1)}}, nil},
		{"null noul", noul, discern.Answer{QuestionID: qOutage, Kind: discern.KindNoul}, nil},
		{"noul kind with a score", noul, discern.Answer{QuestionID: qOutage, Kind: discern.KindNoul, Score: &discern.ScoreAnswer{}}, nil},
		{"choice kind with a noul", choiceQuestion(2), discern.Answer{QuestionID: qQueue, Kind: discern.KindChoice, Noul: &discern.NoulAnswer{Value: 0.5}}, nil},
		{"score kind with a choice", scoreQuestion(2), discern.Answer{QuestionID: qSeverity, Kind: discern.KindScore, Choice: &discern.ChoiceAnswer{}}, nil},
		{"choice confidence NaN", choiceQuestion(2), func() discern.Answer {
			a := choiceWith("o0", 0.5, 0.5)
			a.Choice.Confidence = new(math.NaN())
			return a
		}(), nil},
		{"score confidence negative", scoreQuestion(2), func() discern.Answer {
			a := scoreWith(1, 0, 1)
			a.Score.Confidence = new(-0.01)
			return a
		}(), nil},
		{"probability infinite", choiceQuestion(2), choiceWith("o0", math.Inf(1), 0), nil},
		{"duplicate level probability", scoreQuestion(2), func() discern.Answer {
			a := scoreWith(0, 1, 0)
			a.Score.Probabilities[1].Level = "l0"
			return a
		}(), nil},
		{"missing level probability", scoreQuestion(3), scoreWith(1, 0, 1), nil},
		{"unknown level", scoreQuestion(2), func() discern.Answer {
			a := scoreWith(0, 1, 0)
			a.Score.Probabilities[1].Level = "other"
			return a
		}(), nil},
		{"score probabilities do not sum to one", scoreQuestion(2), scoreWith(1, 0, 0.9), nil},
		{"negative output tokens", noul, discern.Answer{QuestionID: qOutage, Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: 0.5}}, &discern.Usage{OutputTokens: new(int64(-3))}},
		{"negative neurons", noul, discern.Answer{QuestionID: qOutage, Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: 0.5}}, &discern.Usage{Neurons: new(-0.5)}},
		{"NaN neurons", noul, discern.Answer{QuestionID: qOutage, Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: 0.5}}, &discern.Usage{Neurons: new(math.NaN())}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := evaluateAnswer(t, tt.q, tt.a, tt.usage)
			require.ErrorIs(t, err, discern.ErrMalformedResponse)
		})
	}
}

func TestObservationIsReorderedIntoRequestOrder(t *testing.T) {
	obs := jevAnswers().observation()
	obs.Answers[0], obs.Answers[2] = obs.Answers[2], obs.Answers[0]
	c := obs.Answers[2].Choice
	c.Probabilities[0], c.Probabilities[2] = c.Probabilities[2], c.Probabilities[0]
	s := obs.Answers[0].Score
	s.Probabilities[0], s.Probabilities[1] = s.Probabilities[1], s.Probabilities[0]

	ev := testutil.Evaluator(t, discern.WithAdapter(discerntest.Static(discern.Jev, obs)))
	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly))
	require.NoError(t, err)
	require.Len(t, res.Outcomes, 1)
	got := res.Outcomes[0].Observation
	assert.Equal(t, qQueue, got.Answers[0].QuestionID)
	assert.Equal(t, qOutage, got.Answers[1].QuestionID)
	assert.Equal(t, qSeverity, got.Answers[2].QuestionID)
	assert.Equal(t, []discern.OptionProbability{{Option: optBilling, Probability: 0.91}, {Option: optTechnical, Probability: 0.06}, {Option: "account", Probability: 0.03}}, got.Answers[0].Choice.Probabilities)
	assert.Equal(t, []discern.LevelProbability{{Level: levelCosmetic, Probability: 0}, {Level: "degraded", Probability: 0.95}, {Level: levelBlocked, Probability: 0.05}}, got.Answers[2].Score.Probabilities)
}

func TestObservationAnswerSetErrors(t *testing.T) {
	good := clefAnswers().observation()
	tests := []struct {
		name   string
		mutate func(obs *discern.Observation)
	}{
		{"duplicate answer", func(obs *discern.Observation) { obs.Answers[1] = obs.Answers[0] }},
		{"duplicate answer in request order", func(obs *discern.Observation) {
			obs.Answers = append(obs.Answers, obs.Answers[2])
		}},
		{"extra answer", func(obs *discern.Observation) {
			obs.Answers = append(obs.Answers, discern.Answer{QuestionID: "priority", Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: 0.5}})
		}},
		{"no answers", func(obs *discern.Observation) { obs.Answers = nil }},
		{"reordered with an unknown question", func(obs *discern.Observation) {
			obs.Answers[0], obs.Answers[2] = obs.Answers[2], obs.Answers[0]
			obs.Answers[1].QuestionID = "priority"
		}},
		{"reordered with a duplicate probability", func(obs *discern.Observation) {
			obs.Answers[0], obs.Answers[1] = obs.Answers[1], obs.Answers[0]
			obs.Answers[1].Choice.Probabilities[2].Option = optBilling
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := good.Clone()
			tt.mutate(&obs)
			ev := testutil.Evaluator(t, discern.WithAdapter(discerntest.Static(discern.Clef, obs)))
			res, err := ev.Evaluate(t.Context(), ticketRequest(discern.ClefOnly))
			require.ErrorIs(t, err, discern.ErrMalformedResponse)
			require.NotNil(t, res)
			assert.Equal(t, discern.FailureMalformed, res.Outcomes[0].Failure.Kind)
			assert.Equal(t, "req-clef", res.Outcomes[0].Failure.RequestID, "identifiers survive a malformed observation")
		})
	}
}

func TestObservationAbsentNullAndZeroAreDistinct(t *testing.T) {
	zero := jevAnswers()
	zero.conf, zero.scoreConf, zero.noul = new(0.0), new(0.0), 0
	absent := jevAnswers()
	absent.conf, absent.scoreConf, absent.noul = nil, nil, 0
	pol := discern.Policy{Version: "v1", Questions: []discern.QuestionPolicy{
		{Question: qQueue, Checks: []discern.Check{check(discern.MetricConfidence, discern.AtLeast, 0)}},
		{Question: qOutage, Checks: []discern.Check{check(discern.MetricNoul, discern.AtMost, 0)}},
		{Question: qSeverity, Checks: []discern.Check{check(discern.MetricConfidence, discern.AtLeast, 0)}},
	}}
	tests := []struct {
		name    string
		answers ticketAnswers
		status  map[string]discern.RecommendationStatus
	}{
		{"zero values are observed", zero, map[string]discern.RecommendationStatus{qQueue: discern.Accepted, qOutage: discern.Accepted, qSeverity: discern.Accepted}},
		{"absent confidence is not zero", absent, map[string]discern.RecommendationStatus{qQueue: discern.Incomplete, qOutage: discern.Accepted, qSeverity: discern.Incomplete}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jev := discerntest.Static(discern.Jev, tt.answers.observation())
			_, clef := staticPair()
			res := evaluatePolicy(t, discern.JevOnly, jev, clef, pol)
			for _, id := range []string{qQueue, qOutage, qSeverity} {
				rec := recommendation(t, res, id)
				assert.Equal(t, tt.status[id], rec.Status, id)
				require.Len(t, rec.Checks, 1)
				if tt.status[id] == discern.Incomplete {
					assert.Nil(t, rec.Checks[0].Observed)
					assert.Equal(t, []discern.ReasonCode{discern.ReasonMetricAbsent}, rec.Reasons)
				} else {
					require.NotNil(t, rec.Checks[0].Observed)
				}
			}
			require.Len(t, res.Outcomes, 1)
			q, ok := res.Outcomes[0].Observation.Choice(qQueue)
			require.True(t, ok)
			assert.Equal(t, tt.answers.conf, q.Confidence)
		})
	}
}

func TestObservationNormalizationLeavesAdapterDataUntouched(t *testing.T) {
	shuffled := jevAnswers().observation()
	shuffled.Answers[0], shuffled.Answers[2] = shuffled.Answers[2], shuffled.Answers[0]
	c := shuffled.Answers[2].Choice
	c.Probabilities[0], c.Probabilities[1] = c.Probabilities[1], c.Probabilities[0]
	tests := []struct {
		name string
		obs  discern.Observation
	}{
		{"in request order", jevAnswers().observation()},
		{"reordered", shuffled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shared := tt.obs.Clone()
			before := shared.Clone()
			fake := discerntest.Func(discern.Jev, "", func(context.Context, *discern.Request) (*discern.Observation, error) {
				return &shared, nil // every call returns the same observation
			})
			ev := testutil.Evaluator(t, discern.WithAdapter(fake))
			res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly))
			require.NoError(t, err)
			assert.Equal(t, before, shared, "the adapter's observation is never modified")

			require.Len(t, res.Outcomes, 1)
			got := res.Outcomes[0].Observation
			assert.Equal(t, []string{qQueue, qOutage, qSeverity}, []string{got.Answers[0].QuestionID, got.Answers[1].QuestionID, got.Answers[2].QuestionID})
			assert.Equal(t, optBilling, got.Answers[0].Choice.Probabilities[0].Option)

			// The result shares no memory with the adapter's observation.
			got.Answers[0].Choice.Probabilities[0].Probability = 0
			*got.Answers[0].Choice.Confidence = 0
			*got.Usage.InputTokens = 0
			assert.Equal(t, before, shared)
		})
	}
}

func TestObservationLargeQuestionSet(t *testing.T) {
	req, obs := largeRequest(64, 255)
	shuffled := obs.Clone()
	slices.Reverse(shuffled.Answers)
	for i := range shuffled.Answers {
		if c := shuffled.Answers[i].Choice; c != nil {
			slices.Reverse(c.Probabilities)
		}
	}
	for _, tt := range []struct {
		name string
		obs  discern.Observation
	}{{"in request order", obs}, {"reversed", shuffled}} {
		t.Run(tt.name, func(t *testing.T) {
			ev := testutil.Evaluator(t, discern.WithAdapter(discerntest.Static(discern.Jev, tt.obs)))
			res, err := ev.Evaluate(t.Context(), req)
			require.NoError(t, err)
			assert.Equal(t, obs.Answers, res.Outcomes[0].Observation.Answers)
		})
	}
}

func TestIdentifiersAreComparedByteForByte(t *testing.T) {
	// "café" in composed (NFC) and decomposed (NFD) form looks the same but is
	// encoded differently; without normalization they are two distinct options.
	composed, decomposed := "caf\u00e9", "cafe\u0301"
	req := discern.Request{
		Providers: discern.JevOnly, State: discern.Text("ticket"),
		Questions: []discern.Question{discern.Choice(qQueue, discern.Text("Which queue?"),
			discern.Opt(composed, discern.Value{}), discern.Opt(decomposed, discern.Value{}))},
	}
	obs := discern.Observation{Answers: []discern.Answer{{QuestionID: qQueue, Kind: discern.KindChoice, Choice: &discern.ChoiceAnswer{
		Option: decomposed, Probabilities: []discern.OptionProbability{{Option: composed, Probability: 0.4}, {Option: decomposed, Probability: 0.6}},
	}}}}
	ev := testutil.Evaluator(t, discern.WithAdapter(discerntest.Static(discern.Jev, obs)))
	res, err := ev.Evaluate(t.Context(), req)
	require.NoError(t, err)
	require.Len(t, res.Questions, 1)
	require.Len(t, res.Outcomes, 1)
	assert.Equal(t, []string{composed, decomposed}, res.Questions[0].Options)
	c, ok := res.Outcomes[0].Observation.Choice(qQueue)
	require.True(t, ok)
	assert.Equal(t, decomposed, c.Option)
}

func TestEvaluateInvalidProviderIdentifiersAreMalformed(t *testing.T) {
	tests := []struct {
		name string
		edit func(obs *discern.Observation)
	}{
		{"model with a line break", func(obs *discern.Observation) { obs.Model = "clef\nlevel=error msg=forged" }},
		{"model with a space", func(obs *discern.Observation) { obs.Model = "clef flash" }},
		{"model too long", func(obs *discern.Observation) { obs.Model = strings.Repeat("m", 257) }},
		{"model with a format character", func(obs *discern.Observation) { obs.Model = "clef\u202e" }},
		{"request ID with a tab", func(obs *discern.Observation) { obs.RequestID = "req\t1" }},
		{"request ID too long", func(obs *discern.Observation) { obs.RequestID = strings.Repeat("r", 257) }},
		{"ray ID with non-ASCII", func(obs *discern.Observation) { obs.RayID = "8f00-S\u00e9C" }},
		{"ray ID with DEL", func(obs *discern.Observation) { obs.RayID = "8f00\x7f" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := clefAnswers().observation()
			obs.RayID = "8f00-SJC"
			tt.edit(&obs)
			clef := discerntest.Func(discern.Clef, "clef", func(context.Context, *discern.Request) (*discern.Observation, error) {
				c := obs.Clone()
				return &c, nil
			})
			ev := testutil.Evaluator(t, discern.WithAdapter(clef))
			res, err := ev.Evaluate(t.Context(), ticketRequest(discern.ClefOnly), discern.WithPolicy(discern.Policy{
				Version: "v1", Questions: []discern.QuestionPolicy{{Question: qQueue, Checks: []discern.Check{
					{Model: "clef-2", Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.5},
				}}},
			}))
			require.ErrorIs(t, err, discern.ErrMalformedResponse)
			f := testutil.Failure(t, res)
			assert.Equal(t, discern.FailureMalformed, f.Kind)
			for _, id := range []string{f.RequestID, f.RayID} {
				assert.NotContains(t, id, "\n")
				assert.LessOrEqual(t, len(id), 256)
			}
			data := mustJSON(t, res)
			assert.NotContains(t, data, "forged")
			_, err = discern.ParseResult([]byte(data))
			require.NoError(t, err, "the failure is a valid record")
		})
	}

	// The longest valid identifiers are accepted.
	obs := clefAnswers().observation()
	obs.Model, obs.RequestID, obs.RayID = strings.Repeat("m", 256), strings.Repeat("r", 256), "!~"
	ev := testutil.Evaluator(t, discern.WithAdapter(discerntest.Static(discern.Clef, obs)))
	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.ClefOnly))
	require.NoError(t, err)
	require.Len(t, res.Outcomes, 1)
	assert.Equal(t, strings.Repeat("m", 256), res.Outcomes[0].Observation.Model)
}

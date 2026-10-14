package discerntest_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
)

var errQueueClosed = errors.New("support queue closed")

// ticket is a synthetic support-ticket request with one routing question.
func ticket(providers discern.ProviderSet) discern.Request {
	return discern.Request{
		Providers: providers,
		State:     discern.Text("Subject: Charged twice\n\nMy invoice was paid twice this month."),
		Questions: []discern.Question{
			discern.Choice("queue", discern.Text("Which support queue should handle this ticket?"),
				discern.Opt("billing", discern.Text("Invoices, refunds, failed payments")),
				discern.Opt("technical", discern.Text("Errors, outages, integration problems"))),
		},
		CorrelationID: "ticket-1042",
	}
}

// routed is an observation answering ticket.
func routed(model string) discern.Observation {
	return discern.Observation{
		Model: model, RequestID: "req-1",
		Usage: &discern.Usage{InputTokens: new(int64(120))},
		Answers: []discern.Answer{{
			QuestionID: "queue", Kind: discern.KindChoice,
			Choice: &discern.ChoiceAnswer{Option: "billing", Confidence: new(0.8), Probabilities: []discern.OptionProbability{
				{Option: "billing", Probability: 0.9},
				{Option: "technical", Probability: 0.1},
			}},
		}},
	}
}

// call prepares and runs one call on the fake directly, as an evaluator would.
func call(t *testing.T, f *discerntest.Fake, req discern.Request) (*discern.Observation, error) {
	t.Helper()
	c, err := f.Prepare(&req)
	require.NoError(t, err)
	require.NotNil(t, c)
	return c(t.Context())
}

func TestFakeModelDefaults(t *testing.T) {
	answer := func(context.Context, *discern.Request) (*discern.Observation, error) { return nil, errQueueClosed }
	tests := []struct {
		name  string
		fake  *discerntest.Fake
		model string
	}{
		{"static with a model", discerntest.Static(discern.Jev, routed("jev-1.13.0")), "jev-1.13.0"},
		{"static without a model", discerntest.Static(discern.Jev, routed("")), discerntest.DefaultModel},
		{"func with a model", discerntest.Func(discern.Clef, "clef-flash", answer), "clef-flash"},
		{"func without a model", discerntest.Func(discern.Clef, "", answer), discerntest.DefaultModel},
		{"failing", discerntest.Failing(discern.Clef, nil), discerntest.DefaultModel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.model, tt.fake.Model())
			assert.Equal(t, 0, tt.fake.Calls(), "constructing a fake makes no call")
		})
	}
	assert.Equal(t, discern.Clef, discerntest.Failing(discern.Clef, nil).Provider())
	assert.Equal(t, discern.Jev, discerntest.Static(discern.Jev, routed("")).Provider())
}

func TestStaticReturnsIndependentCopies(t *testing.T) {
	obs := routed("jev-1.13.0")
	fake := discerntest.Static(discern.Jev, obs)

	// Changing the caller's observation after Static does not change the answers.
	obs.Answers[0].Choice.Option = "technical"
	obs.Answers[0].Choice.Probabilities[0].Probability = 0.1
	*obs.Answers[0].Choice.Confidence = 0.1
	*obs.Usage.InputTokens = 1
	obs.Model = "mutated"

	first, err := call(t, fake, ticket(discern.JevOnly))
	require.NoError(t, err)
	want := routed("jev-1.13.0")
	assert.Equal(t, &want, first)

	// Changing one answer does not change the next.
	first.Answers[0].Choice.Probabilities[0].Probability = 0
	*first.Answers[0].Choice.Confidence = 0
	*first.Usage.InputTokens = 0
	first.Answers = append(first.Answers, discern.Answer{QuestionID: "extra"})

	second, err := call(t, fake, ticket(discern.JevOnly))
	require.NoError(t, err)
	assert.Equal(t, &want, second)
	assert.NotSame(t, first, second)
	assert.Equal(t, 2, fake.Calls())
}

func TestFuncReceivesContextAndRequestCopy(t *testing.T) {
	type key struct{}
	var (
		gotValue any
		gotReq   *discern.Request
	)
	fake := discerntest.Func(discern.Clef, "clef-flash", func(ctx context.Context, req *discern.Request) (*discern.Observation, error) {
		gotValue, gotReq = ctx.Value(key{}), req
		obs := routed("clef-flash")
		return &obs, nil
	})
	ev, err := discern.New(discern.WithAdapter(fake))
	require.NoError(t, err)

	req := ticket(discern.ClefOnly)
	ctx := context.WithValue(t.Context(), key{}, "ticket-context")
	res, err := ev.Evaluate(ctx, req)
	require.NoError(t, err)

	assert.Equal(t, "ticket-context", gotValue)
	require.NotNil(t, gotReq)
	assert.Equal(t, "ticket-1042", gotReq.CorrelationID)
	assert.Equal(t, "billing", gotReq.Questions[0].Options[0].ID)
	gotReq.Questions[0].Options[0].ID = "mutated"
	assert.Equal(t, "billing", req.Questions[0].Options[0].ID, "the fake gets its own copy of the request")
	require.Len(t, res.Outcomes, 1)
	assert.Equal(t, "clef-flash", res.Outcomes[0].RequestedModel)
	assert.Equal(t, 1, fake.Calls())
}

func TestFuncErrorsReachTheCaller(t *testing.T) {
	fake := discerntest.Func(discern.Jev, "", func(context.Context, *discern.Request) (*discern.Observation, error) {
		return nil, errQueueClosed
	})
	ev, err := discern.New(discern.WithAdapter(fake))
	require.NoError(t, err)
	res, err := ev.Evaluate(t.Context(), ticket(discern.JevOnly))
	require.ErrorIs(t, err, errQueueClosed)
	pe := testutil.ProviderError(t, err)
	assert.Equal(t, discern.FailureAdapter, pe.Kind)
	assert.NotContains(t, err.Error(), errQueueClosed.Error(), "the cause is reachable but never printed")
	require.NotNil(t, res)
	assert.Equal(t, discern.Failed, res.Status)
}

func TestFailingScenarios(t *testing.T) {
	rejected := discern.NewProviderError(discern.Jev, discern.FailureRejected, nil)
	rejected.StatusCode = 429
	tests := []struct {
		name       string
		err        error
		kind       discern.FailureKind
		is         error
		statusCode int
	}{
		{"nil error is a transport failure", nil, discern.FailureTransport, nil, 0},
		{"provider error is kept", rejected, discern.FailureRejected, discern.ErrRejected, 429},
		{"plain error is an adapter failure", errQueueClosed, discern.FailureAdapter, errQueueClosed, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := discerntest.Failing(discern.Jev, tt.err)
			obs, err := call(t, fake, ticket(discern.JevOnly))
			assert.Nil(t, obs)
			require.Error(t, err)
			if tt.err == nil {
				pe := testutil.ProviderError(t, err)
				assert.Equal(t, discern.Jev, pe.Provider)
				assert.Equal(t, discern.FailureTransport, pe.Kind)
			}

			ev, err := discern.New(discern.WithAdapter(fake))
			require.NoError(t, err)
			res, err := ev.Evaluate(t.Context(), ticket(discern.JevOnly))
			if tt.is != nil {
				require.ErrorIs(t, err, tt.is)
			}
			require.NotNil(t, res)
			require.NotNil(t, res.Outcomes[0].Failure)
			assert.Equal(t, tt.kind, res.Outcomes[0].Failure.Kind)
			assert.Equal(t, tt.statusCode, res.Outcomes[0].Failure.StatusCode)
			assert.Equal(t, 2, fake.Calls())
		})
	}
}

func TestFakeCountsCallsNotPreparations(t *testing.T) {
	fake := discerntest.Static(discern.Jev, routed(""))
	req := ticket(discern.JevOnly)
	c, err := fake.Prepare(&req)
	require.NoError(t, err)
	_, err = fake.Prepare(&req)
	require.NoError(t, err)
	assert.Equal(t, 0, fake.Calls())
	_, err = c(t.Context())
	require.NoError(t, err)
	_, err = c(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 2, fake.Calls())
}

func TestFakeCountsConcurrentCalls(t *testing.T) {
	jev := discerntest.Static(discern.Jev, routed("jev-1.13.0"))
	clef := discerntest.Static(discern.Clef, routed("clef-flash"))
	ev, err := discern.New(discern.WithAdapter(jev), discern.WithAdapter(clef), discern.WithBatchConcurrency(4))
	require.NoError(t, err)

	const goroutines, perGoroutine = 8, 5
	errs := make([]error, goroutines*perGoroutine)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			for i := range perGoroutine {
				_, errs[g*perGoroutine+i] = ev.Evaluate(t.Context(), ticket(discern.JevAndClef))
			}
		})
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}

	reqs := make([]discern.Request, 12)
	for i := range reqs {
		reqs[i] = ticket(discern.JevOnly)
	}
	items, err := ev.EvaluateBatch(t.Context(), reqs)
	require.NoError(t, err)
	for _, it := range items {
		require.NoError(t, it.Err)
	}

	assert.Equal(t, goroutines*perGoroutine+len(reqs), jev.Calls())
	assert.Equal(t, goroutines*perGoroutine, clef.Calls())
}

func TestStaticThroughEvaluator(t *testing.T) {
	jev := discerntest.Static(discern.Jev, routed("jev-1.13.0"))
	clef := discerntest.Static(discern.Clef, routed(""))
	policy := discern.Policy{Version: "routing-v3", Questions: []discern.QuestionPolicy{{
		Question: "queue", Checks: []discern.Check{{Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.8}},
	}}}
	ev, err := discern.New(discern.WithAdapter(jev), discern.WithAdapter(clef), discern.WithDefaultPolicy(policy))
	require.NoError(t, err)

	res, err := ev.Evaluate(t.Context(), ticket(discern.JevAndClef))
	require.NoError(t, err)
	assert.Equal(t, discern.Complete, res.Status)
	require.Len(t, res.Outcomes, 2)
	assert.Equal(t, "jev-1.13.0", res.Outcomes[0].RequestedModel)
	assert.Equal(t, discerntest.DefaultModel, res.Outcomes[1].RequestedModel)
	assert.Empty(t, res.Outcomes[1].Observation.Model, "a fake reports no model unless the observation has one")
	rec, ok := res.Recommendation("queue")
	require.True(t, ok)
	assert.Equal(t, discern.Accepted, rec.Status)
	assert.Equal(t, &discern.Selected{Providers: []discern.Provider{discern.Jev, discern.Clef}, Option: "billing"}, rec.Selected)
	assert.Equal(t, 1, jev.Calls())
	assert.Equal(t, 1, clef.Calls())
}

func TestStaticAnswersAreValidatedLikeRealOnes(t *testing.T) {
	wrong := routed("")
	wrong.Answers[0].Choice.Option = "account"
	fake := discerntest.Static(discern.Jev, wrong)
	ev, err := discern.New(discern.WithAdapter(fake))
	require.NoError(t, err)
	res, err := ev.Evaluate(t.Context(), ticket(discern.JevOnly))
	require.ErrorIs(t, err, discern.ErrMalformedResponse)
	require.NotNil(t, res)
	assert.Equal(t, discern.FailureMalformed, res.Outcomes[0].Failure.Kind)
	assert.Equal(t, 1, fake.Calls())

	// The reason is the ProviderError's cause.
	pe := testutil.ProviderError(t, err)
	reason := errors.Unwrap(pe)
	require.Error(t, reason)
	assert.Contains(t, reason.Error(), "choice is not one of the options")
}

func TestFakeObservationIdentifiersAreValidated(t *testing.T) {
	tests := []struct {
		name   string
		edit   func(obs *discern.Observation)
		reason string
	}{
		{"model with a line break", func(obs *discern.Observation) { obs.Model = "jev\nforged" }, "model"},
		{"request ID with a space", func(obs *discern.Observation) { obs.RequestID = "req 1" }, "request_id"},
		{"ray ID too long", func(obs *discern.Observation) { obs.RayID = string(make([]byte, 257)) }, "ray_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := routed("jev-1.13.0")
			tt.edit(&obs)
			fake := discerntest.Func(discern.Jev, "jev-1.13.0", func(context.Context, *discern.Request) (*discern.Observation, error) {
				c := obs.Clone()
				return &c, nil
			})
			ev, err := discern.New(discern.WithAdapter(fake))
			require.NoError(t, err)
			_, err = ev.Evaluate(t.Context(), ticket(discern.JevOnly))
			require.ErrorIs(t, err, discern.ErrMalformedResponse)
			pe := testutil.ProviderError(t, err)
			reason := errors.Unwrap(pe)
			require.Error(t, reason)
			assert.Contains(t, reason.Error(), tt.reason)
		})
	}
}

func TestNilFakeIsRejected(t *testing.T) {
	var fake *discerntest.Fake
	var (
		ev  *discern.Evaluator
		err error
	)
	require.NotPanics(t, func() { ev, err = discern.New(discern.WithAdapter(fake)) })
	require.ErrorIs(t, err, discern.ErrInvalidConfig)
	assert.Nil(t, ev)
}

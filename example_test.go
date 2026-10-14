package discern_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/discerntest"
)

// exampleRequest is a synthetic support ticket with one routing question.
func exampleRequest(providers discern.ProviderSet) discern.Request {
	return discern.Request{
		Providers: providers,
		State:     discern.Text("Subject: Charged twice\n\nMy invoice was paid twice this month."),
		Questions: []discern.Question{
			discern.Choice("queue", discern.Text("Which support queue should handle this ticket?"),
				discern.Opt("billing", discern.Text("Invoices, refunds, failed payments")),
				discern.Opt("technical", discern.Text("Errors, outages, integration problems")),
				discern.Opt("account", discern.Text("Login, profile, permissions"))),
		},
		CorrelationID: "ticket-1042",
	}
}

// exampleObservation is what a fake provider answers for exampleRequest.
func exampleObservation(model, option string, billing, technical, account float64) discern.Observation {
	return discern.Observation{Model: model, Answers: []discern.Answer{{
		QuestionID: "queue", Kind: discern.KindChoice,
		Choice: &discern.ChoiceAnswer{Option: option, Probabilities: []discern.OptionProbability{
			{Option: "billing", Probability: billing},
			{Option: "technical", Probability: technical},
			{Option: "account", Probability: account},
		}},
	}}}
}

func ExampleEvaluator_Evaluate() {
	fake := discerntest.Static(discern.Clef, exampleObservation("clef-flash", "billing", 0.87, 0.1, 0.03))
	ev, err := discern.New(discern.WithAdapter(fake))
	if err != nil {
		fmt.Println(err)
		return
	}

	res, err := ev.Evaluate(context.Background(), exampleRequest(discern.ClefOnly))
	if err != nil {
		fmt.Println(err)
		return
	}
	obs, _ := res.Observation(discern.Clef)
	q, _ := obs.Choice("queue")
	fmt.Println(res.Status, q.Option)
	for _, p := range q.Probabilities {
		fmt.Println(p.Option, p.Probability)
	}
	fmt.Println(fake.Calls(), "call")
	// Output:
	// complete billing
	// billing 0.87
	// technical 0.1
	// account 0.03
	// 1 call
}

func ExampleEvaluator_Evaluate_dualWithPolicy() {
	jev := discerntest.Static(discern.Jev, exampleObservation("jev-1.13.0", "billing", 0.91, 0.06, 0.03))
	clef := discerntest.Static(discern.Clef, exampleObservation("clef", "billing", 0.87, 0.1, 0.03))
	routing := discern.Policy{
		Version: "routing-v3",
		Questions: []discern.QuestionPolicy{{
			Question: "queue",
			Checks: []discern.Check{
				{ID: "queue-jev", Provider: discern.Jev, Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.80},
				{ID: "queue-clef", Provider: discern.Clef, Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.75},
			},
		}},
	}
	ev, err := discern.New(discern.WithAdapter(jev), discern.WithAdapter(clef), discern.WithDefaultPolicy(routing))
	if err != nil {
		fmt.Println(err)
		return
	}

	res, err := ev.Evaluate(context.Background(), exampleRequest(discern.JevAndClef))
	switch {
	case err == nil:
	case errors.Is(err, discern.ErrPartialFailure):
		fmt.Println("one provider failed")
	default:
		fmt.Println(err)
		return
	}
	rec, _ := res.Recommendation("queue")
	fmt.Println(rec.Status, rec.Selected.Option, rec.Selected.Providers)
	fmt.Println(rec.Reasons)
	fmt.Println(rec.Summary)
	fmt.Println(res.Overall.Status)
	// Output:
	// accepted billing [jev clef]
	// [providers_agree]
	// accepted billing: jev choice.probability 0.91 ≥ 0.8; clef choice.probability 0.87 ≥ 0.75; both chose billing
	// accepted
}

func ExampleRecommend() {
	fake := discerntest.Static(discern.Jev, exampleObservation("jev-1.13.0", "technical", 0.3, 0.62, 0.08))
	ev, err := discern.New(discern.WithAdapter(fake))
	if err != nil {
		fmt.Println(err)
		return
	}
	res, err := ev.Evaluate(context.Background(), exampleRequest(discern.JevOnly))
	if err != nil {
		fmt.Println(err)
		return
	}

	// Replay two candidate thresholds locally: no further provider calls.
	for _, threshold := range []float64{0.6, 0.7} {
		pol := discern.Policy{Version: fmt.Sprintf("trial-%.1f", threshold), Questions: []discern.QuestionPolicy{{
			Question: "queue",
			Checks:   []discern.Check{{Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: threshold}},
		}}}
		again, err := discern.Recommend(res, pol)
		if err != nil {
			fmt.Println(err)
			return
		}
		rec, _ := again.Recommendation("queue")
		fmt.Println(pol.Version, rec.Status)
	}
	fmt.Println(fake.Calls(), "call")
	// Output:
	// trial-0.6 accepted
	// trial-0.7 below_threshold
	// 1 call
}

func ExampleParseResult() {
	fake := discerntest.Static(discern.Jev, exampleObservation("jev-1.13.0", "billing", 0.91, 0.06, 0.03))
	ev, err := discern.New(discern.WithAdapter(fake))
	if err != nil {
		fmt.Println(err)
		return
	}
	res, err := ev.Evaluate(context.Background(), exampleRequest(discern.JevOnly))
	if err != nil {
		fmt.Println(err)
		return
	}

	data, err := json.Marshal(res.Redact(discern.Redaction{CorrelationID: true}))
	if err != nil {
		fmt.Println(err)
		return
	}
	restored, err := discern.ParseResult(data)
	if err != nil {
		fmt.Println(err)
		return
	}
	obs, _ := restored.Observation(discern.Jev)
	q, _ := obs.Choice("queue")
	fmt.Println(restored.SchemaVersion, restored.Redacted, q.Option)
	// Output:
	// discern.result/v1 [correlation_id] billing
}

func ExampleScoreAnswer_Nearest() {
	s := discern.ScoreAnswer{Value: 1.05, Probabilities: []discern.LevelProbability{
		{Level: "cosmetic", Probability: 0.0},
		{Level: "degraded", Probability: 0.95},
		{Level: "blocked", Probability: 0.05},
	}}
	fmt.Println(s.Nearest())
	// Output: degraded
}

// Package main routes synthetic support tickets with go-discern.
//
// By default it runs offline against fake providers, so it is safe to run and is
// exercised by the test suite:
//
//	go run ./examples
//
// With -live it calls TypeSafe Jev and Cloudflare Clef instead. Every live run
// makes billable calls, so it also requires DISCERN_LIVE=1 and reads the
// credentials from TYPESAFE_API_KEY, CLOUDFLARE_ACCOUNT_ID and
// CLOUDFLARE_API_TOKEN. The library itself never reads the environment; this
// program does, as an application would.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/jev"
)

var errLiveNotEnabled = errors.New("live mode makes billable calls: set DISCERN_LIVE=1 and the provider credentials")

func main() { os.Exit(realMain(os.Args, os.Stdout, os.Stderr)) }

// realMain runs the program for the command line args, program name first, and
// returns the exit status: 0 on success or after -h, 2 for an invalid flag, and 1
// when the run fails, with the error printed to stderr.
func realMain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	live := fs.Bool("live", false, "call the real providers (billable; requires DISCERN_LIVE=1 and credentials)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, stdout, *live); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// run evaluates one ticket on both providers, replays a stricter policy locally,
// and routes a small batch on one provider.
func run(ctx context.Context, w io.Writer, live bool) error {
	adapters, err := providers(live)
	if err != nil {
		return err
	}
	return runWith(ctx, w, adapters)
}

// runWith is run with the Jev and Clef adapters already built.
func runWith(ctx context.Context, w io.Writer, adapters [2]discern.Adapter) error {
	ev, err := discern.New(
		discern.WithAdapter(adapters[0]),
		discern.WithAdapter(adapters[1]),
		discern.WithDefaultPolicy(routingPolicy()),
		discern.WithBatchConcurrency(2),
	)
	if err != nil {
		return err
	}

	// One ticket, both providers: exactly two potentially billable calls.
	res, err := ev.Evaluate(ctx, ticket("ticket-1042", "Charged twice",
		"My card was charged twice for this month's invoice. Please refund the duplicate.", discern.JevAndClef))
	if res != nil {
		report(w, res) // persist or log the record before looking at err
	}
	switch {
	case err == nil:
	case errors.Is(err, discern.ErrPartialFailure):
		_, _ = fmt.Fprintln(w, "one provider failed; the other's observation is still usable")
	case res != nil:
		return fmt.Errorf("both providers failed: %w", err)
	default:
		return fmt.Errorf("nothing was sent: %w", err)
	}

	// Replay a stricter policy on the stored result: no further calls.
	strict := routingPolicy()
	strict.Version = "routing-v4-strict"
	strict.Questions[0].Checks[0].Threshold = 0.95
	again, err := discern.Recommend(res, strict)
	if err != nil {
		return err
	}
	rec, _ := again.Recommendation("queue")
	_, _ = fmt.Fprintf(w, "\nreplayed with %s: queue is %s\n  %s\n", strict.Version, rec.Status, rec.Summary)

	// The stored record: versioned JSON without state, instructions or credentials.
	data, err := json.MarshalIndent(res.Redact(discern.Redaction{CorrelationID: true}), "", "  ")
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "\nrecord (%d bytes, correlation ID redacted):\n%s\n", len(data), data)

	// A small batch on Clef alone, with at most two requests in flight.
	items, err := ev.EvaluateBatch(ctx, []discern.Request{
		ticket("ticket-1043", "Cannot log in", "The password reset link says it has expired.", discern.ClefOnly),
		ticket("ticket-1044", "API errors", "Every webhook call has returned 500 since this morning.", discern.ClefOnly),
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(w, "\nbatch:")
	for _, it := range items {
		if it.Err != nil && !errors.Is(it.Err, discern.ErrPartialFailure) {
			_, _ = fmt.Fprintf(w, "  failed: %v\n", it.Err)
			continue
		}
		obs, _ := it.Result.Observation(discern.Clef)
		q, _ := obs.Choice("queue")
		_, _ = fmt.Fprintf(w, "  %s -> %s (%.2f)\n", it.Result.CorrelationID, q.Option, probability(q))
	}
	return nil
}

// ticket builds a request for a synthetic support ticket.
func ticket(id, subject, body string, providers discern.ProviderSet) discern.Request {
	outage := discern.Noul("outage", discern.Text("Does the customer report a production outage?"))
	outage.Yes = discern.Text("A production system is down or failing for the customer")
	outage.No = discern.Text("No production impact is described")
	return discern.Request{
		Providers: providers,
		State:     discern.Text(subject + "\n\n" + body),
		Questions: []discern.Question{
			discern.Choice("queue", discern.Text("Which support queue should handle this ticket?"),
				discern.Opt("billing", discern.Text("Invoices, refunds, failed payments")),
				discern.Opt("technical", discern.Text("Errors, outages, integration problems")),
				discern.Opt("account", discern.Text("Login, profile, permissions"))),
			outage,
			discern.Score("severity", discern.Text("How severe is the customer impact?"),
				discern.Level("cosmetic", discern.Text("Cosmetic; work continues")),
				discern.Level("degraded", discern.Text("Degraded; a workaround exists")),
				discern.Level("blocked", discern.Text("Blocked; no workaround"))),
		},
		CorrelationID: id,
		EvidenceRefs:  []string{"kb:article-17"},
	}
}

// routingPolicy accepts a queue when both providers agree with enough probability
// and a severity when both are low enough and within a quarter level of each other.
func routingPolicy() discern.Policy {
	return discern.Policy{
		Version: "routing-v3",
		Questions: []discern.QuestionPolicy{{
			Question: "queue",
			Checks: []discern.Check{
				{ID: "queue-jev", Provider: discern.Jev, Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.80},
				{ID: "queue-clef", Provider: discern.Clef, Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.75},
			},
		}, {
			Question:  "severity",
			Checks:    []discern.Check{{ID: "severity-max", Metric: discern.MetricScore, Op: discern.AtMost, Threshold: 1.5}},
			Agreement: &discern.Agreement{MaxDifference: 0.25},
		}},
	}
}

// report prints each provider's observation and the recommendations.
func report(w io.Writer, res *discern.Result) {
	_, _ = fmt.Fprintf(w, "status %s in %s\n", res.Status, res.Elapsed.Round(time.Millisecond))
	for _, o := range res.Outcomes {
		if o.Observation == nil {
			_, _ = fmt.Fprintf(w, "%s (%s): failed: %s\n", o.Provider, o.RequestedModel, o.Failure.Kind)
			continue
		}
		obs := o.Observation
		q, _ := obs.Choice("queue")
		n, _ := obs.Noul("outage")
		s, _ := obs.Score("severity")
		_, _ = fmt.Fprintf(w, "%s (%s):\n  queue %s", o.Provider, obs.Model, q.Option)
		for _, p := range q.Probabilities {
			_, _ = fmt.Fprintf(w, " [%s %.2f]", p.Option, p.Probability)
		}
		_, _ = fmt.Fprintf(w, "\n  outage P(yes) %.2f\n  severity %.2f (nearest %s)\n", n.Value, s.Value, s.Nearest())
	}
	for _, rec := range res.Recommendations {
		_, _ = fmt.Fprintf(w, "%s: %s %v\n  %s\n", rec.QuestionID, rec.Status, rec.Reasons, rec.Summary)
	}
	_, _ = fmt.Fprintf(w, "overall: %s (not covered: %v)\n", res.Overall.Status, res.Overall.Uncovered)
}

// probability returns the probability of the chosen option.
func probability(q discern.ChoiceAnswer) float64 {
	for _, p := range q.Probabilities {
		if p.Option == q.Option {
			return p.Probability
		}
	}
	return 0
}

// providers returns the Jev and Clef adapters: fakes by default, real clients in
// live mode.
func providers(live bool) ([2]discern.Adapter, error) {
	if !live {
		return [2]discern.Adapter{
			discerntest.Static(discern.Jev, fakeObservation("jev-1.13.0", [3]float64{0.91, 0.06, 0.03}, 0.07, [3]float64{0.0, 0.95, 0.05})),
			discerntest.Static(discern.Clef, fakeObservation("clef-flash", [3]float64{0.87, 0.10, 0.03}, 0.10, [3]float64{0.0, 0.90, 0.10})),
		}, nil
	}
	if os.Getenv("DISCERN_LIVE") != "1" {
		return [2]discern.Adapter{}, errLiveNotEnabled
	}
	jc, err := jev.New(os.Getenv("TYPESAFE_API_KEY"), jev.WithModel("jev-1.13.0"), jev.WithTimeout(15*time.Second))
	if err != nil {
		return [2]discern.Adapter{}, err
	}
	cc, err := clef.New(os.Getenv("CLOUDFLARE_ACCOUNT_ID"), os.Getenv("CLOUDFLARE_API_TOKEN"),
		clef.WithModel(clef.ModelFlash), clef.WithTimeout(15*time.Second))
	if err != nil {
		return [2]discern.Adapter{}, err
	}
	return [2]discern.Adapter{jc, cc}, nil
}

// fakeObservation answers every ticket the same way; queue probabilities are for
// billing, technical and account, and levels for cosmetic, degraded and blocked.
func fakeObservation(model string, queue [3]float64, outage float64, levels [3]float64) discern.Observation {
	score := levels[1] + 2*levels[2]
	return discern.Observation{
		Model: model,
		Answers: []discern.Answer{
			{QuestionID: "queue", Kind: discern.KindChoice, Choice: &discern.ChoiceAnswer{
				Option: "billing", Confidence: new(0.8),
				Probabilities: []discern.OptionProbability{
					{Option: "billing", Probability: queue[0]},
					{Option: "technical", Probability: queue[1]},
					{Option: "account", Probability: queue[2]},
				},
			}},
			{QuestionID: "outage", Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: outage}},
			{QuestionID: "severity", Kind: discern.KindScore, Score: &discern.ScoreAnswer{
				Value: score,
				Probabilities: []discern.LevelProbability{
					{Level: "cosmetic", Probability: levels[0]},
					{Level: "degraded", Probability: levels[1]},
					{Level: "blocked", Probability: levels[2]},
				},
			}},
		},
	}
}

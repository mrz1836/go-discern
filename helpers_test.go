package discern_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
)

// Question, option and level IDs of the synthetic ticket request.
const (
	qQueue        = "queue"
	qOutage       = "outage"
	qSeverity     = "severity"
	optBilling    = "billing"
	optTechnical  = "technical"
	levelCosmetic = "cosmetic"
	levelBlocked  = "blocked"
)

// mutated is written into copies to show that the original does not change.
const mutated = "mutated"

// Planted strings that must never appear in records or error strings.
const (
	stateSentinel       = "STATE-SENTINEL-7f3a"
	instructionSentinel = "INSTRUCTION-SENTINEL-91bc"
	descriptionSentinel = "DESCRIPTION-SENTINEL-c2d4"
)

var (
	errFakeTransport = errors.New("fake transport failure")
	errFakeAdapter   = errors.New("fake adapter failure")
	errFakePrepare   = errors.New("fake prepare failure")
)

// ticketRequest is a synthetic support-ticket request with one question of each kind.
func ticketRequest(providers discern.ProviderSet) discern.Request {
	return discern.Request{
		Providers: providers,
		State:     discern.Text("Subject: Charged twice\n\nMy invoice was paid twice this month. " + stateSentinel),
		Questions: []discern.Question{
			discern.Choice(qQueue, discern.Text("Which support queue should handle this ticket? "+instructionSentinel),
				discern.Opt(optBilling, discern.Text("Invoices, refunds, failed payments "+descriptionSentinel)),
				discern.Opt(optTechnical, discern.Text("Errors, outages, integration problems")),
				discern.Opt("account", discern.Text("Login, profile, permissions"))),
			discern.Noul(qOutage, discern.Text("Does the customer report a production outage?")),
			discern.Score(qSeverity, discern.Text("How severe is the customer impact?"),
				discern.Level(levelCosmetic, discern.Text("Cosmetic; work continues")),
				discern.Level("degraded", discern.Text("Degraded; workaround exists")),
				discern.Level(levelBlocked, discern.Text("Blocked; no workaround"))),
		},
		CorrelationID: "ticket-1042",
		EvidenceRefs:  []string{"kb:article-17"},
	}
}

// ticketAnswers holds the numbers for one provider's answers to ticketRequest.
type ticketAnswers struct {
	model     string
	option    string
	probs     [3]float64 // billing, technical, account
	conf      *float64
	noul      float64
	score     float64
	levels    [3]float64 // cosmetic, degraded, blocked
	scoreConf *float64
}

func jevAnswers() ticketAnswers {
	return ticketAnswers{
		model: "jev-1.13.0", option: optBilling, probs: [3]float64{0.91, 0.06, 0.03}, conf: new(0.87),
		noul: 0.07, score: 1.05, levels: [3]float64{0.0, 0.95, 0.05}, scoreConf: new(0.92),
	}
}

func clefAnswers() ticketAnswers {
	return ticketAnswers{
		model: "clef", option: optBilling, probs: [3]float64{0.87, 0.1, 0.03}, conf: new(0.8),
		noul: 0.1, score: 1.1, levels: [3]float64{0.0, 0.9, 0.1}, scoreConf: new(0.85),
	}
}

func (t ticketAnswers) observation() discern.Observation {
	return discern.Observation{
		Model:     t.model,
		RequestID: "req-" + t.model,
		Usage:     &discern.Usage{InputTokens: new(int64(304)), OutputTokens: new(int64(0))},
		Answers: []discern.Answer{
			{QuestionID: qQueue, Kind: discern.KindChoice, Choice: &discern.ChoiceAnswer{
				Option: t.option, Confidence: t.conf,
				Probabilities: []discern.OptionProbability{
					{Option: optBilling, Probability: t.probs[0]},
					{Option: optTechnical, Probability: t.probs[1]},
					{Option: "account", Probability: t.probs[2]},
				},
			}},
			{QuestionID: qOutage, Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: t.noul}},
			{QuestionID: qSeverity, Kind: discern.KindScore, Score: &discern.ScoreAnswer{
				Value: t.score, Confidence: t.scoreConf,
				Probabilities: []discern.LevelProbability{
					{Level: levelCosmetic, Probability: t.levels[0]},
					{Level: "degraded", Probability: t.levels[1]},
					{Level: levelBlocked, Probability: t.levels[2]},
				},
			}},
		},
	}
}

// largeRequest returns a synthetic request with n questions, cycling through
// Choice questions with options options, Noul and Score questions, and a matching
// observation in request order.
func largeRequest(n, options int) (discern.Request, discern.Observation) {
	req := discern.Request{Providers: discern.JevOnly, State: discern.Text("Subject: Charged twice for invoice 1042")}
	var obs discern.Observation
	for i := range n {
		id := fmt.Sprintf("q%02d", i)
		switch i % 3 {
		case 0:
			opts := make([]discern.ChoiceOption, options)
			probs := make([]discern.OptionProbability, options)
			for j := range opts {
				opts[j] = discern.Opt(fmt.Sprintf("option-%03d", j), discern.Value{})
				probs[j] = discern.OptionProbability{Option: opts[j].ID}
			}
			probs[0].Probability = 0.91
			probs[1].Probability = 0.09
			req.Questions = append(req.Questions, discern.Choice(id, discern.Text("Which queue?"), opts...))
			obs.Answers = append(obs.Answers, discern.Answer{QuestionID: id, Kind: discern.KindChoice, Choice: &discern.ChoiceAnswer{
				Option: opts[0].ID, Confidence: new(0.87), Probabilities: probs,
			}})
		case 1:
			req.Questions = append(req.Questions, discern.Noul(id, discern.Text("Is this a duplicate charge?")))
			obs.Answers = append(obs.Answers, discern.Answer{QuestionID: id, Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: 0.07}})
		default:
			req.Questions = append(req.Questions, discern.Score(id, discern.Text("How severe?"),
				discern.Level("low", discern.Text("Low")), discern.Level("high", discern.Text("High"))))
			obs.Answers = append(obs.Answers, discern.Answer{QuestionID: id, Kind: discern.KindScore, Score: &discern.ScoreAnswer{
				Value: 0.25, Confidence: new(0.5), Probabilities: []discern.LevelProbability{{Level: "low", Probability: 0.75}, {Level: "high", Probability: 0.25}},
			}})
		}
	}
	return req, obs
}

// largePolicy returns a policy with one check per question of largeRequest.
func largePolicy(n int) discern.Policy {
	pol := discern.Policy{Version: "large-v1"}
	for i := range n {
		id := fmt.Sprintf("q%02d", i)
		var c discern.Check
		switch i % 3 {
		case 0:
			c = check(discern.MetricChoiceProbability, discern.AtLeast, 0.8)
		case 1:
			c = check(discern.MetricNoul, discern.AtMost, 0.1)
		default:
			c = check(discern.MetricScore, discern.AtMost, 0.5)
		}
		pol.Questions = append(pol.Questions, discern.QuestionPolicy{Question: id, Checks: []discern.Check{c}})
	}
	return pol
}

// mustEvaluate runs ticketRequest on ev and requires success.
func mustEvaluate(tb testing.TB, ev *discern.Evaluator, set discern.ProviderSet, opts ...discern.EvalOption) *discern.Result {
	tb.Helper()
	res, err := ev.Evaluate(tb.Context(), ticketRequest(set), opts...)
	require.NoError(tb, err)
	return res
}

// staticPair returns static fakes for both providers with the standard answers.
func staticPair() (*discerntest.Fake, *discerntest.Fake) {
	return discerntest.Static(discern.Jev, jevAnswers().observation()),
		discerntest.Static(discern.Clef, clefAnswers().observation())
}

// pairEvaluator returns an evaluator over staticPair, built with opts as well,
// for tests that never look at the fakes themselves.
func pairEvaluator(tb testing.TB, opts ...discern.Option) *discern.Evaluator {
	tb.Helper()
	jev, clef := staticPair()
	return testutil.Evaluator(tb, append([]discern.Option{discern.WithAdapter(jev), discern.WithAdapter(clef)}, opts...)...)
}

// countingPrepare is an adapter whose Prepare can fail; it counts calls.
type countingPrepare struct {
	provider discern.Provider
	err      error
	inner    *discerntest.Fake
}

func (c *countingPrepare) Provider() discern.Provider { return c.provider }

func (c *countingPrepare) Model() string { return c.inner.Model() }

func (c *countingPrepare) Prepare(req *discern.Request) (discern.Call, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.inner.Prepare(req)
}

// nilCallAdapter is an adapter whose Prepare returns neither a call nor an error.
type nilCallAdapter struct {
	provider discern.Provider
}

func (n *nilCallAdapter) Provider() discern.Provider { return n.provider }

func (n *nilCallAdapter) Model() string { return discerntest.DefaultModel }

func (n *nilCallAdapter) Prepare(*discern.Request) (discern.Call, error) {
	return nil, nil //nolint:nilnil // the adapter bug under test: neither a call nor an error
}

// blockingCall returns a fake call that waits for release (or ctx) before answering.
func blockingCall(release <-chan struct{}, obs discern.Observation) func(context.Context, *discern.Request) (*discern.Observation, error) {
	return func(ctx context.Context, _ *discern.Request) (*discern.Observation, error) {
		select {
		case <-release:
			c := obs.Clone()
			return &c, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

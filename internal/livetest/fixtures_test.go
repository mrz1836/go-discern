//go:build live

package livetest_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
)

const (
	pinnedJev = "jev-1.13.0"

	qQueue     = "queue"
	qRefund    = "refund"
	qUrgency   = "urgency"
	optBilling = "billing"

	ticket = "Subject: Charged twice\n\nHi, my card was charged twice for the March invoice (INV-2291). " +
		"Please refund the duplicate payment as soon as possible. This is the second time this has happened."
)

// versionedJev matches the versioned model Jev reports, such as jev-1.13.0.
func versionedJev() *regexp.Regexp { return regexp.MustCompile(`^jev-\d+\.\d+\.\d+$`) }

func supportQuestions() []discern.Question {
	return []discern.Question{
		discern.Choice(qQueue, discern.Text("Which support queue should handle this ticket?"),
			discern.Opt(optBilling, discern.Text("Invoices, refunds, failed or duplicate payments")),
			discern.Opt("technical", discern.Text("Errors, outages, integration problems")),
			discern.Opt("account", discern.Text("Login, profile, permissions"))),
		discern.Noul(qRefund, discern.Text("Is the customer asking for a refund?")),
		discern.Score(qUrgency, discern.Text("How urgent is this ticket?"),
			discern.Level("low", discern.Text("Can wait a week")),
			discern.Level("normal", discern.Text("Handle within two days")),
			discern.Level("high", discern.Text("Handle today")),
			discern.Level("critical", discern.Text("Handle immediately"))),
	}
}

// ticketRequest asks the three support questions about the billing ticket.
func ticketRequest(ps discern.ProviderSet) discern.Request {
	return discern.Request{Providers: ps, State: discern.Text(ticket), Questions: supportQuestions()}
}

// refundRequest asks only whether the ticket asks for a refund.
func refundRequest(ps discern.ProviderSet, state string) discern.Request {
	return discern.Request{
		Providers: ps,
		State:     discern.Text(state),
		Questions: []discern.Question{discern.Noul(qRefund, discern.Text("Is the customer asking for a refund?"))},
	}
}

// supportPolicy accepts the billing queue at 0.7, a refund request at 0.5 and an
// urgency up to the top level, with agreement limits for the numeric answers.
func supportPolicy(allowPartial bool) discern.Policy {
	return discern.Policy{
		Version: "live-1",
		Questions: []discern.QuestionPolicy{
			{Question: qQueue, Checks: []discern.Check{
				{ID: "queue-p", Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.7},
			}},
			{Question: qRefund, Checks: []discern.Check{
				{ID: "refund-yes", Metric: discern.MetricNoul, Op: discern.AtLeast, Threshold: 0.5},
			}, Agreement: &discern.Agreement{MaxDifference: 0.3}},
			{Question: qUrgency, Checks: []discern.Check{
				{ID: "urgency-cap", Metric: discern.MetricScore, Op: discern.AtMost, Threshold: 3},
			}, Agreement: &discern.Agreement{MaxDifference: 1}},
		},
		AllowPartial: allowPartial,
	}
}

// logLines returns n distinct log lines, about 28 tokens each.
func logLines(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "Log %d: customer %d reported that exporting invoices from the dashboard shows error E%d after the March update.\n",
			i, i*7%1000, i%97)
	}
	return b.String()
}

// requireComplete stops t unless every selected provider answered.
func requireComplete(t *testing.T, res *discern.Result, err error) {
	t.Helper()
	require.NoError(t, err, changed(res, err))
	require.NotNil(t, res)
	require.Equal(t, discern.Complete, res.Status, changed(res, err))
}

// requireObservation returns p's observation, stopping t if there is none.
func requireObservation(t *testing.T, res *discern.Result, p discern.Provider) discern.Observation {
	t.Helper()
	obs, ok := res.Observation(p)
	require.True(t, ok, changed(res, nil))
	return obs
}

// assertJevMeta checks what Jev reports with every answer: a versioned model,
// the requested one when it was versioned, input and output token counts, and a
// request ID but no ray ID.
func assertJevMeta(t *testing.T, res *discern.Result, requested string) {
	t.Helper()
	obs := requireObservation(t, res, discern.Jev)
	msg := changed(res, nil)
	assert.Regexp(t, versionedJev(), obs.Model, msg)
	if versionedJev().MatchString(requested) {
		assert.Equal(t, requested, obs.Model, msg)
	}
	require.NotNil(t, obs.Usage, msg)
	require.NotNil(t, obs.Usage.InputTokens, msg)
	require.NotNil(t, obs.Usage.OutputTokens, msg)
	assert.Positive(t, *obs.Usage.InputTokens, msg)
	assert.Positive(t, *obs.Usage.OutputTokens, msg)
	assert.Nil(t, obs.Usage.Neurons, msg)
	assert.NotEmpty(t, obs.RequestID, msg)
	assert.Empty(t, obs.RayID, msg)
}

// assertClefMeta checks what Clef reports with every answer: the short model
// name, input tokens with zero output tokens, neurons, and request and ray IDs.
func assertClefMeta(t *testing.T, res *discern.Result, model string) {
	t.Helper()
	obs := requireObservation(t, res, discern.Clef)
	msg := changed(res, nil)
	assert.Equal(t, model, obs.Model, msg)
	require.NotNil(t, obs.Usage, msg)
	require.NotNil(t, obs.Usage.InputTokens, msg)
	require.NotNil(t, obs.Usage.OutputTokens, msg)
	require.NotNil(t, obs.Usage.Neurons, msg)
	assert.Positive(t, *obs.Usage.InputTokens, msg)
	assert.Zero(t, *obs.Usage.OutputTokens, msg)
	assert.Positive(t, *obs.Usage.Neurons, msg)
	assert.NotEmpty(t, obs.RequestID, msg)
	assert.NotEmpty(t, obs.RayID, msg)
}

// assertSupportAnswers checks the robust answers to the billing ticket: the
// billing queue, a refund request when checkRefund is set (clef-flash hovers
// near the line), and an urgency within the four levels.
func assertSupportAnswers(t *testing.T, res *discern.Result, p discern.Provider, checkRefund bool) {
	t.Helper()
	obs := requireObservation(t, res, p)
	msg := changed(res, nil)
	q, ok := obs.Choice(qQueue)
	require.True(t, ok, msg)
	assert.Equal(t, optBilling, q.Option, msg)
	assert.Len(t, q.Probabilities, 3, msg)
	n, ok := obs.Noul(qRefund)
	require.True(t, ok, msg)
	if checkRefund {
		assert.GreaterOrEqual(t, n.Value, 0.5, msg)
	}
	u, ok := obs.Score(qUrgency)
	require.True(t, ok, msg)
	assert.Len(t, u.Probabilities, 4, msg)
	assert.GreaterOrEqual(t, u.Value, 0.0, msg)
	assert.LessOrEqual(t, u.Value, 3.0, msg)
}

// assertYes checks that p answers the Noul question id with yes: a value of at
// least 0.5.
func assertYes(t *testing.T, res *discern.Result, p discern.Provider, id string) {
	t.Helper()
	obs := requireObservation(t, res, p)
	n, ok := obs.Noul(id)
	require.True(t, ok, changed(res, nil))
	assert.GreaterOrEqual(t, n.Value, 0.5, changed(res, nil))
}

// requireRejected checks a single-provider refusal with the HTTP status and
// returns its failure.
func requireRejected(t *testing.T, res *discern.Result, err error, status int) *discern.Failure {
	t.Helper()
	require.ErrorIs(t, err, discern.ErrRejected, changed(res, err))
	require.NotNil(t, res, changed(res, err))
	require.Len(t, res.Outcomes, 1, changed(res, err))
	f := res.Outcomes[0].Failure
	require.NotNil(t, f, changed(res, err))
	assert.Equal(t, discern.FailureRejected, f.Kind, changed(res, err))
	assert.Equal(t, status, f.StatusCode, changed(res, err))
	return f
}

// assertMessage checks that one of the provider messages err carries holds
// want.
func assertMessage(t *testing.T, res *discern.Result, err error, want string) {
	t.Helper()
	msgs := providerMessages(err)
	assert.True(t, slices.ContainsFunc(msgs, func(m string) bool { return strings.Contains(m, want) }),
		"%s; want a message containing %q", changed(res, err), want)
}

// describe renders an evaluation's metadata for logs and failure messages:
// statuses, models, IDs, usage, failure details and the first answers. It
// holds no credentials, state or instructions.
func describe(res *discern.Result, err error) string {
	var b strings.Builder
	if err != nil {
		fmt.Fprintf(&b, "err=%q ", err)
	}
	if msgs := providerMessages(err); len(msgs) > 0 {
		fmt.Fprintf(&b, "messages=%q ", msgs)
	}
	if res == nil {
		b.WriteString("no call made")
		return b.String()
	}
	fmt.Fprintf(&b, "status=%s", res.Status)
	for _, o := range res.Outcomes {
		fmt.Fprintf(&b, " | %s requested=%s %dms", o.Provider, o.RequestedModel, o.Elapsed.Milliseconds())
		if o.Failure != nil {
			describeFailure(&b, o.Failure)
			continue
		}
		describeObservation(&b, o.Observation)
	}
	return b.String()
}

func describeFailure(b *strings.Builder, f *discern.Failure) {
	fmt.Fprintf(b, " FAILED kind=%s", f.Kind)
	if f.StatusCode != 0 {
		fmt.Fprintf(b, " http=%d", f.StatusCode)
	}
	if len(f.Codes) > 0 {
		fmt.Fprintf(b, " codes=%v", f.Codes)
	}
	if f.RetryAfter != nil {
		fmt.Fprintf(b, " retry_after=%s", *f.RetryAfter)
	}
	fmt.Fprintf(b, " request_id=%q ray_id=%q", f.RequestID, f.RayID)
}

func describeObservation(b *strings.Builder, obs *discern.Observation) {
	fmt.Fprintf(b, " model=%q request_id=%q ray_id=%q", obs.Model, obs.RequestID, obs.RayID)
	if u := obs.Usage; u != nil {
		if u.InputTokens != nil {
			fmt.Fprintf(b, " in=%d", *u.InputTokens)
		}
		if u.OutputTokens != nil {
			fmt.Fprintf(b, " out=%d", *u.OutputTokens)
		}
		if u.Neurons != nil {
			fmt.Fprintf(b, " neurons=%.2f", *u.Neurons)
		}
	}
	for i, a := range obs.Answers {
		if i == 3 {
			fmt.Fprintf(b, " (+%d answers)", len(obs.Answers)-3)
			break
		}
		b.WriteString(" " + describeAnswer(a))
	}
}

func describeAnswer(a discern.Answer) string {
	switch {
	case a.Choice != nil:
		p := 0.0
		for _, op := range a.Choice.Probabilities {
			if op.Option == a.Choice.Option {
				p = op.Probability
			}
		}
		return fmt.Sprintf("%s=%s(%.3f)", a.QuestionID, a.Choice.Option, p)
	case a.Noul != nil:
		return fmt.Sprintf("%s=%.3f", a.QuestionID, a.Noul.Value)
	case a.Score != nil:
		return fmt.Sprintf("%s=%.2f(%s)", a.QuestionID, a.Score.Value, a.Score.Nearest())
	}
	return a.QuestionID + "=?"
}

// describeRecommendations renders the overall status and each question's
// status, selection and reasons.
func describeRecommendations(res *discern.Result) string {
	if res == nil {
		return "no result"
	}
	parts := make([]string, 0, len(res.Recommendations))
	for _, r := range res.Recommendations {
		p := fmt.Sprintf("%s:%s", r.QuestionID, r.Status)
		if r.Selected != nil && r.Selected.Option != "" {
			p += "=" + r.Selected.Option
		}
		parts = append(parts, fmt.Sprintf("%s%v", p, r.Reasons))
	}
	return fmt.Sprintf("overall=%s %s", res.Overall.Status, strings.Join(parts, " "))
}

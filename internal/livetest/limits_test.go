//go:build live

package livetest_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
)

const targetJev = "jev"

// limitTests checks requests at the documented limits: 64 questions, 255
// options and 10 levels. Jev also takes 65 questions, more than Clef's limit.
func (s *suite) limitTests(t *testing.T) {
	for _, target := range []string{targetJev, clef.ModelFlash} {
		s.run(t, "64-questions/"+target, needFor(target), func(t *testing.T) { s.manyQuestions(t, target, 64) })
	}
	// Observed in October 2026: Jev documents no question limit and answered 65.
	s.run(t, "65-questions/jev", needJev, func(t *testing.T) { s.manyQuestions(t, targetJev, 65) })
	for _, target := range []string{targetJev, clef.ModelFlash} {
		s.run(t, "255-options/"+target, needFor(target), func(t *testing.T) { s.manyOptions(t, target) })
	}
	s.run(t, "10-levels/dual", needBoth, s.tenLevels)
}

// target returns a single-provider evaluator for Jev (pinned) or a Clef model,
// with the request's provider set and the provider.
func (s *suite) target(t *testing.T, name string) (*discern.Evaluator, discern.ProviderSet, discern.Provider) {
	t.Helper()
	if name == targetJev {
		return evaluator(t, s.jev(t)), discern.JevOnly, discern.Jev
	}
	return evaluator(t, s.clef(t, name)), discern.ClefOnly, discern.Clef
}

// needFor returns what a test on the target needs.
func needFor(target string) need {
	if target == targetJev {
		return needJev
	}
	return needClef
}

func (s *suite) manyQuestions(t *testing.T, target string, n int) {
	ev, ps, p := s.target(t, target)
	qs := make([]discern.Question, n)
	for i := range qs {
		qs[i] = discern.Noul(fmt.Sprintf("q%02d", i), discern.Text(fmt.Sprintf("Does the ticket mention the number %d?", i)))
	}
	res, err := s.evaluate(t, ev, discern.Request{Providers: ps, State: discern.Text(ticket + " Orders 7, 12 and 2291."), Questions: qs})
	requireComplete(t, res, err)
	obs := requireObservation(t, res, p)
	require.Len(t, obs.Answers, n, changed(res, err))
	for i, a := range obs.Answers {
		assert.Equal(t, fmt.Sprintf("q%02d", i), a.QuestionID)
	}
}

func (s *suite) manyOptions(t *testing.T, target string) {
	ev, ps, p := s.target(t, target)
	opts := make([]discern.ChoiceOption, 255)
	opts[0] = discern.Opt("o000", discern.Text("Billing: invoices, refunds, duplicate payments"))
	for i := 1; i < len(opts); i++ {
		opts[i] = discern.Opt(fmt.Sprintf("o%03d", i), discern.Text(fmt.Sprintf("Unrelated category number %d", i)))
	}
	res, err := s.evaluate(t, ev, discern.Request{
		Providers: ps,
		State:     discern.Text(ticket),
		Questions: []discern.Question{discern.Choice("topic", discern.Text("Which category fits the ticket?"), opts...)},
	})
	requireComplete(t, res, err)
	obs := requireObservation(t, res, p)
	c, ok := obs.Choice("topic")
	require.True(t, ok, changed(res, err))
	assert.Equal(t, "o000", c.Option, changed(res, err))
	assert.Len(t, c.Probabilities, len(opts))
}

func (s *suite) tenLevels(t *testing.T) {
	levels := make([]discern.ScoreLevel, 10)
	for i := range levels {
		levels[i] = discern.Level(fmt.Sprintf("l%d", i), discern.Text(fmt.Sprintf("Anger level %d of 9", i)))
	}
	ev := evaluator(t, s.jev(t), s.clef(t, clef.ModelFlash))
	res, err := s.evaluate(t, ev, discern.Request{
		Providers: discern.JevAndClef,
		State:     discern.Text(ticket),
		Questions: []discern.Question{discern.Score("anger", discern.Text("How angry is the customer?"), levels...)},
	})
	requireComplete(t, res, err)
	for _, p := range []discern.Provider{discern.Jev, discern.Clef} {
		obs := requireObservation(t, res, p)
		sc, ok := obs.Score("anger")
		require.True(t, ok, changed(res, err))
		assert.Len(t, sc.Probabilities, len(levels))
		assert.GreaterOrEqual(t, sc.Value, 0.0)
		assert.LessOrEqual(t, sc.Value, 9.0)
	}
}

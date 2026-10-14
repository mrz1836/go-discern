//go:build live

package livetest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
	"github.com/mrz1836/go-discern/jev"
)

// jevTests checks Jev's answers and the model names it reports.
func (s *suite) jevTests(t *testing.T) {
	s.run(t, "kinds", needJev, func(t *testing.T) {
		res, err := s.evaluate(t, evaluator(t, s.jev(t)), ticketRequest(discern.JevOnly))
		requireComplete(t, res, err)
		assertJevMeta(t, res, pinnedJev)
		assertSupportAnswers(t, res, discern.Jev, true)
	})
	// The aliases resolve to a versioned model, which Jev reports.
	for _, model := range []string{jev.DefaultModel, "jev-preview"} {
		s.run(t, "alias/"+model, needJev, func(t *testing.T) {
			res, err := s.evaluate(t, evaluator(t, s.jev(t, jev.WithModel(model))), refundRequest(discern.JevOnly, ticket))
			requireComplete(t, res, err)
			require.Len(t, res.Outcomes, 1)
			assert.Equal(t, model, res.Outcomes[0].RequestedModel)
			assertJevMeta(t, res, model)
		})
	}
}

// clefTests checks each Clef model's answers and metadata.
func (s *suite) clefTests(t *testing.T) {
	for _, model := range []string{clef.ModelClef, clef.ModelFlash, clef.ModelOmni} {
		s.run(t, "kinds/"+model, needClef, func(t *testing.T) {
			res, err := s.evaluate(t, evaluator(t, s.clef(t, model)), ticketRequest(discern.ClefOnly))
			requireComplete(t, res, err)
			assertClefMeta(t, res, model)
			assertSupportAnswers(t, res, discern.Clef, model != clef.ModelFlash)
		})
	}
}

package discern_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
)

func TestNewConfigErrors(t *testing.T) {
	jev, clef := staticPair()
	tests := []struct {
		name string
		opts []discern.Option
		want error
	}{
		{"no adapters", nil, discern.ErrInvalidConfig},
		{"nil adapter", []discern.Option{discern.WithAdapter(nil)}, discern.ErrInvalidConfig},
		{"unknown provider", []discern.Option{discern.WithAdapter(discerntest.Static("other", discern.Observation{}))}, discern.ErrInvalidConfig},
		{"duplicate provider", []discern.Option{discern.WithAdapter(jev), discern.WithAdapter(discerntest.Static(discern.Jev, discern.Observation{}))}, discern.ErrInvalidConfig},
		{"model with a line break", []discern.Option{discern.WithAdapter(discerntest.Func(discern.Jev, "jev-1.13.0\nforged", nil))}, discern.ErrInvalidConfig},
		{"model longer than 256 bytes", []discern.Option{discern.WithAdapter(discerntest.Func(discern.Jev, strings.Repeat("m", 257), nil))}, discern.ErrInvalidConfig},
		{"batch concurrency zero", []discern.Option{discern.WithAdapter(jev), discern.WithBatchConcurrency(0)}, discern.ErrInvalidConfig},
		{"default policy without version", []discern.Option{discern.WithAdapter(jev), discern.WithDefaultPolicy(discern.Policy{})}, discern.ErrInvalidPolicy},
		{
			"default policy names unregistered provider",
			[]discern.Option{discern.WithAdapter(jev), discern.WithDefaultPolicy(discern.Policy{Version: "v1", PreferOnConflict: discern.Clef})},
			discern.ErrInvalidPolicy,
		},
		{
			"default policy check names unregistered provider",
			[]discern.Option{discern.WithAdapter(clef), discern.WithDefaultPolicy(discern.Policy{Version: "v1", Questions: []discern.QuestionPolicy{{
				Question: qQueue, Checks: []discern.Check{{Provider: discern.Jev, Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.5}},
			}}})},
			discern.ErrInvalidPolicy,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := discern.New(tt.opts...)
			require.ErrorIs(t, err, tt.want)
			assert.Nil(t, ev)
			ve := testutil.ValidationError(t, err)
			assert.NotEmpty(t, ve.Field)
		})
	}
}

func TestNewIgnoresNilOptions(t *testing.T) {
	jev, _ := staticPair()
	ev, err := discern.New(nil, discern.WithAdapter(jev), nil)
	require.NoError(t, err)
	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly), nil)
	require.NoError(t, err)
	assert.Equal(t, discern.Complete, res.Status)
}

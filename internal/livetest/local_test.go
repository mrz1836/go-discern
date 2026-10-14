//go:build live

package livetest_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
	"github.com/mrz1836/go-discern/internal/testutil"
	"github.com/mrz1836/go-discern/jev"
)

// localTests checks requests that real adapters must refuse before any call.
// The adapters hold placeholder credentials, so a call made by mistake would
// be refused rather than billed.
func (s *suite) localTests(t *testing.T) {
	jc := newJev(t, placeholderKey)
	cc := newClef(t, wrongAccount, placeholderKey, clef.ModelFlash)
	tooManyQuestions := make([]discern.Question, clef.MaxQuestions+1)
	for i := range tooManyQuestions {
		tooManyQuestions[i] = discern.Noul(fmt.Sprintf("q%02d", i), discern.Text("Is it about billing?"))
	}
	tooManyOptions := make([]discern.ChoiceOption, 256)
	for i := range tooManyOptions {
		tooManyOptions[i] = discern.Opt(fmt.Sprintf("o%03d", i), discern.Text("A category"))
	}
	jevRequest := func(state discern.Value, qs ...discern.Question) discern.Request {
		return discern.Request{Providers: discern.JevOnly, State: state, Questions: qs}
	}
	refund := discern.Noul(qRefund, discern.Text("Is the customer asking for a refund?"))

	tests := []struct {
		name     string
		adapter  discern.Adapter
		req      discern.Request
		opts     []discern.EvalOption
		canceled bool
		want     error
		field    string
	}{
		{name: "canceled-context", adapter: jc, req: refundRequest(discern.JevOnly, ticket), canceled: true, want: context.Canceled},
		{
			name: "65-questions-clef", adapter: cc, want: discern.ErrUnsupported, field: "questions",
			req: discern.Request{Providers: discern.ClefOnly, State: discern.Text(ticket), Questions: tooManyQuestions},
		},
		{
			name: "256-options", adapter: jc, want: discern.ErrInvalidRequest, field: "questions[0].options",
			req: jevRequest(discern.Text(ticket), discern.Choice("topic", discern.Text("Which topic?"), tooManyOptions...)),
		},
		{
			name: "duplicate-json-keys", adapter: jc, want: discern.ErrInvalidRequest, field: "state",
			req: jevRequest(discern.JSON([]byte(`{"a":1,"a":2}`)), refund),
		},
		{
			name: "bad-question-id", adapter: jc, want: discern.ErrInvalidRequest, field: "questions[0].id",
			req: jevRequest(discern.Text(ticket), discern.Noul("has space", discern.Text("Refund?"))),
		},
		{
			name: "blank-state", adapter: jc, want: discern.ErrInvalidRequest, field: "state",
			req: jevRequest(discern.Text("  \n\t "), refund),
		},
		{
			name: "unregistered-provider", adapter: jc, want: discern.ErrUnsupported, field: "providers",
			req: refundRequest(discern.ClefOnly, ticket),
		},
		{
			name: "invalid-policy", adapter: jc, want: discern.ErrInvalidPolicy, field: "version",
			req: refundRequest(discern.JevOnly, ticket), opts: []discern.EvalOption{discern.WithPolicy(discern.Policy{})},
		},
		{
			name: "response-size-precheck", adapter: newJev(t, placeholderKey, jev.WithMaxResponseBytes(64)),
			want: discern.ErrUnsupported, field: "request", req: ticketRequest(discern.JevOnly),
		},
	}
	for _, tt := range tests {
		s.run(t, tt.name, 0, func(t *testing.T) {
			ctx := t.Context()
			if tt.canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			res, err := s.call(ctx, evaluator(t, tt.adapter), tt.req, tt.opts...)
			s.checkLeaks(t, res, err)
			t.Log(describe(res, err))
			require.Nil(t, res, "a local rejection must make no call")
			require.ErrorIs(t, err, tt.want)
			if tt.field != "" {
				assert.Equal(t, tt.field, testutil.ValidationError(t, err).Field)
			}
		})
	}
}

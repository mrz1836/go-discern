package discern_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
)

func TestEvaluateInvalidRequestMakesNoCalls(t *testing.T) {
	options := func(n int) []discern.ChoiceOption {
		out := make([]discern.ChoiceOption, n)
		for i := range out {
			out[i] = discern.Opt(fmt.Sprintf("o%d", i), discern.Value{})
		}
		return out
	}
	levels := func(n int) []discern.ScoreLevel {
		out := make([]discern.ScoreLevel, n)
		for i := range out {
			out[i] = discern.Level(fmt.Sprintf("l%d", i), discern.Text("level"))
		}
		return out
	}
	instr := discern.Text("Which queue?")
	withQuestions := func(qs ...discern.Question) discern.Request {
		r := ticketRequest(discern.JevAndClef)
		r.Questions = qs
		return r
	}
	modify := func(f func(r *discern.Request)) discern.Request {
		r := ticketRequest(discern.JevAndClef)
		f(&r)
		return r
	}
	noulWith := func(yes discern.Value) discern.Question {
		q := discern.Noul(qOutage, instr)
		q.Yes = yes
		return q
	}
	tests := []struct {
		name string
		req  discern.Request
		want error
	}{
		{"providers unset", modify(func(r *discern.Request) { r.Providers = "" }), discern.ErrInvalidRequest},
		{"unknown provider set", modify(func(r *discern.Request) { r.Providers = "jev,clef" }), discern.ErrInvalidRequest},
		{"zero state", modify(func(r *discern.Request) { r.State = discern.Value{} }), discern.ErrInvalidRequest},
		{"scalar JSON state", modify(func(r *discern.Request) { r.State = discern.JSON([]byte(`42`)) }), discern.ErrInvalidRequest},
		{"null JSON state", modify(func(r *discern.Request) { r.State = discern.JSON([]byte(`null`)) }), discern.ErrInvalidRequest},
		{"invalid JSON state", modify(func(r *discern.Request) { r.State = discern.JSON([]byte(`{"a":`)) }), discern.ErrInvalidRequest},
		{"empty JSON state", modify(func(r *discern.Request) { r.State = discern.JSON(nil) }), discern.ErrInvalidRequest},
		{"invalid UTF-8 text state", modify(func(r *discern.Request) { r.State = discern.Text("bad \xff byte") }), discern.ErrInvalidRequest},
		{"invalid UTF-8 JSON state", modify(func(r *discern.Request) { r.State = discern.JSON([]byte("\"bad \xff\"")) }), discern.ErrInvalidRequest},
		{"empty text state", modify(func(r *discern.Request) { r.State = discern.Text("") }), discern.ErrInvalidRequest},
		{"white-space text state", modify(func(r *discern.Request) { r.State = discern.Text("  \n\t \r") }), discern.ErrInvalidRequest},
		{"blank JSON string state", modify(func(r *discern.Request) { r.State = discern.JSON([]byte(`"  "`)) }), discern.ErrInvalidRequest},
		{"escaped white-space JSON string state", modify(func(r *discern.Request) { r.State = discern.JSON([]byte(`" \n"`)) }), discern.ErrInvalidRequest},
		{"empty object state", modify(func(r *discern.Request) { r.State = discern.JSON([]byte(`{}`)) }), discern.ErrInvalidRequest},
		{"empty array state", modify(func(r *discern.Request) { r.State = discern.JSON([]byte(`[]`)) }), discern.ErrInvalidRequest},
		{"empty object state between spaces", modify(func(r *discern.Request) { r.State = discern.JSON([]byte(" \n{ } \t")) }), discern.ErrInvalidRequest},
		{"no questions", withQuestions(), discern.ErrInvalidRequest},
		{"empty question ID", withQuestions(discern.Noul("", instr)), discern.ErrInvalidRequest},
		{"question ID charset", withQuestions(discern.Noul("queue id", instr)), discern.ErrInvalidRequest},
		{"question ID too long", withQuestions(discern.Noul(strings.Repeat("q", 101), instr)), discern.ErrInvalidRequest},
		{"duplicate question ID", withQuestions(discern.Noul(qOutage, instr), discern.Noul(qOutage, instr)), discern.ErrInvalidRequest},
		{"unknown kind", withQuestions(discern.Question{ID: "q", Kind: "rank", Instructions: instr}), discern.ErrInvalidRequest},
		{"zero instructions", withQuestions(discern.Noul(qOutage, discern.Value{})), discern.ErrInvalidRequest},
		{"blank instructions", withQuestions(discern.Noul(qOutage, discern.Text(" \n\t"))), discern.ErrInvalidRequest},
		{"empty object instructions", withQuestions(discern.Noul(qOutage, discern.JSON([]byte(` { } `)))), discern.ErrInvalidRequest},
		{"empty JSON string instructions", withQuestions(discern.Noul(qOutage, discern.JSON([]byte(`"  "`)))), discern.ErrInvalidRequest},
		{"numeric instructions", withQuestions(discern.Noul(qOutage, discern.JSON([]byte(`7`)))), discern.ErrInvalidRequest},
		{"one option", withQuestions(discern.Choice(qQueue, instr, options(1)...)), discern.ErrInvalidRequest},
		{"256 options", withQuestions(discern.Choice(qQueue, instr, options(256)...)), discern.ErrInvalidRequest},
		{"duplicate option", withQuestions(discern.Choice(qQueue, instr, discern.Opt("a", discern.Value{}), discern.Opt("a", discern.Value{}))), discern.ErrInvalidRequest},
		{"duplicate option among many", withQuestions(discern.Choice(qQueue, instr, append(options(254), discern.Opt("o17", discern.Value{}))...)), discern.ErrInvalidRequest},
		{"duplicate question among many", withQuestions(func() []discern.Question {
			qs := make([]discern.Question, 40)
			for i := range qs {
				qs[i] = discern.Noul(fmt.Sprintf("q%d", i), instr)
			}
			qs[39].ID = "q20"
			return qs
		}()...), discern.ErrInvalidRequest},
		{"empty option ID", withQuestions(discern.Choice(qQueue, instr, discern.Opt("", discern.Value{}), discern.Opt("a", discern.Value{}))), discern.ErrInvalidRequest},
		{"control character in option ID", withQuestions(discern.Choice(qQueue, instr, discern.Opt("a\x00", discern.Value{}), discern.Opt("b", discern.Value{}))), discern.ErrInvalidRequest},
		{"option ID too long", withQuestions(discern.Choice(qQueue, instr, discern.Opt(strings.Repeat("a", 257), discern.Value{}), discern.Opt("b", discern.Value{}))), discern.ErrInvalidRequest},
		{"scalar option description", withQuestions(discern.Choice(qQueue, instr, discern.Opt("a", discern.JSON([]byte(`true`))), discern.Opt("b", discern.Value{}))), discern.ErrInvalidRequest},
		{"choice with levels", withQuestions(discern.Question{ID: "q", Kind: discern.KindChoice, Instructions: instr, Options: options(2), Levels: levels(2)}), discern.ErrInvalidRequest},
		{"one level", withQuestions(discern.Score(qSeverity, instr, levels(1)...)), discern.ErrInvalidRequest},
		{"eleven levels", withQuestions(discern.Score(qSeverity, instr, levels(11)...)), discern.ErrInvalidRequest},
		{"missing level description", withQuestions(discern.Score(qSeverity, instr, discern.Level("a", discern.Value{}), discern.Level("b", discern.Text("b")))), discern.ErrInvalidRequest},
		{"null level description", withQuestions(discern.Score(qSeverity, instr, discern.Level("a", discern.JSON([]byte(`null`))), discern.Level("b", discern.Text("b")))), discern.ErrInvalidRequest},
		{"duplicate level", withQuestions(discern.Score(qSeverity, instr, discern.Level("a", discern.Text("a")), discern.Level("a", discern.Text("b")))), discern.ErrInvalidRequest},
		{"score with options", withQuestions(discern.Question{ID: "q", Kind: discern.KindScore, Instructions: instr, Options: options(2), Levels: levels(2)}), discern.ErrInvalidRequest},
		{"noul with options", withQuestions(discern.Question{ID: "q", Kind: discern.KindNoul, Instructions: instr, Options: options(2)}), discern.ErrInvalidRequest},
		{"noul with numeric yes", withQuestions(noulWith(discern.JSON([]byte(`1`)))), discern.ErrInvalidRequest},
		{"noul with null yes", withQuestions(noulWith(discern.JSON([]byte(`null`)))), discern.ErrInvalidRequest},
		{"correlation ID too long", modify(func(r *discern.Request) { r.CorrelationID = strings.Repeat("c", 257) }), discern.ErrInvalidRequest},
		{"correlation ID control character", modify(func(r *discern.Request) { r.CorrelationID = "ticket\n1" }), discern.ErrInvalidRequest},
		{"too many evidence refs", modify(func(r *discern.Request) { r.EvidenceRefs = make([]string, 33) }), discern.ErrInvalidRequest},
		{"empty evidence ref", modify(func(r *discern.Request) { r.EvidenceRefs = []string{""} }), discern.ErrInvalidRequest},
		{"evidence ref too long", modify(func(r *discern.Request) { r.EvidenceRefs = []string{strings.Repeat("r", 257)} }), discern.ErrInvalidRequest},
		{"evidence ref control character", modify(func(r *discern.Request) { r.EvidenceRefs = []string{"kb:\x7f"} }), discern.ErrInvalidRequest},
		{"correlation ID invalid UTF-8", modify(func(r *discern.Request) { r.CorrelationID = "ticket-\xff" }), discern.ErrInvalidRequest},
		{"zero-width space in correlation ID", modify(func(r *discern.Request) { r.CorrelationID = "ticket\u200b1042" }), discern.ErrInvalidRequest},
		{"byte order mark in evidence ref", modify(func(r *discern.Request) { r.EvidenceRefs = []string{"kb:\ufeffarticle-17"} }), discern.ErrInvalidRequest},
		{"right-to-left override in option ID", withQuestions(discern.Choice(qQueue, instr, discern.Opt("billing\u202e", discern.Value{}), discern.Opt("b", discern.Value{}))), discern.ErrInvalidRequest},
		{"word joiner in level ID", withQuestions(discern.Score(qSeverity, instr, discern.Level("low\u2060", discern.Text("a")), discern.Level("b", discern.Text("b")))), discern.ErrInvalidRequest},
		{"empty kind", withQuestions(discern.Question{ID: "q", Instructions: instr}), discern.ErrInvalidRequest},
		{"invalid JSON instructions", withQuestions(discern.Noul(qOutage, discern.JSON([]byte(`{"a":`)))), discern.ErrInvalidRequest},
		{"empty array instructions", withQuestions(discern.Noul(qOutage, discern.JSON([]byte(`[ ]`)))), discern.ErrInvalidRequest},
		{"escaped blank JSON string instructions", withQuestions(discern.Noul(qOutage, discern.JSON([]byte(`" \t\u00a0"`)))), discern.ErrInvalidRequest},
		{"invalid JSON option description", withQuestions(discern.Choice(qQueue, instr, discern.Opt("a", discern.JSON([]byte(`{`))), discern.Opt("b", discern.Value{}))), discern.ErrInvalidRequest},
		{"scalar level description", withQuestions(discern.Score(qSeverity, instr, discern.Level("a", discern.JSON([]byte(`3`))), discern.Level("b", discern.Text("b")))), discern.ErrInvalidRequest},
		{"choice with yes", withQuestions(func() discern.Question {
			q := discern.Choice(qQueue, instr, options(2)...)
			q.Yes = discern.Text("yes")
			return q
		}()), discern.ErrInvalidRequest},
		{"score with no", withQuestions(func() discern.Question {
			q := discern.Score(qSeverity, instr, levels(2)...)
			q.No = discern.Text("no")
			return q
		}()), discern.ErrInvalidRequest},
		{"noul with levels", withQuestions(discern.Question{ID: "q", Kind: discern.KindNoul, Instructions: instr, Levels: levels(2)}), discern.ErrInvalidRequest},
		{"noul with invalid no", withQuestions(func() discern.Question {
			q := discern.Noul(qOutage, instr)
			q.No = discern.JSON([]byte(`[1,`))
			return q
		}()), discern.ErrInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jev, clef := staticPair()
			ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))
			res, err := ev.Evaluate(t.Context(), tt.req)
			require.ErrorIs(t, err, tt.want)
			assert.Nil(t, res)
			require.ErrorAs(t, err, new(*discern.ValidationError))
			assert.NotContains(t, err.Error(), stateSentinel)
			assert.Equal(t, 0, jev.Calls()+clef.Calls())
		})
	}
}

// TestEvaluateLocalFailuresMakeNoCalls covers the failures found after the
// request validates and before any call: an adapter's Prepare error, a
// provider no adapter serves, an already canceled context and an adapter that
// prepares no call. None of them reaches either provider.
func TestEvaluateLocalFailuresMakeNoCalls(t *testing.T) {
	failingPrepare := func(err error) func(*discerntest.Fake) discern.Adapter {
		return func(clef *discerntest.Fake) discern.Adapter {
			return &countingPrepare{provider: discern.Clef, err: err, inner: clef}
		}
	}
	tests := []struct {
		name     string
		clef     func(inner *discerntest.Fake) discern.Adapter // the Clef adapter registered; nil registers none
		canceled bool
		want     error
	}{
		{"unsupported", failingPrepare(&discern.ValidationError{Err: discern.ErrUnsupported, Field: "questions", Reason: "too many"}), false, discern.ErrUnsupported},
		{"invalid request", failingPrepare(&discern.ValidationError{Err: discern.ErrInvalidRequest, Field: "state", Reason: "too large"}), false, discern.ErrInvalidRequest},
		{"other error is wrapped as unsupported", failingPrepare(errFakePrepare), false, discern.ErrUnsupported},
		{"unregistered provider is unsupported", nil, false, discern.ErrUnsupported},
		{"already canceled", func(clef *discerntest.Fake) discern.Adapter { return clef }, true, context.Canceled},
		{"prepared nil call is unsupported", func(*discerntest.Fake) discern.Adapter { return &nilCallAdapter{provider: discern.Clef} }, false, discern.ErrUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jev, clef := staticPair()
			opts := []discern.Option{discern.WithAdapter(jev)}
			if tt.clef != nil {
				opts = append(opts, discern.WithAdapter(tt.clef(clef)))
			}
			ev := testutil.Evaluator(t, opts...)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.canceled {
				cancel()
			}
			res, err := ev.Evaluate(ctx, ticketRequest(discern.JevAndClef))
			require.ErrorIs(t, err, tt.want)
			assert.Nil(t, res)
			assert.Equal(t, 0, jev.Calls()+clef.Calls())
		})
	}
}

func TestEvaluatePrepareErrorTextIsFixed(t *testing.T) {
	// A third-party adapter's message may hold caller data, so it never reaches
	// Error; the adapter's error stays reachable for errors.Is and errors.As.
	leaky := fmt.Errorf("cannot send %q: %w", stateSentinel, errFakePrepare)
	jev, clef := staticPair()
	failing := &countingPrepare{provider: discern.Clef, err: leaky, inner: clef}
	ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(failing))

	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevAndClef))
	assert.Nil(t, res)
	require.ErrorIs(t, err, discern.ErrUnsupported)
	require.ErrorIs(t, err, errFakePrepare)
	assert.Equal(t, "discern: unsupported by provider: the clef adapter rejected the request", err.Error())
	assert.NotContains(t, fmt.Sprintf("%v %+v %s", err, err, err), stateSentinel)

	items, err := ev.EvaluateBatch(t.Context(), []discern.Request{ticketRequest(discern.JevAndClef)})
	assert.Nil(t, items)
	require.ErrorIs(t, err, errFakePrepare)
	assert.Equal(t, "discern: batch item 0: discern: unsupported by provider: the clef adapter rejected the request", err.Error())
	assert.Equal(t, 0, jev.Calls()+clef.Calls())
}

func TestEvaluatePrepareSentinelErrorsAreReturnedUnchanged(t *testing.T) {
	for _, sentinel := range []error{discern.ErrInvalidRequest, discern.ErrUnsupported} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			adapterErr := &discern.ValidationError{Err: sentinel, Field: "questions", Reason: "too many"}
			jev, _ := staticPair()
			ev := testutil.Evaluator(t, discern.WithAdapter(&countingPrepare{provider: discern.Jev, err: adapterErr, inner: jev}))
			_, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly))
			assert.Same(t, adapterErr, err)
		})
	}
}

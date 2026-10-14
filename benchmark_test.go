package discern_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
)

// These benchmarks measure local CPU work only (validation, copying, policy and
// encoding) with fake adapters; they say nothing about network latency.

func benchPolicy() discern.Policy {
	return discern.Policy{Version: "routing-v3", Questions: []discern.QuestionPolicy{
		{Question: qQueue, Checks: []discern.Check{{Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.8}}},
		{Question: qSeverity, Checks: []discern.Check{{Metric: discern.MetricScore, Op: discern.AtMost, Threshold: 1.5}}, Agreement: &discern.Agreement{MaxDifference: 0.25}},
	}}
}

func BenchmarkEvaluateSingle(b *testing.B) {
	ev := pairEvaluator(b)
	req := ticketRequest(discern.JevOnly)
	ctx := b.Context()
	for b.Loop() {
		if _, err := ev.Evaluate(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEvaluateDualWithPolicy(b *testing.B) {
	ev := pairEvaluator(b, discern.WithDefaultPolicy(benchPolicy()))
	req := ticketRequest(discern.JevAndClef)
	ctx := b.Context()
	for b.Loop() {
		if _, err := ev.Evaluate(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEvaluateBatch(b *testing.B) {
	ev := pairEvaluator(b, discern.WithBatchConcurrency(8))
	reqs := make([]discern.Request, 32)
	for i := range reqs {
		reqs[i] = ticketRequest(discern.JevAndClef)
	}
	ctx := b.Context()
	for b.Loop() {
		if _, err := ev.EvaluateBatch(ctx, reqs); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRecommend(b *testing.B) {
	res := mustEvaluate(b, pairEvaluator(b), discern.JevAndClef)
	pol := benchPolicy()
	for b.Loop() {
		if _, err := discern.Recommend(res, pol); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkResultMarshal(b *testing.B) {
	res := mustEvaluate(b, pairEvaluator(b, discern.WithDefaultPolicy(benchPolicy())), discern.JevAndClef)
	for b.Loop() {
		if _, err := json.Marshal(res); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseResult(b *testing.B) {
	res := mustEvaluate(b, pairEvaluator(b, discern.WithDefaultPolicy(benchPolicy())), discern.JevAndClef)
	data, err := json.Marshal(res)
	require.NoError(b, err)
	for b.Loop() {
		if _, err := discern.ParseResult(data); err != nil {
			b.Fatal(err)
		}
	}
}

// largeJSONState returns a synthetic ticket thread of about 2 MB.
func largeJSONState() []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"messages":[`)
	for i := range 20000 {
		if i > 0 {
			buf.WriteByte(',')
		}
		fmt.Fprintf(&buf, `{"id":%d,"text":"I was charged twice for invoice %d, please refund the duplicate payment."}`, i, i)
	}
	buf.WriteString(`]}`)
	return buf.Bytes()
}

// BenchmarkJSONValue measures building a Value, which copies and validates the
// bytes once.
func BenchmarkJSONValue(b *testing.B) {
	raw := largeJSONState()
	b.SetBytes(int64(len(raw)))
	for b.Loop() {
		_ = discern.JSON(raw)
	}
}

// BenchmarkEvaluateLargeJSONState shows that evaluating a request never rescans
// or copies its state.
func BenchmarkEvaluateLargeJSONState(b *testing.B) {
	ev := pairEvaluator(b)
	req := ticketRequest(discern.JevOnly)
	req.State = discern.JSON(largeJSONState())
	ctx := b.Context()
	for b.Loop() {
		if _, err := ev.Evaluate(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

// largeBench is a dual evaluation of largeRequest(64, 255) with a check on every
// question.
type largeBench struct {
	ev  *discern.Evaluator
	req discern.Request
	res *discern.Result
	pol discern.Policy
}

func benchLarge(b *testing.B) largeBench {
	b.Helper()
	req, obs := largeRequest(64, discern.MaxChoiceOptions)
	req.Providers = discern.JevAndClef
	ev := testutil.Evaluator(b, discern.WithAdapter(discerntest.Static(discern.Jev, obs)), discern.WithAdapter(discerntest.Static(discern.Clef, obs)))
	pol := largePolicy(64)
	res, err := ev.Evaluate(b.Context(), req, discern.WithPolicy(pol))
	require.NoError(b, err)
	return largeBench{ev: ev, req: req, res: res, pol: pol}
}

// BenchmarkEvaluateLarge evaluates 64 questions of up to 255 options on both
// providers.
func BenchmarkEvaluateLarge(b *testing.B) {
	lb := benchLarge(b)
	ctx := b.Context()
	for b.Loop() {
		if _, err := lb.ev.Evaluate(ctx, lb.req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRecommendLarge(b *testing.B) {
	lb := benchLarge(b)
	for b.Loop() {
		if _, err := discern.Recommend(lb.res, lb.pol); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseResultLarge(b *testing.B) {
	data, err := json.Marshal(benchLarge(b).res)
	require.NoError(b, err)
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		if _, err := discern.ParseResult(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkClone(b *testing.B) {
	res := mustEvaluate(b, pairEvaluator(b, discern.WithDefaultPolicy(benchPolicy())), discern.JevAndClef)
	for b.Loop() {
		_ = res.Clone()
	}
}

func BenchmarkCloneLarge(b *testing.B) {
	res := benchLarge(b).res
	for b.Loop() {
		_ = res.Clone()
	}
}

func BenchmarkRedact(b *testing.B) {
	res := mustEvaluate(b, pairEvaluator(b, discern.WithDefaultPolicy(benchPolicy())), discern.JevAndClef)
	all := discern.Redaction{CorrelationID: true, EvidenceRefs: true, RequestIDs: true, Usage: true}
	for b.Loop() {
		_ = res.Redact(all)
	}
}

// BenchmarkEvaluateBatchLarge evaluates 1000 dual requests, 8 at a time.
func BenchmarkEvaluateBatchLarge(b *testing.B) {
	ev := pairEvaluator(b, discern.WithBatchConcurrency(8), discern.WithDefaultPolicy(benchPolicy()))
	reqs := make([]discern.Request, 1000)
	for i := range reqs {
		reqs[i] = ticketRequest(discern.JevAndClef)
		reqs[i].CorrelationID = fmt.Sprintf("ticket-%d", i)
	}
	ctx := b.Context()
	for b.Loop() {
		if _, err := ev.EvaluateBatch(ctx, reqs); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkValueText measures building a large text Value, which validates the
// UTF-8 once and copies nothing, and encoding it into a reused buffer.
func BenchmarkValueText(b *testing.B) {
	text := strings.Repeat("Subject: Charged twice\n\nMy invoice was paid twice this month; café, naïve. ", 1<<14)
	buf := make([]byte, 0, len(text)+len(text)/8)
	b.SetBytes(int64(len(text)))
	for b.Loop() {
		v := discern.Text(text)
		out, err := v.AppendJSON(buf[:0])
		if err != nil {
			b.Fatal(err)
		}
		if len(out) < len(text) {
			b.Fatalf("encoded %d bytes, want at least the %d of the text", len(out), len(text))
		}
	}
}

// BenchmarkJSONStringValue measures building a Value from a large JSON string,
// which is validated and copied but never decoded.
func BenchmarkJSONStringValue(b *testing.B) {
	raw, err := json.Marshal(strings.Repeat("I was charged twice for invoice 1042. ", 1<<15))
	require.NoError(b, err)
	b.SetBytes(int64(len(raw)))
	for b.Loop() {
		_ = discern.JSON(raw)
	}
}

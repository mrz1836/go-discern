package discern_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
	"github.com/mrz1836/go-discern/jev"
)

// These tests run the real jev and clef adapters end to end against one in-memory
// transport, which answers by host and counts every attempt.

const (
	jevHost       = "api.typesafe.ai"
	clefHost      = "api.cloudflare.com"
	testAccountID = "0123456789abcdef0123456789abcdef"
	jevKey        = "jev-key-SENTINEL"
	clefAuthValue = "clef-token-SENTINEL"
)

const jevTicketBody = `{"model":"jev-1.13.0","answers":{
"queue":{"type":"choice","choice":"billing","probabilities":{"technical":0.06,"billing":0.91,"account":0.03},"confidence":0.87},
"outage":{"type":"noul","noul":0.07},
"severity":{"type":"score","score":1.05,"legend":{"0":"Cosmetic","1":"Degraded","2":"Blocked"},"probabilities":{"0":0.0,"1":0.95,"2":0.05},"confidence":0.92}},
"usage":{"input_tokens":304,"output_tokens":18}}`

const clefTicketBody = `{"result":{"model":"clef-flash","answers":{
"queue":{"type":"choice","choice":"billing","probabilities":{"billing":0.8712,"technical":0.0988,"account":0.03},"confidence":0.8061},
"outage":{"type":"noul","noul":0.1},
"severity":{"type":"score","score":1.1,"legend":{"0":{"what":"Cosmetic"},"1":{"what":"Degraded"},"2":{"what":"Blocked"}},"probabilities":{"0":0.0,"1":0.9,"2":0.1},"confidence":0.85}},
"usage":{"input_tokens":290,"output_tokens":0}},"success":true,"errors":[],"messages":[]}`

// reply is one canned response.
type reply struct {
	status int
	body   string
	header map[string]string
	block  bool // wait for the request context instead of answering
}

// providerTransport answers requests by host and records every attempt.
type providerTransport struct {
	mu      sync.Mutex
	replies map[string]reply
	hits    map[string]int
	paths   map[string]string
	bodies  map[string]string
	auth    map[string]string
}

func newProviderTransport(replies map[string]reply) *providerTransport {
	return &providerTransport{
		replies: replies, hits: map[string]int{}, paths: map[string]string{},
		bodies: map[string]string{}, auth: map[string]string{},
	}
}

func (p *providerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	host := req.URL.Host
	p.mu.Lock()
	p.hits[host]++
	p.paths[host] = req.URL.Path
	p.bodies[host] = string(body)
	p.auth[host] = req.Header.Get("Authorization")
	r, ok := p.replies[host]
	p.mu.Unlock()
	if !ok {
		r = reply{status: http.StatusNotFound, body: `{}`}
	}
	if r.block {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	h := http.Header{"Content-Type": []string{"application/json"}}
	for k, v := range r.header {
		h.Set(k, v)
	}
	return &http.Response{
		StatusCode: r.status, Header: h, Request: req,
		Body: io.NopCloser(strings.NewReader(r.body)), ContentLength: int64(len(r.body)),
	}, nil
}

func (p *providerTransport) count(host string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hits[host]
}

func (p *providerTransport) total() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, v := range p.hits {
		n += v
	}
	return n
}

func okReplies() map[string]reply {
	return map[string]reply{
		jevHost:  {status: http.StatusOK, body: jevTicketBody, header: map[string]string{"x-typesafe-request-id": "jev-req-1"}},
		clefHost: {status: http.StatusOK, body: clefTicketBody, header: map[string]string{"cf-ai-req-id": "clef-req-1", "cf-ray": "8f00aa-SJC", "cf-ai-neurons": "1.19"}},
	}
}

// realEvaluator wires both real adapters to the transport.
func realEvaluator(t *testing.T, tr http.RoundTripper, opts ...discern.Option) *discern.Evaluator {
	t.Helper()
	hc := &http.Client{Transport: tr}
	jc, err := jev.New(jevKey, jev.WithModel("jev-1.13.0"), jev.WithHTTPClient(hc), jev.WithTimeout(5*time.Second))
	require.NoError(t, err)
	cc, err := clef.New(testAccountID, clefAuthValue, clef.WithModel(clef.ModelFlash), clef.WithHTTPClient(hc), clef.WithTimeout(2*time.Second))
	require.NoError(t, err)
	return testutil.Evaluator(t, append([]discern.Option{discern.WithAdapter(jc), discern.WithAdapter(cc)}, opts...)...)
}

func routingPolicy() discern.Policy {
	return discern.Policy{Version: "routing-v3", Questions: []discern.QuestionPolicy{{
		Question: qQueue,
		Checks: []discern.Check{
			{ID: "queue-jev", Provider: discern.Jev, Model: "jev-1.13.0", Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.80},
			{ID: "queue-clef", Provider: discern.Clef, Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.75},
		},
	}, {
		Question: qSeverity, Checks: []discern.Check{{Metric: discern.MetricScore, Op: discern.AtMost, Threshold: 1.5}},
		Agreement: &discern.Agreement{MaxDifference: 0.25},
	}}}
}

func TestIntegrationDualRunThroughRealAdapters(t *testing.T) {
	tr := newProviderTransport(okReplies())
	ev := realEvaluator(t, tr, discern.WithDefaultPolicy(routingPolicy()))

	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevAndClef))
	require.NoError(t, err)
	assert.Equal(t, discern.Complete, res.Status)
	assert.Equal(t, 1, tr.count(jevHost))
	assert.Equal(t, 1, tr.count(clefHost))

	assert.Equal(t, "/v1/systemone", tr.paths[jevHost])
	assert.Equal(t, "/client/v4/accounts/"+testAccountID+"/ai/run/@cf/cloudflare/clef-flash", tr.paths[clefHost])
	assert.Equal(t, "Bearer "+jevKey, tr.auth[jevHost])
	assert.Equal(t, "Bearer "+clefAuthValue, tr.auth[clefHost])
	assert.True(t, strings.HasPrefix(tr.bodies[clefHost], `{"model":"clef-flash","state":`))
	assert.True(t, strings.HasPrefix(tr.bodies[jevHost], `{"model":"jev-1.13.0","state":`))
	assert.Less(t, strings.Index(tr.bodies[jevHost], `"billing"`), strings.Index(tr.bodies[jevHost], `"technical"`), "option order survives on the wire")

	jo, ok := res.Observation(discern.Jev)
	require.True(t, ok)
	assert.Equal(t, "jev-req-1", jo.RequestID)
	require.NotNil(t, jo.Usage)
	require.NotNil(t, jo.Usage.OutputTokens)
	assert.Equal(t, int64(18), *jo.Usage.OutputTokens)
	co, ok := res.Observation(discern.Clef)
	require.True(t, ok)
	assert.Equal(t, "clef-flash", co.Model)
	assert.Equal(t, "clef-req-1", co.RequestID)
	assert.Equal(t, "8f00aa-SJC", co.RayID)
	require.NotNil(t, co.Usage)
	require.NotNil(t, co.Usage.Neurons)
	assert.InDelta(t, 1.19, *co.Usage.Neurons, 0)
	q, _ := co.Choice(qQueue)
	assert.Equal(t, []discern.OptionProbability{{Option: optBilling, Probability: 0.8712}, {Option: "technical", Probability: 0.0988}, {Option: "account", Probability: 0.03}}, q.Probabilities)

	queue := recommendation(t, res, qQueue)
	assert.Equal(t, discern.Accepted, queue.Status)
	assert.Equal(t, &discern.Selected{Providers: []discern.Provider{discern.Jev, discern.Clef}, Option: optBilling}, queue.Selected)
	assert.Equal(t, discern.Accepted, recommendation(t, res, qSeverity).Status)
	assert.Equal(t, discern.Overall{Status: discern.Accepted, Uncovered: []string{qOutage}}, res.Overall)

	data, err := json.Marshal(res)
	require.NoError(t, err)
	for _, secret := range []string{jevKey, clefAuthValue, testAccountID, stateSentinel, instructionSentinel, descriptionSentinel, "Cosmetic"} {
		assert.NotContains(t, string(data), secret)
	}
	restored, err := discern.ParseResult(data)
	require.NoError(t, err)
	assert.Equal(t, res, restored)
}

func TestIntegrationFailuresMakeOneAttempt(t *testing.T) {
	tests := []struct {
		name       string
		jev, clef  reply
		status     discern.ResultStatus
		jevKind    discern.FailureKind
		clefKind   discern.FailureKind
		retryAfter time.Duration
	}{
		{
			name: "jev rate limited", jev: reply{status: http.StatusTooManyRequests, body: `{"error":"slow down"}`, header: map[string]string{"Retry-After": "2"}},
			clef: okReplies()[clefHost], status: discern.Partial, jevKind: discern.FailureRejected, retryAfter: 2 * time.Second,
		},
		{
			name: "clef overloaded", jev: okReplies()[jevHost],
			clef:   reply{status: http.StatusServiceUnavailable, body: `{"success":false,"errors":[{"code":3040,"message":"Capacity temporarily exceeded"}]}`},
			status: discern.Partial, clefKind: discern.FailureRejected,
		},
		{
			name: "both fail", jev: reply{status: 529, body: `overloaded`}, clef: reply{status: http.StatusBadGateway, body: `<html>`},
			status: discern.Failed, jevKind: discern.FailureRejected, clefKind: discern.FailureRejected,
		},
		{
			name: "clef answers success false on 200", jev: okReplies()[jevHost],
			clef:   reply{status: http.StatusOK, body: `{"result":null,"success":false,"errors":[{"code":10000,"message":"Authentication error"}],"messages":[]}`},
			status: discern.Partial, clefKind: discern.FailureRejected,
		},
		{
			name: "jev malformed", jev: reply{status: http.StatusOK, body: `{"model":"jev-1.13.0","answers":{"queue":`},
			clef: okReplies()[clefHost], status: discern.Partial, jevKind: discern.FailureMalformed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := newProviderTransport(map[string]reply{jevHost: tt.jev, clefHost: tt.clef})
			ev := realEvaluator(t, tr)
			res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevAndClef))
			require.Error(t, err)
			require.NotNil(t, res)
			assert.Equal(t, tt.status, res.Status)
			assert.Equal(t, 1, tr.count(jevHost), "exactly one Jev attempt")
			assert.Equal(t, 1, tr.count(clefHost), "exactly one Clef attempt")
			if tt.jevKind != "" {
				require.NotNil(t, res.Outcomes[0].Failure)
				assert.Equal(t, tt.jevKind, res.Outcomes[0].Failure.Kind)
			}
			if tt.clefKind != "" {
				require.NotNil(t, res.Outcomes[1].Failure)
				assert.Equal(t, tt.clefKind, res.Outcomes[1].Failure.Kind)
			}
			if tt.retryAfter > 0 {
				require.NotNil(t, res.Outcomes[0].Failure.RetryAfter)
				assert.Equal(t, tt.retryAfter, *res.Outcomes[0].Failure.RetryAfter)
			}
			for _, secret := range []string{jevKey, clefAuthValue, testAccountID, stateSentinel} {
				assert.NotContains(t, err.Error(), secret)
				assert.NotContains(t, fmt.Sprintf("%+v %#v %t", err, err, err), secret)
			}
		})
	}
}

func TestIntegrationOneProviderTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		replies := okReplies()
		replies[clefHost] = reply{block: true}
		tr := newProviderTransport(replies)
		ev := realEvaluator(t, tr)
		res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevAndClef))
		require.ErrorIs(t, err, discern.ErrPartialFailure)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Equal(t, discern.Partial, res.Status)
		require.Len(t, res.Outcomes, 2)
		assert.Equal(t, discern.OutcomeSucceeded, res.Outcomes[0].Status)
		require.NotNil(t, res.Outcomes[1].Failure)
		assert.Equal(t, discern.FailureTimeout, res.Outcomes[1].Failure.Kind)
		assert.Equal(t, 2*time.Second, res.Outcomes[1].Elapsed)
		assert.Equal(t, 1, tr.count(clefHost))
	})
}

func TestIntegrationLocalErrorsSendNothing(t *testing.T) {
	many := ticketRequest(discern.JevAndClef)
	for i := len(many.Questions); i < 65; i++ {
		many.Questions = append(many.Questions, discern.Noul(fmt.Sprintf("extra%d", i), discern.Text("Is this a duplicate?")))
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	tests := []struct {
		name string
		ctx  context.Context //nolint:containedctx // table field for the call under test
		req  discern.Request
		opts []discern.EvalOption
		want error
	}{
		{"clef question limit", t.Context(), many, nil, discern.ErrUnsupported},
		{"invalid policy", t.Context(), ticketRequest(discern.JevAndClef), []discern.EvalOption{discern.WithPolicy(discern.Policy{})}, discern.ErrInvalidPolicy},
		{"invalid request", t.Context(), ticketRequest(""), nil, discern.ErrInvalidRequest},
		{"canceled context", canceled, ticketRequest(discern.JevAndClef), nil, context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := newProviderTransport(okReplies())
			ev := realEvaluator(t, tr)
			res, err := ev.Evaluate(tt.ctx, tt.req, tt.opts...)
			require.ErrorIs(t, err, tt.want)
			assert.Nil(t, res)
			assert.Equal(t, 0, tr.total())
		})
	}
}

func TestIntegrationBatchAndConcurrency(t *testing.T) {
	tr := newProviderTransport(okReplies())
	ev := realEvaluator(t, tr, discern.WithBatchConcurrency(3))
	reqs := make([]discern.Request, 6)
	for i := range reqs {
		reqs[i] = ticketRequest(discern.JevAndClef)
		reqs[i].CorrelationID = fmt.Sprintf("ticket-%d", i)
	}
	items, err := ev.EvaluateBatch(t.Context(), reqs)
	require.NoError(t, err)
	for i, it := range items {
		require.NoError(t, it.Err)
		assert.Equal(t, fmt.Sprintf("ticket-%d", i), it.Result.CorrelationID)
	}
	assert.Equal(t, 6, tr.count(jevHost))
	assert.Equal(t, 6, tr.count(clefHost))

	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := range errs {
		wg.Go(func() {
			sets := []discern.ProviderSet{discern.JevOnly, discern.ClefOnly, discern.JevAndClef, discern.JevAndClef}
			_, errs[i] = ev.Evaluate(t.Context(), ticketRequest(sets[i%4]))
		})
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, 6+15, tr.count(jevHost))
	assert.Equal(t, 6+15, tr.count(clefHost))
}

func TestIntegrationFakeAndRealAdapterMix(t *testing.T) {
	tr := newProviderTransport(okReplies())
	cc, err := clef.New(testAccountID, clefAuthValue, clef.WithHTTPClient(&http.Client{Transport: tr}))
	require.NoError(t, err)
	fake := discerntest.Static(discern.Jev, jevAnswers().observation())
	ev := testutil.Evaluator(t, discern.WithAdapter(fake), discern.WithAdapter(cc))
	res, err := ev.Evaluate(t.Context(), ticketRequest(discern.JevAndClef))
	require.NoError(t, err)
	assert.Equal(t, discern.Complete, res.Status)
	assert.Equal(t, 1, fake.Calls())
	assert.Equal(t, 1, tr.count(clefHost))
	require.Len(t, res.Outcomes, 2)
	assert.Equal(t, "clef", res.Outcomes[1].RequestedModel)
	assert.Equal(t, "clef-flash", res.Outcomes[1].Observation.Model)
}

// nilPointerAdapter has pointer receivers that read a field, so a nil
// *nilPointerAdapter panics like a nil *discerntest.Fake does.
type nilPointerAdapter struct{ provider discern.Provider }

func (n *nilPointerAdapter) Provider() discern.Provider { return n.provider }

func (n *nilPointerAdapter) Model() string { return string(n.provider) }

func (n *nilPointerAdapter) Prepare(*discern.Request) (discern.Call, error) {
	return nil, errFakePrepare
}

// panickingPrepare is an adapter whose Prepare panics.
type panickingPrepare struct{}

func (panickingPrepare) Provider() discern.Provider { return discern.Jev }

func (panickingPrepare) Model() string { return "jev-1.13.0" }

func (panickingPrepare) Prepare(*discern.Request) (discern.Call, error) {
	panic("prepare adapter bug")
}

func TestNewRejectsTypedNilAdapters(t *testing.T) {
	jc, _ := staticPair()
	tests := []struct {
		name    string
		adapter discern.Adapter
	}{
		{"nil *jev.Client", (*jev.Client)(nil)},
		{"nil *clef.Client", (*clef.Client)(nil)},
		{"nil *discerntest.Fake", (*discerntest.Fake)(nil)},
		{"nil pointer of a custom adapter", (*nilPointerAdapter)(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				ev  *discern.Evaluator
				err error
			)
			require.NotPanics(t, func() { ev, err = discern.New(discern.WithAdapter(jc), discern.WithAdapter(tt.adapter)) })
			require.ErrorIs(t, err, discern.ErrInvalidConfig)
			assert.Nil(t, ev)
			ve := testutil.ValidationError(t, err)
			assert.Equal(t, "adapters[1]", ve.Field)
			assert.Equal(t, "is a nil adapter", ve.Reason)
		})
	}

	// A non-nil adapter whose Prepare panics is an adapter bug: the panic still
	// reaches the caller, and nothing is sent.
	ev := testutil.Evaluator(t, discern.WithAdapter(panickingPrepare{}))
	assert.PanicsWithValue(t, "prepare adapter bug", func() {
		_, _ = ev.Evaluate(t.Context(), ticketRequest(discern.JevOnly))
	})
}

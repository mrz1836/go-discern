package clef_test

import (
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
	"github.com/mrz1836/go-discern/internal/testutil"
)

// ticketWireAfterModel is the exact body of ticketRequest after the model member.
const ticketWireAfterModel = `,"state":"Subject: Charged twice\n\nMy invoice was paid twice this month.",` +
	`"questions":{` +
	`"queue":{"type":"choice","instructions":"Which support queue should handle this ticket?",` +
	`"criteria":{"technical":"Errors, outages, integration problems","billing":"Invoices, refunds, failed payments","account":"Login, profile, permissions"}},` +
	`"outage":{"type":"noul","instructions":"Does the customer report a production outage?",` +
	`"criteria":{"true":"Production is down for the customer.","false":"Production works; the issue is elsewhere."}},` +
	`"severity":{"type":"score","instructions":"How severe is the customer impact?",` +
	`"criteria":["Cosmetic; work continues","Degraded; workaround exists","Blocked; no workaround"]}}}`

// assertBody compares wire bytes exactly: key order, whitespace and escaping are
// part of the contract, so a semantic JSON comparison would hide regressions.
func assertBody(t *testing.T, want string, got []byte) {
	t.Helper()
	assert.Equal(t, want, string(got))
}

func TestCallSendsExactWire(t *testing.T) {
	tests := []struct {
		name  string
		opts  []clef.Option
		model string
	}{
		{name: "default model", model: clef.ModelClef},
		{name: "clef", opts: []clef.Option{clef.WithModel(clef.ModelClef)}, model: clef.ModelClef},
		{name: "clef-flash", opts: []clef.Option{clef.WithModel(clef.ModelFlash)}, model: clef.ModelFlash},
		{name: "clef-omni", opts: []clef.Option{clef.WithModel(clef.ModelOmni)}, model: clef.ModelOmni},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newServer(t, okReply(envelope(ticketResult(tt.model))))
			res, err := testutil.Evaluate(t, serverClient(t, srv, tt.opts...), ticketRequest())
			require.NoError(t, err)
			assert.Equal(t, discern.Complete, res.Status)
			require.Len(t, res.Outcomes, 1)
			assert.Equal(t, tt.model, res.Outcomes[0].RequestedModel)

			reqs := srv.Requests()
			require.Len(t, reqs, 1)
			r := reqs[0]
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/client/v4/accounts/"+testAccount+"/ai/run/@cf/cloudflare/"+tt.model, r.URL)
			assert.Equal(t, []string{"Bearer " + testToken}, r.Header.Values("Authorization"))
			assert.Equal(t, []string{"application/json"}, r.Header.Values("Content-Type"))
			assert.Equal(t, []string{"application/json"}, r.Header.Values("Accept"))
			assert.Equal(t, []string{"go-discern"}, r.Header.Values("User-Agent"))
			assert.Empty(t, r.Header.Values("Idempotency-Key"))
			assert.Empty(t, r.Header.Values("X-Idempotency-Key"))
			wantBody := `{"model":"` + tt.model + `"` + ticketWireAfterModel
			assertBody(t, wantBody, r.Body)
		})
	}
}

func TestCallLowerCasesTheAccountID(t *testing.T) {
	srv := newServer(t, okReply(envelope(outageResult)))
	c, err := clef.New(strings.ToUpper(testAccount), testToken, clef.WithTestEndpoint(srv.URL()), clef.WithHTTPClient(srv.Client()))
	require.NoError(t, err)
	_, err = testutil.Evaluate(t, c, noulRequest())
	require.NoError(t, err)
	reqs := srv.Requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "/client/v4/accounts/"+testAccount+"/ai/run/@cf/cloudflare/clef", reqs[0].URL)
}

func TestCallDropsInvalidRequestAndRayIDs(t *testing.T) {
	tests := []struct {
		name      string
		requestID string
		rayID     string
		wantID    string
		wantRay   string
	}{
		{name: "valid", requestID: testRequestID, rayID: testRayID, wantID: testRequestID, wantRay: testRayID},
		{name: "spaces", requestID: "req 1", rayID: "8c1f SJC"},
		{name: "too long", requestID: strings.Repeat("r", 257), rayID: strings.Repeat("r", 257)},
		{name: "non-ASCII", requestID: "réq", rayID: "räy"},
		{name: "longest", requestID: strings.Repeat("r", 256), rayID: strings.Repeat("y", 256), wantID: strings.Repeat("r", 256), wantRay: strings.Repeat("y", 256)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct := okTransport(envelope(outageResult))
			ct.header.Set("Cf-Ai-Req-Id", tt.requestID)
			ct.header.Set("Cf-Ray", tt.rayID)
			req := noulRequest()
			call, err := ct.client(t).Prepare(&req)
			require.NoError(t, err)
			obs, err := call(t.Context())
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, obs.RequestID)
			assert.Equal(t, tt.wantRay, obs.RayID)
		})
	}
}

func TestCallSendsJSONStateVerbatim(t *testing.T) {
	state := discern.JSON([]byte(`{"ticket_id":12345678901234567890,"subject":"refund <now> & later","tags":["billing"]}`))
	req := stateRequest(state)
	req.Questions[0].Instructions = discern.Text("Is <prod> down & blocking checkout?")
	srv := newServer(t, okReply(envelope(outageResult)))
	_, err := testutil.Evaluate(t, serverClient(t, srv), req)
	require.NoError(t, err)

	reqs := srv.Requests()
	require.Len(t, reqs, 1)
	wantBody := `{"model":"clef",` +
		`"state":{"ticket_id":12345678901234567890,"subject":"refund <now> & later","tags":["billing"]},` +
		`"questions":{"outage":{"type":"noul","instructions":"Is <prod> down & blocking checkout?"}}}`
	assertBody(t, wantBody, reqs[0].Body)
	for _, escaped := range []string{`\u003c`, `\u003e`, `\u0026`} {
		assert.NotContains(t, string(reqs[0].Body), escaped)
	}
	assert.NotContains(t, string(reqs[0].Body), `"state":"{`)
}

func TestCallDecodesSuccessEnvelope(t *testing.T) {
	srv := newServer(t, okReply(envelope(ticketResult(clef.ModelFlash))))
	res, err := testutil.Evaluate(t, serverClient(t, srv, clef.WithModel(clef.ModelFlash)), ticketRequest())
	require.NoError(t, err)
	assert.Equal(t, 1, srv.Hits())
	assert.Equal(t, discern.Complete, res.Status)
	require.Len(t, res.Outcomes, 1)
	assert.Equal(t, discern.Clef, res.Outcomes[0].Provider)
	assert.Equal(t, clef.ModelFlash, res.Outcomes[0].RequestedModel)

	obs, ok := res.Observation(discern.Clef)
	require.True(t, ok)
	want := discern.Observation{
		Model:     clef.ModelFlash,
		RequestID: testRequestID,
		RayID:     testRayID,
		Usage:     &discern.Usage{InputTokens: new(int64(312)), OutputTokens: new(int64(0)), Neurons: new(1.19)},
		Answers: []discern.Answer{
			{QuestionID: qQueue, Kind: discern.KindChoice, Choice: &discern.ChoiceAnswer{
				Option: optBilling, Confidence: new(0.9104),
				Probabilities: []discern.OptionProbability{
					{Option: optTechnical, Probability: 0.0147},
					{Option: optBilling, Probability: 0.9541},
					{Option: optAccount, Probability: 0.0312},
				},
			}},
			{QuestionID: qOutage, Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: 0.0731}},
			{QuestionID: qSeverity, Kind: discern.KindScore, Score: &discern.ScoreAnswer{
				Value: 1.0991, Confidence: new(0.8517),
				Probabilities: []discern.LevelProbability{
					{Level: levelCosmetic, Probability: 0.0012},
					{Level: levelDegraded, Probability: 0.8985},
					{Level: levelBlocked, Probability: 0.1003},
				},
			}},
		},
	}
	assert.Equal(t, want, obs)
	sev, ok := obs.Score(qSeverity)
	require.True(t, ok)
	assert.Equal(t, levelDegraded, sev.Nearest())
}

func TestCallDecodesObservedEnvelope(t *testing.T) {
	// The shape recorded from a live clef-flash call.
	body := `{"result":{"model":"clef-flash","answers":{"route":{"type":"choice","choice":"billing",` +
		`"probabilities":{"billing":0.9854,"support":0.0146},"confidence":0.9423}},` +
		`"usage":{"input_tokens":145,"output_tokens":0}},"success":true,"errors":[],"messages":[]}`
	srv := newServer(t, okReply(body))
	req := discern.Request{
		Providers: discern.ClefOnly,
		State:     discern.Text("Subject: Charged twice\n\nMy invoice was paid twice this month."),
		Questions: []discern.Question{discern.Choice("route", discern.Text("Which team should handle this ticket?"),
			discern.Opt("support", discern.Text("General product help")),
			discern.Opt(optBilling, discern.Text("Invoices, refunds, failed payments")))},
	}
	res, err := testutil.Evaluate(t, serverClient(t, srv, clef.WithModel(clef.ModelFlash)), req)
	require.NoError(t, err)
	obs, ok := res.Observation(discern.Clef)
	require.True(t, ok)
	q, ok := obs.Choice("route")
	require.True(t, ok)
	assert.Equal(t, optBilling, q.Option)
	assert.Equal(t, []discern.OptionProbability{{Option: "support", Probability: 0.0146}, {Option: optBilling, Probability: 0.9854}}, q.Probabilities)
	assert.Equal(t, &discern.Usage{InputTokens: new(int64(145)), OutputTokens: new(int64(0)), Neurons: new(1.19)}, obs.Usage)
}

func TestCallNeuronsHeader(t *testing.T) {
	tests := []struct {
		name  string
		value *string // nil means the header is absent
		want  *float64
	}{
		{name: "observed value", value: new("1.19"), want: new(1.19)},
		{name: "integer", value: new("3"), want: new(3.0)},
		{name: "zero", value: new("0"), want: new(0.0)},
		{name: "negative zero", value: new("-0"), want: new(0.0)},
		{name: "surrounding space", value: new(" 2.5 "), want: new(2.5)},
		{name: "absent"},
		{name: "empty", value: new("")},
		{name: "garbage", value: new("about one")},
		{name: "negative", value: new("-1.19")},
		{name: "not a number", value: new("NaN")},
		{name: "infinite", value: new("+Inf")},
		{name: "overflow", value: new("1e400")},
		{name: "two values joined", value: new("1.19, 2")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct := okTransport(envelope(outageResult))
			if tt.value != nil {
				ct.header.Set("Cf-Ai-Neurons", *tt.value)
			}
			res, err := testutil.Evaluate(t, ct.client(t), noulRequest())
			require.NoError(t, err)
			obs, ok := res.Observation(discern.Clef)
			require.True(t, ok)
			require.NotNil(t, obs.Usage)
			assert.Equal(t, new(int64(88)), obs.Usage.InputTokens)
			assert.Equal(t, tt.want, obs.Usage.Neurons)
			if tt.want != nil {
				require.NotNil(t, obs.Usage.Neurons)
				assert.False(t, math.Signbit(*obs.Usage.Neurons))
			}
		})
	}
}

func TestCallUsageWithoutTokenCounts(t *testing.T) {
	result := `{"answers":{"outage":{"type":"noul","noul":0.0731}}}`
	tests := []struct {
		name    string
		neurons string
		want    *discern.Usage
	}{
		{name: "neurons only", neurons: testNeurons, want: &discern.Usage{Neurons: new(1.19)}},
		{name: "no usage at all"},
		{name: "unusable neurons", neurons: "n/a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct := okTransport(envelope(result))
			if tt.neurons != "" {
				ct.header.Set("Cf-Ai-Neurons", tt.neurons)
			}
			res, err := testutil.Evaluate(t, ct.client(t), noulRequest())
			require.NoError(t, err)
			obs, ok := res.Observation(discern.Clef)
			require.True(t, ok)
			assert.Empty(t, obs.Model)
			assert.Equal(t, tt.want, obs.Usage)
		})
	}
}

func TestCallMissingMetadataHeaders(t *testing.T) {
	ct := &countingTransport{status: http.StatusOK, body: envelope(outageResult)}
	res, err := testutil.Evaluate(t, ct.client(t), noulRequest())
	require.NoError(t, err)
	obs, ok := res.Observation(discern.Clef)
	require.True(t, ok)
	assert.Empty(t, obs.RequestID)
	assert.Empty(t, obs.RayID)
	require.NotNil(t, obs.Usage)
	assert.Nil(t, obs.Usage.Neurons)
}

func TestClientConcurrentUse(t *testing.T) {
	const n = 16
	srv := newServer(t, okReply(envelope(ticketResult(clef.ModelClef))))
	ev := testutil.Evaluator(t, discern.WithAdapter(serverClient(t, srv)))
	ctx := t.Context()

	var wg sync.WaitGroup
	errs := make([]error, n)
	statuses := make([]discern.ResultStatus, n)
	for i := range n {
		wg.Go(func() {
			res, callErr := ev.Evaluate(ctx, ticketRequest())
			errs[i] = callErr
			if res != nil {
				statuses[i] = res.Status
			}
		})
	}
	wg.Wait()

	for i := range n {
		require.NoError(t, errs[i], "worker %d", i)
		assert.Equal(t, discern.Complete, statuses[i], "worker %d", i)
	}
	assert.Equal(t, n, srv.Hits())
}

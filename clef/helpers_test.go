package clef_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
	"github.com/mrz1836/go-discern/internal/testutil"
)

// Synthetic credentials: low-entropy values that are obviously not real.
const (
	testAccount = "0123456789abcdef0123456789abcdef"
	testToken   = "test-token"
)

// Response metadata the fake servers and transports send.
const (
	testRequestID = "req-7d1c-synthetic"
	testRayID     = "8c1f2a3b4d5e6f70-SJC"
	testNeurons   = "1.19"
)

// Question, option and level IDs of the synthetic ticket requests.
const (
	qQueue        = "queue"
	qOutage       = "outage"
	qSeverity     = "severity"
	optTechnical  = "technical"
	optBilling    = "billing"
	optAccount    = "account"
	levelCosmetic = "cosmetic"
	levelDegraded = "degraded"
	levelBlocked  = "blocked"
)

// Ticket text and the outage question used by the one-question requests.
const (
	ticketState        = "Subject: Checkout down\n\nOur checkout page has returned errors since 09:00."
	outageInstructions = "Does the customer report a production outage?"
)

// outageResult is a System One result for noulRequest.
const outageResult = `{"model":"clef","answers":{"outage":{"type":"noul","noul":0.0731}},"usage":{"input_tokens":88,"output_tokens":0}}`

// capacityEnvelope is the Workers AI capacity error envelope.
const capacityEnvelope = `{"result":null,"success":false,"errors":[{"code":3040,"message":"Capacity temporarily exceeded, please try again."}],"messages":[]}`

var errSyntheticReset = errors.New("synthetic connection reset")

// envelope wraps a System One result in a successful Cloudflare envelope.
func envelope(result string) string {
	return `{"result":` + result + `,"success":true,"errors":[],"messages":[]}`
}

// ticketRequest is a synthetic support-ticket request with one question of each
// kind; the choice options are deliberately not in alphabetical order.
func ticketRequest() discern.Request {
	outage := discern.Noul(qOutage, discern.Text(outageInstructions))
	outage.Yes = discern.Text("Production is down for the customer.")
	outage.No = discern.Text("Production works; the issue is elsewhere.")
	return discern.Request{
		Providers: discern.ClefOnly,
		State:     discern.Text("Subject: Charged twice\n\nMy invoice was paid twice this month."),
		Questions: []discern.Question{
			discern.Choice(qQueue, discern.Text("Which support queue should handle this ticket?"),
				discern.Opt(optTechnical, discern.Text("Errors, outages, integration problems")),
				discern.Opt(optBilling, discern.Text("Invoices, refunds, failed payments")),
				discern.Opt(optAccount, discern.Text("Login, profile, permissions"))),
			outage,
			discern.Score(qSeverity, discern.Text("How severe is the customer impact?"),
				discern.Level(levelCosmetic, discern.Text("Cosmetic; work continues")),
				discern.Level(levelDegraded, discern.Text("Degraded; workaround exists")),
				discern.Level(levelBlocked, discern.Text("Blocked; no workaround"))),
		},
		CorrelationID: "ticket-1042",
	}
}

// ticketResult is a System One result for ticketRequest: answers, probability
// keys and legend keys are in a different order than the request, and the legend
// values are objects.
func ticketResult(model string) string {
	return `{"model":"` + model + `","answers":{` +
		`"severity":{"type":"score","score":1.0991,"probabilities":{"2":0.1003,"0":0.0012,"1":0.8985},` +
		`"legend":{"2":{"label":"blocked"},"0":{"label":"cosmetic"},"1":{"label":"degraded"}},"confidence":0.8517},` +
		`"queue":{"type":"choice","choice":"billing","probabilities":{"account":0.0312,"billing":0.9541,"technical":0.0147},"confidence":0.9104},` +
		`"outage":{"type":"noul","noul":0.0731}},` +
		`"usage":{"input_tokens":312,"output_tokens":0}}`
}

// stateRequest is a one-question Clef request with the given state.
func stateRequest(state discern.Value) discern.Request {
	return discern.Request{
		Providers: discern.ClefOnly,
		State:     state,
		Questions: []discern.Question{discern.Noul(qOutage, discern.Text(outageInstructions))},
	}
}

// noulRequest is a one-question Clef request answered by outageResult.
func noulRequest() discern.Request { return stateRequest(discern.Text(ticketState)) }

// manyQuestions returns a request with n noul questions q00, q01, and so on.
func manyQuestions(n int, providers discern.ProviderSet) discern.Request {
	qs := make([]discern.Question, 0, n)
	for i := range n {
		qs = append(qs, discern.Noul(fmt.Sprintf("q%02d", i), discern.Text("Does the ticket mention item "+strconv.Itoa(i)+"?")))
	}
	return discern.Request{Providers: providers, State: discern.Text(ticketState), Questions: qs}
}

// manyAnswers is a System One result answering manyQuestions(n).
func manyAnswers(n int) string {
	var b strings.Builder
	b.WriteString(`{"model":"clef","answers":{`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"q%02d":{"type":"noul","noul":0.5}`, i)
	}
	b.WriteString(`}}`)
	return b.String()
}

// countingTransport is an in-memory http.RoundTripper that answers every request
// with a fixed response, or fails with err, and counts the attempts. It never
// touches the network.
type countingTransport struct {
	status    int
	header    http.Header
	body      string
	err       error
	attempts  atomic.Int32
	bodyBytes atomic.Int64
}

// okTransport answers with HTTP 200, the standard metadata headers and body.
func okTransport(body string) *countingTransport {
	return &countingTransport{status: http.StatusOK, header: metadataHeader(), body: body}
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.attempts.Add(1)
	c.bodyBytes.Store(testutil.Drain(r))
	if c.err != nil {
		return nil, c.err
	}
	h := c.header.Clone()
	if h == nil {
		h = http.Header{}
	}
	return &http.Response{
		StatusCode:    c.status,
		Header:        h,
		Body:          io.NopCloser(strings.NewReader(c.body)),
		ContentLength: -1,
		Request:       r,
	}, nil
}

// client returns a Client on the production URL whose calls go to c only.
func (c *countingTransport) client(t testing.TB) *clef.Client {
	t.Helper()
	cc, err := clef.New(testAccount, testToken, clef.WithHTTPClient(&http.Client{Transport: c}))
	require.NoError(t, err)
	return cc
}

// metadataHeader holds the headers Workers AI sends on every response.
func metadataHeader() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Cf-Ai-Req-Id", testRequestID)
	h.Set("Cf-Ray", testRayID)
	return h
}

// reply is a fake server's fixed response.
type reply struct {
	status int
	header http.Header
	body   string
}

// okReply is HTTP 200 with the metadata headers, a neurons figure and body.
func okReply(body string) reply {
	h := metadataHeader()
	h.Set("Cf-Ai-Neurons", testNeurons)
	return reply{status: http.StatusOK, header: h, body: body}
}

// statusReply is the given status with the metadata headers and body.
func statusReply(status int, body string) reply {
	return reply{status: status, header: metadataHeader(), body: body}
}

func (r reply) write(w http.ResponseWriter, _ *http.Request) {
	for k, vs := range r.header {
		w.Header()[k] = slices.Clone(vs)
	}
	w.WriteHeader(r.status)
	_, _ = io.WriteString(w, r.body)
}

// newServer starts a recording test server that answers every request with
// rep.
func newServer(t *testing.T, rep reply) *testutil.Server {
	t.Helper()
	return testutil.NewServer(t, rep.write)
}

// serverClient returns a Client that calls srv; later options win.
func serverClient(t *testing.T, srv *testutil.Server, opts ...clef.Option) *clef.Client {
	t.Helper()
	all := append([]clef.Option{clef.WithTestEndpoint(srv.URL()), clef.WithHTTPClient(srv.Client())}, opts...)
	c, err := clef.New(testAccount, testToken, all...)
	require.NoError(t, err)
	return c
}

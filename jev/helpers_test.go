package jev_test

import (
	"errors"
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/testutil"
	"github.com/mrz1836/go-discern/jev"
)

const (
	testKey         = "test-key"
	pinnedModel     = "jev-1.13.0"
	requestIDHeader = "x-typesafe-request-id"
	testRequestID   = "req-01HX7Q2K9M"
	systemOnePath   = "/v1/systemone"

	qQueue     = "queue"
	qOutage    = "outage"
	qSeverity  = "severity"
	optBilling = "billing"
)

// Planted strings that must never appear in errors, printed clients or records.
const (
	sentinelKey         = "test-key-SENTINEL"
	stateSentinel       = "STATE-SENTINEL-7f3a"
	instructionSentinel = "INSTRUCTION-SENTINEL-91bc"
)

var (
	errConnectionReset = errors.New("connection reset by peer")
	errUnexpectedCall  = errors.New("unexpected round trip")
)

// ticketRequest is a synthetic support-ticket request with one question of each
// kind. The Choice options are deliberately not in alphabetical order.
func ticketRequest() discern.Request {
	outage := discern.Noul(qOutage, discern.Text("Does the customer report a production outage?"))
	outage.Yes = discern.Text("The product is down for the customer")
	outage.No = discern.Text("The product still works")
	return discern.Request{
		Providers: discern.JevOnly,
		State:     discern.Text("Subject: Charged twice\n\nMy invoice was paid twice this month."),
		Questions: []discern.Question{
			discern.Choice(qQueue, discern.Text("Which support queue should handle this ticket?"),
				discern.Opt("technical", discern.Text("Errors, outages, integration problems")),
				discern.Opt(optBilling, discern.Text("Invoices, refunds, failed payments")),
				discern.Opt("account", discern.Text("Login, profile, permissions"))),
			outage,
			discern.Score(qSeverity, discern.Text("How severe is the customer impact?"),
				discern.Level("cosmetic", discern.Text("Cosmetic; work continues")),
				discern.Level("degraded", discern.Text("Degraded; workaround exists")),
				discern.Level("blocked", discern.Text("Blocked; no workaround"))),
		},
	}
}

// ticketWire is the exact request body of ticketRequest for pinnedModel.
const ticketWire = `{"model":"jev-1.13.0",` +
	`"state":"Subject: Charged twice\n\nMy invoice was paid twice this month.",` +
	`"questions":{` +
	`"queue":{"type":"choice","instructions":"Which support queue should handle this ticket?",` +
	`"criteria":{"technical":"Errors, outages, integration problems","billing":"Invoices, refunds, failed payments","account":"Login, profile, permissions"}},` +
	`"outage":{"type":"noul","instructions":"Does the customer report a production outage?",` +
	`"criteria":{"true":"The product is down for the customer","false":"The product still works"}},` +
	`"severity":{"type":"score","instructions":"How severe is the customer impact?",` +
	`"criteria":["Cosmetic; work continues","Degraded; workaround exists","Blocked; no workaround"]}` +
	`}}`

// ticketResponse answers ticketRequest. Answers and probability keys are in a
// different order from the questions, options and levels, and the legend values
// are objects.
const ticketResponse = `{"model":"jev-1.13.0","answers":{` +
	`"severity":{"type":"score","score":1.05,` +
	`"legend":{"0":{"label":"Cosmetic"},"1":{"label":"Degraded"},"2":{"label":"Blocked"}},` +
	`"probabilities":{"2":0.05,"0":0,"1":0.95},"confidence":0.92},` +
	`"queue":{"type":"choice","choice":"billing","probabilities":{"account":0.03,"billing":0.91,"technical":0.06},"confidence":0.87},` +
	`"outage":{"type":"noul","noul":0.07}` +
	`},"usage":{"input_tokens":304,"output_tokens":0}}`

// sparseResponse answers ticketRequest without the optional model, usage and
// confidence fields.
const sparseResponse = `{"answers":{` +
	`"queue":{"type":"choice","choice":"billing","probabilities":{"technical":0.06,"billing":0.91,"account":0.03}},` +
	`"outage":{"type":"noul","noul":0.07},` +
	`"severity":{"type":"score","score":1.05,"probabilities":{"0":0,"1":0.95,"2":0.05}}` +
	`}}`

// ticketObservation is the decoding of ticketResponse with the given request ID.
func ticketObservation(requestID string) *discern.Observation {
	return &discern.Observation{
		Model:     pinnedModel,
		RequestID: requestID,
		Usage:     &discern.Usage{InputTokens: new(int64(304)), OutputTokens: new(int64(0))},
		Answers: []discern.Answer{
			{QuestionID: qQueue, Kind: discern.KindChoice, Choice: &discern.ChoiceAnswer{
				Option:     optBilling,
				Confidence: new(0.87),
				Probabilities: []discern.OptionProbability{
					{Option: "technical", Probability: 0.06},
					{Option: optBilling, Probability: 0.91},
					{Option: "account", Probability: 0.03},
				},
			}},
			{QuestionID: qOutage, Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: 0.07}},
			{QuestionID: qSeverity, Kind: discern.KindScore, Score: &discern.ScoreAnswer{
				Value:      1.05,
				Confidence: new(0.92),
				Probabilities: []discern.LevelProbability{
					{Level: "cosmetic", Probability: 0},
					{Level: "degraded", Probability: 0.95},
					{Level: "blocked", Probability: 0.05},
				},
			}},
		},
	}
}

// replyHeader is the header of every fake reply: testRequestID, a JSON content
// type and any extra header name/value pairs.
func replyHeader(headerPairs ...string) http.Header {
	h := http.Header{}
	h.Set(requestIDHeader, testRequestID)
	h.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headerPairs); i += 2 {
		h.Set(headerPairs[i], headerPairs[i+1])
	}
	return h
}

// respond returns an in-memory transport that consumes the request and answers
// with the status, the body and replyHeader(headerPairs...).
func respond(status int, body string, headerPairs ...string) testutil.RoundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		testutil.Drain(r)
		return &http.Response{
			StatusCode:    status,
			Header:        replyHeader(headerPairs...),
			Body:          io.NopCloser(strings.NewReader(body)),
			ContentLength: int64(len(body)),
			Request:       r,
		}, nil
	}
}

// inMemory returns a Client for pinnedModel whose calls go to rt. The production
// URL is kept: rt never touches the network.
func inMemory(tb testing.TB, rt http.RoundTripper, opts ...jev.Option) *jev.Client {
	tb.Helper()
	c, err := jev.New(testKey, append([]jev.Option{jev.WithModel(pinnedModel), jev.WithHTTPClient(&http.Client{Transport: rt})}, opts...)...)
	require.NoError(tb, err)
	return c
}

// serverClient returns a Client for pinnedModel aimed at srv; later options win.
func serverClient(t *testing.T, srv *testutil.Server, opts ...jev.Option) *jev.Client {
	t.Helper()
	base := []jev.Option{jev.WithModel(pinnedModel), jev.WithTestEndpoint(srv.URL()), jev.WithHTTPClient(srv.Client())}
	c, err := jev.New(testKey, append(base, opts...)...)
	require.NoError(t, err)
	return c
}

// reply returns a handler that answers with the status, the body and
// replyHeader(headerPairs...).
func reply(status int, body string, headerPairs ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		maps.Copy(w.Header(), replyHeader(headerPairs...))
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

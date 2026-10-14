package clef_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
)

// Planted strings that must never appear in error strings, formatted clients or
// result records.
const (
	sentinelToken       = "test-token-SENTINEL"
	stateSentinel       = "STATE-SENTINEL-7f3a"
	instructionSentinel = "INSTRUCTION-SENTINEL-91bc"
	descriptionSentinel = "DESCRIPTION-SENTINEL-c2d4"
	messageSentinel     = "MESSAGE-SENTINEL-55e1"
)

func TestCallRejectsUnsuccessfulEnvelope(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		codes    []int
		messages []string
	}{
		{
			name:     "success false on HTTP 200",
			body:     capacityEnvelope,
			codes:    []int{3040},
			messages: []string{"Capacity temporarily exceeded, please try again."},
		},
		{
			name:     "success true with errors",
			body:     `{"result":` + outageResult + `,"success":true,"errors":[{"code":3036,"message":"Daily allocation exceeded"}],"messages":[]}`,
			codes:    []int{3036},
			messages: []string{"Daily allocation exceeded"},
		},
		{
			name:     "success true with a message-only error",
			body:     `{"result":` + outageResult + `,"success":true,"errors":[{"message":"Model is warming up"}]}`,
			messages: []string{"Model is warming up"},
		},
		{
			name: "success false without errors",
			body: `{"result":` + outageResult + `,"success":false}`,
		},
		{
			name: "success true with an empty error object",
			body: `{"result":` + outageResult + `,"success":true,"errors":[{}],"messages":[]}`,
		},
		{
			name: "success true with an error that has neither code nor message",
			body: `{"result":` + outageResult + `,"success":true,"errors":[{"documentation_url":"https://example.invalid/docs"}]}`,
		},
		{
			name:     "success false with string errors",
			body:     `{"result":null,"success":false,"errors":["No such model"]}`,
			messages: []string{"No such model"},
		},
		{
			name:     "success false with a string code",
			body:     `{"result":null,"success":false,"errors":[{"code":"5007","message":"No such model"},{"code":5007.0}]}`,
			codes:    []int{5007},
			messages: []string{"No such model"},
		},
		{
			name: "success false with errors that are not an array",
			body: `{"result":null,"success":false,"errors":{"code":5007}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newServer(t, okReply(tt.body))
			res, err := testutil.Evaluate(t, serverClient(t, srv), noulRequest())
			require.ErrorIs(t, err, discern.ErrRejected)
			assert.Equal(t, 1, srv.Hits())
			pe := testutil.ProviderError(t, err)
			assert.Equal(t, discern.FailureRejected, pe.Kind)
			assert.Equal(t, http.StatusOK, pe.StatusCode)
			assert.Equal(t, tt.codes, pe.Codes)
			assert.Equal(t, tt.messages, pe.ProviderMessages())
			assert.Equal(t, testRequestID, pe.RequestID)
			assert.Equal(t, testRayID, pe.RayID)
			assert.Equal(t, "discern: clef: rejected (HTTP 200)", err.Error())
			for _, m := range tt.messages {
				assert.NotContains(t, err.Error(), m)
				assert.NotContains(t, pe.GoString(), m)
			}
			require.NotNil(t, res)
			assert.Equal(t, discern.Failed, res.Status)
			require.NotNil(t, res.Outcomes[0].Failure)
			assert.Equal(t, tt.codes, res.Outcomes[0].Failure.Codes)
		})
	}
}

func TestCallMalformedEnvelope(t *testing.T) {
	answer := `{"type":"noul","noul":0.0731}`
	tests := []struct {
		name string
		body string
	}{
		{name: "missing success", body: `{"result":` + outageResult + `,"errors":[],"messages":[]}`},
		{name: "success not a boolean", body: `{"result":` + outageResult + `,"success":"true"}`},
		{name: "case-variant success", body: `{"result":` + outageResult + `,"Success":true}`},
		{name: "null result", body: `{"result":null,"success":true,"errors":[],"messages":[]}`},
		{name: "absent result", body: `{"success":true,"errors":[],"messages":[]}`},
		{name: "empty body", body: ``},
		{name: "invalid JSON", body: `{"result":{"answers":},"success":true}`},
		{name: "HTML page", body: `<html><body>Bad gateway</body></html>`},
		{name: "truncated JSON", body: `{"result":{"model":"clef","answers":{"outage":{"type":"noul","noul":0.07`},
		{name: "trailing data", body: envelope(outageResult) + `{}`},
		{name: "duplicate envelope key", body: `{"success":true,"success":true,"result":` + outageResult + `}`},
		{name: "duplicate result key", body: envelope(`{"answers":{"outage":` + answer + `},"answers":{"outage":` + answer + `}}`)},
		{name: "duplicate answer key", body: envelope(`{"answers":{"outage":` + answer + `,"outage":` + answer + `}}`)},
		{name: "extra answer ID", body: envelope(`{"answers":{"outage":` + answer + `,"priority":` + answer + `}}`)},
		{name: "missing answer", body: envelope(`{"answers":{}}`)},
		{name: "case-variant answers key", body: envelope(`{"Answers":{"outage":` + answer + `}}`)},
		{name: "case-variant answer ID", body: envelope(`{"answers":{"Outage":` + answer + `}}`)},
		{name: "value type mismatch", body: envelope(`{"answers":{"outage":{"type":"noul","noul":"0.07"}}}`)},
		{name: "answer type mismatch", body: envelope(`{"answers":{"outage":{"type":"choice","choice":"yes"}}}`)},
		{name: "probability above one", body: envelope(`{"answers":{"outage":{"type":"noul","noul":1.5}}}`)},
		{name: "probability below zero", body: envelope(`{"answers":{"outage":{"type":"noul","noul":-0.01}}}`)},
		{name: "probability overflow", body: envelope(`{"answers":{"outage":{"type":"noul","noul":1e400}}}`)},
		{name: "negative token count", body: envelope(`{"answers":{"outage":` + answer + `},"usage":{"input_tokens":-1}}`)},
		{name: "result not an object", body: envelope(`[1,2]`)},
		{name: "errors not an array", body: `{"result":` + outageResult + `,"success":true,"errors":{}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newServer(t, okReply(tt.body))
			res, err := testutil.Evaluate(t, serverClient(t, srv), noulRequest())
			require.ErrorIs(t, err, discern.ErrMalformedResponse)
			require.NotErrorIs(t, err, discern.ErrRejected)
			assert.Equal(t, 1, srv.Hits())
			pe := testutil.ProviderError(t, err)
			assert.Equal(t, discern.FailureMalformed, pe.Kind)
			assert.Equal(t, http.StatusOK, pe.StatusCode)
			assert.Equal(t, testRequestID, pe.RequestID)
			assert.Equal(t, testRayID, pe.RayID)
			assert.Equal(t, "discern: clef: malformed_response (HTTP 200)", err.Error())
			require.NotNil(t, res)
			assert.Equal(t, discern.Failed, res.Status)
			require.NotNil(t, res.Outcomes[0].Failure)
			assert.Equal(t, testRequestID, res.Outcomes[0].Failure.RequestID)
			assert.Equal(t, testRayID, res.Outcomes[0].Failure.RayID)
		})
	}
}

func TestCallRejectsInconsistentProbabilities(t *testing.T) {
	// Decoding succeeds; package discern's observation check rejects the sum.
	body := envelope(`{"answers":{` +
		`"queue":{"type":"choice","choice":"billing","probabilities":{"technical":0.3,"billing":0.6,"account":0.6}},` +
		`"outage":{"type":"noul","noul":0.1},` +
		`"severity":{"type":"score","score":1,"probabilities":{"0":0,"1":1,"2":0}}}}`)
	srv := newServer(t, okReply(body))
	_, err := testutil.Evaluate(t, serverClient(t, srv), ticketRequest())
	require.ErrorIs(t, err, discern.ErrMalformedResponse)
	pe := testutil.ProviderError(t, err)
	assert.Equal(t, discern.FailureMalformed, pe.Kind)
	assert.Equal(t, testRequestID, pe.RequestID)
	assert.Equal(t, testRayID, pe.RayID)
}

func TestCallRejectsNon2xx(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		header     map[string]string
		body       string
		codes      []int
		messages   []string
		retryAfter time.Duration
		hasRetry   bool
	}{
		{
			name: "authentication error", status: http.StatusUnauthorized,
			body:  `{"result":null,"success":false,"errors":[{"code":10000,"message":"Authentication error"}],"messages":[]}`,
			codes: []int{10000}, messages: []string{"Authentication error"},
		},
		{
			name: "no route", status: http.StatusBadRequest,
			body:  `{"success":false,"errors":[{"code":7000,"message":"No route for that URI"}],"messages":[],"result":null}`,
			codes: []int{7000}, messages: []string{"No route for that URI"},
		},
		{
			name: "forbidden", status: http.StatusForbidden,
			body:  `{"result":null,"success":false,"errors":[{"code":10000,"message":"Authentication error"}],"messages":[]}`,
			codes: []int{10000}, messages: []string{"Authentication error"},
		},
		{
			name: "request timeout", status: http.StatusRequestTimeout,
			body:  `{"result":null,"success":false,"errors":[{"code":3007,"message":"Request timeout"}],"messages":[]}`,
			codes: []int{3007}, messages: []string{"Request timeout"},
		},
		{
			name: "request too large", status: http.StatusRequestEntityTooLarge,
			body:  `{"result":null,"success":false,"errors":[{"code":3006,"message":"Request is too large"}],"messages":[]}`,
			codes: []int{3006}, messages: []string{"Request is too large"},
		},
		{
			name: "capacity exceeded", status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "30"},
			body:  capacityEnvelope,
			codes: []int{3040}, messages: []string{"Capacity temporarily exceeded, please try again."},
			retryAfter: 30 * time.Second, hasRetry: true,
		},
		{
			name: "daily allocation exceeded", status: http.StatusTooManyRequests,
			body:  `{"result":null,"success":false,"errors":[{"code":3036,"message":"Daily allocation exceeded"}],"messages":[]}`,
			codes: []int{3036}, messages: []string{"Daily allocation exceeded"},
		},
		{
			name: "non-JSON server error", status: http.StatusInternalServerError,
			body: `upstream connect error or disconnect/reset before headers`,
		},
		{
			name: "success envelope on a server error", status: http.StatusInternalServerError,
			body: envelope(outageResult),
		},
		{name: "unavailable without body", status: http.StatusServiceUnavailable, header: map[string]string{"Retry-After": "0"}, hasRetry: true},
		{
			name: "envelope without success", status: http.StatusNotFound,
			body:  `{"errors":[{"code":5007,"message":"No such model"}]}`,
			codes: []int{5007}, messages: []string{"No such model"},
		},
		{
			name: "string errors and codes", status: http.StatusBadRequest,
			body:  `{"success":false,"errors":["Bad input",{"code":"5007"},{"code":3010.0,"message":"Invalid input"}]}`,
			codes: []int{3010}, messages: []string{"Bad input", "Invalid input"},
		},
		{
			name: "fractional retry-after-ms", status: http.StatusTooManyRequests, header: map[string]string{"Retry-After-Ms": "250.5", "Retry-After": "30"},
			body:  capacityEnvelope,
			codes: []int{3040}, messages: []string{"Capacity temporarily exceeded, please try again."},
			retryAfter: 251 * time.Millisecond, hasRetry: true,
		},
		{name: "origin timeout page", status: 524, body: `<html><body>A timeout occurred</body></html>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := statusReply(tt.status, tt.body)
			for k, v := range tt.header {
				rep.header.Set(k, v)
			}
			srv := newServer(t, rep)
			res, err := testutil.Evaluate(t, serverClient(t, srv), noulRequest())
			require.ErrorIs(t, err, discern.ErrRejected)
			assert.Equal(t, 1, srv.Hits())
			pe := testutil.ProviderError(t, err)
			assert.Equal(t, discern.FailureRejected, pe.Kind)
			assert.Equal(t, tt.status, pe.StatusCode)
			assert.Equal(t, tt.codes, pe.Codes)
			assert.Equal(t, tt.messages, pe.ProviderMessages())
			assert.Equal(t, tt.retryAfter, pe.RetryAfter)
			assert.Equal(t, tt.hasRetry, pe.HasRetryAfter)
			assert.Equal(t, testRequestID, pe.RequestID)
			assert.Equal(t, testRayID, pe.RayID)
			assert.Equal(t, "discern: clef: rejected (HTTP "+strconv.Itoa(tt.status)+")", err.Error())
			require.NotNil(t, res)
			assert.Equal(t, discern.Failed, res.Status)
			require.NotNil(t, res.Outcomes[0].Failure)
			assert.Equal(t, tt.status, res.Outcomes[0].Failure.StatusCode)
			assert.Equal(t, tt.codes, res.Outcomes[0].Failure.Codes)
		})
	}
}

func TestCallDoesNotFollowRedirects(t *testing.T) {
	for _, status := range []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			target := newServer(t, okReply(envelope(outageResult)))
			origin := testutil.NewServer(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL()+"/collect", status)
			})
			res, err := testutil.Evaluate(t, serverClient(t, origin), noulRequest())
			require.ErrorIs(t, err, discern.ErrRejected)
			pe := testutil.ProviderError(t, err)
			assert.Equal(t, discern.FailureRejected, pe.Kind)
			assert.Equal(t, status, pe.StatusCode)
			assert.Nil(t, pe.Codes)
			assert.Equal(t, 1, origin.Hits())
			assert.Equal(t, 0, target.Hits())
			for _, r := range target.Requests() {
				assert.Empty(t, r.Header.Values("Authorization"))
			}
			require.NotNil(t, res)
			assert.Equal(t, discern.Failed, res.Status)
		})
	}
}

func TestCallResponseSizeLimit(t *testing.T) {
	body := envelope(ticketResult(clef.ModelClef))
	require.Greater(t, len(body), 256)

	t.Run("over the limit", func(t *testing.T) {
		srv := newServer(t, okReply(body))
		res, err := testutil.Evaluate(t, serverClient(t, srv, clef.WithMaxResponseBytes(256)), ticketRequest())
		require.ErrorIs(t, err, discern.ErrMalformedResponse)
		pe := testutil.ProviderError(t, err)
		assert.Equal(t, discern.FailureTooLarge, pe.Kind)
		assert.Equal(t, http.StatusOK, pe.StatusCode)
		assert.Equal(t, testRequestID, pe.RequestID)
		assert.Equal(t, 1, srv.Hits())
		require.NotNil(t, res)
		assert.Equal(t, discern.Failed, res.Status)
	})

	t.Run("oversized refusal keeps the retry hint", func(t *testing.T) {
		rep := statusReply(http.StatusTooManyRequests, strings.Repeat(" ", 512))
		rep.header.Set("Retry-After", "30")
		srv := newServer(t, rep)
		res, err := testutil.Evaluate(t, serverClient(t, srv, clef.WithMaxResponseBytes(256)), ticketRequest())
		require.ErrorIs(t, err, discern.ErrMalformedResponse)
		pe := testutil.ProviderError(t, err)
		assert.Equal(t, discern.FailureTooLarge, pe.Kind)
		assert.Equal(t, http.StatusTooManyRequests, pe.StatusCode)
		assert.True(t, pe.HasRetryAfter)
		assert.Equal(t, 30*time.Second, pe.RetryAfter)
		require.NotNil(t, res)
		require.NotNil(t, res.Outcomes[0].Failure)
		assert.Equal(t, new(30*time.Second), res.Outcomes[0].Failure.RetryAfter)
	})

	t.Run("exactly the limit", func(t *testing.T) {
		srv := newServer(t, okReply(body))
		res, err := testutil.Evaluate(t, serverClient(t, srv, clef.WithMaxResponseBytes(int64(len(body)))), ticketRequest())
		require.NoError(t, err)
		assert.Equal(t, discern.Complete, res.Status)
	})
}

func TestEvaluateUnsupportedMakesNoCall(t *testing.T) {
	srv := newServer(t, okReply(envelope(outageResult)))
	jev := discerntest.Static(discern.Jev, discern.Observation{})
	ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(serverClient(t, srv)))
	tests := []struct {
		name  string
		req   discern.Request
		field string
	}{
		{name: "65 questions", req: manyQuestions(clef.MaxQuestions+1, discern.ClefOnly), field: "questions"},
		{name: "state over 13 MiB", req: stateRequest(discern.Text(strings.Repeat("a", clef.MaxRequestBytes))), field: "request"},
		{name: "dual run with 65 questions", req: manyQuestions(clef.MaxQuestions+1, discern.JevAndClef), field: "questions"},
		{name: "dual run with a state over 13 MiB", req: func() discern.Request {
			r := stateRequest(discern.Text(strings.Repeat("a", clef.MaxRequestBytes)))
			r.Providers = discern.JevAndClef
			return r
		}(), field: "request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := ev.Evaluate(t.Context(), tt.req)
			assert.Nil(t, res)
			require.ErrorIs(t, err, discern.ErrUnsupported)
			assert.Equal(t, tt.field, testutil.ValidationError(t, err).Field)
			assert.Equal(t, 0, srv.Hits())
			assert.Equal(t, 0, jev.Calls())
		})
	}
}

func TestEvaluateAllowsMaxQuestions(t *testing.T) {
	srv := newServer(t, okReply(envelope(manyAnswers(clef.MaxQuestions))))
	res, err := testutil.Evaluate(t, serverClient(t, srv), manyQuestions(clef.MaxQuestions, discern.ClefOnly))
	require.NoError(t, err)
	assert.Equal(t, discern.Complete, res.Status)
	assert.Equal(t, 1, srv.Hits())
	obs, ok := res.Observation(discern.Clef)
	require.True(t, ok)
	assert.Len(t, obs.Answers, clef.MaxQuestions)
}

// blockingTransport waits for the request's context to end, counting attempts.
type blockingTransport struct {
	attempts atomic.Int32
	onStart  func()
}

func (b *blockingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	b.attempts.Add(1)
	testutil.Drain(r)
	if b.onStart != nil {
		b.onStart()
	}
	<-r.Context().Done()
	return nil, r.Context().Err()
}

func TestCallTimeoutAndCancellation(t *testing.T) {
	tests := []struct {
		name          string
		opts          []clef.Option
		clientTimeout time.Duration // the injected http.Client's Timeout; 0 means none
		callerLimit   time.Duration // 0 means no caller deadline
		cancel        bool          // the caller cancels once the request is in flight
		wantKind      discern.FailureKind
		wantErr       error
		wantElapsed   time.Duration
	}{
		{
			name: "configured timeout", opts: []clef.Option{clef.WithTimeout(2 * time.Second)},
			wantKind: discern.FailureTimeout, wantErr: context.DeadlineExceeded, wantElapsed: 2 * time.Second,
		},
		{name: "default timeout", wantKind: discern.FailureTimeout, wantErr: context.DeadlineExceeded, wantElapsed: 30 * time.Second},
		{
			name: "injected client timeout", clientTimeout: 2 * time.Second,
			wantKind: discern.FailureTimeout, wantErr: context.DeadlineExceeded, wantElapsed: 2 * time.Second,
		},
		{
			name: "caller deadline", callerLimit: time.Second,
			wantKind: discern.FailureDeadlineExceeded, wantErr: context.DeadlineExceeded, wantElapsed: time.Second,
		},
		{
			name: "caller deadline before client timeout", clientTimeout: 2 * time.Second, callerLimit: time.Second,
			wantKind: discern.FailureDeadlineExceeded, wantErr: context.DeadlineExceeded, wantElapsed: time.Second,
		},
		{name: "caller canceled", cancel: true, wantKind: discern.FailureCanceled, wantErr: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tt.callerLimit > 0 {
					ctx, cancel = context.WithTimeout(ctx, tt.callerLimit)
					defer cancel()
				}
				bt := &blockingTransport{}
				if tt.cancel {
					bt.onStart = cancel
				}
				hc := &http.Client{Transport: bt, Timeout: tt.clientTimeout}
				opts := append([]clef.Option{clef.WithHTTPClient(hc)}, tt.opts...)
				c, err := clef.New(testAccount, testToken, opts...)
				require.NoError(t, err)

				start := time.Now()
				res, err := testutil.Evaluator(t, discern.WithAdapter(c)).Evaluate(ctx, noulRequest())
				assert.Equal(t, tt.wantElapsed, time.Since(start))
				require.ErrorIs(t, err, tt.wantErr)
				pe := testutil.ProviderError(t, err)
				assert.Equal(t, tt.wantKind, pe.Kind)
				assert.Equal(t, discern.Clef, pe.Provider)
				assert.Equal(t, int32(1), bt.attempts.Load())
				require.NotNil(t, res)
				assert.Equal(t, discern.Failed, res.Status)
			})
		})
	}
}

func TestCallMakesSingleAttempt(t *testing.T) {
	tests := []struct {
		name string
		ct   *countingTransport
		kind discern.FailureKind
	}{
		{
			name: "rate limited",
			ct:   &countingTransport{status: http.StatusTooManyRequests, header: http.Header{"Retry-After": {"0"}}, body: capacityEnvelope},
			kind: discern.FailureRejected,
		},
		{
			name: "unavailable",
			ct:   &countingTransport{status: http.StatusServiceUnavailable, header: http.Header{"Retry-After": {"1"}}, body: `upstream unavailable`},
			kind: discern.FailureRejected,
		},
		{
			name: "success false on HTTP 200",
			ct:   &countingTransport{status: http.StatusOK, body: capacityEnvelope},
			kind: discern.FailureRejected,
		},
		{
			name: "transport error",
			ct:   &countingTransport{err: errSyntheticReset},
			kind: discern.FailureTransport,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := testutil.Evaluate(t, tt.ct.client(t), noulRequest())
			assert.Equal(t, tt.kind, testutil.ProviderError(t, err).Kind)
			assert.Equal(t, int32(1), tt.ct.attempts.Load())
		})
	}
}

func TestCallTransportErrorCause(t *testing.T) {
	ct := &countingTransport{err: errSyntheticReset}
	_, err := testutil.Evaluate(t, ct.client(t), noulRequest())
	require.ErrorIs(t, err, errSyntheticReset)
	assert.Equal(t, "discern: clef: transport", err.Error())
	var ue *url.Error
	assert.NotErrorAs(t, err, &ue)
}

// secretRequest plants sentinels in the state, instructions and descriptions.
func secretRequest() discern.Request {
	outage := discern.Noul(qOutage, discern.Text("Is production down? "+instructionSentinel))
	outage.Yes = discern.Text("Production is down. " + descriptionSentinel)
	return discern.Request{
		Providers: discern.ClefOnly,
		State:     discern.Text("Subject: Checkout down\n\n" + stateSentinel),
		Questions: []discern.Question{outage},
	}
}

func TestErrorsNeverLeakSecrets(t *testing.T) {
	echoed := "Invalid input: " + stateSentinel + " " + messageSentinel
	rejected := newServer(t, statusReply(http.StatusTooManyRequests,
		`{"result":null,"success":false,"errors":[{"code":3040,"message":"`+echoed+`"}],"messages":[]}`))
	refusedOn200 := newServer(t, okReply(`{"result":null,"success":false,"errors":[{"code":5006,"message":"`+echoed+`"}],"messages":[]}`))
	malformedBody := newServer(t, okReply(envelope(`{"echo":"`+stateSentinel+`","answers":{"outage":"`+stateSentinel+`"}}`)))

	tests := []struct {
		name     string
		opts     []clef.Option
		kind     discern.FailureKind
		messages []string
	}{
		{
			name: "rejected", kind: discern.FailureRejected, messages: []string{echoed},
			opts: []clef.Option{clef.WithTestEndpoint(rejected.URL()), clef.WithHTTPClient(rejected.Client())},
		},
		{
			name: "rejected on HTTP 200", kind: discern.FailureRejected, messages: []string{echoed},
			opts: []clef.Option{clef.WithTestEndpoint(refusedOn200.URL()), clef.WithHTTPClient(refusedOn200.Client())},
		},
		{
			name: "malformed", kind: discern.FailureMalformed,
			opts: []clef.Option{clef.WithTestEndpoint(malformedBody.URL()), clef.WithHTTPClient(malformedBody.Client())},
		},
		{
			// The real default client against a closed loopback port.
			name: "transport", kind: discern.FailureTransport,
			opts: []clef.Option{clef.WithTestEndpoint("https://127.0.0.1:1"), clef.WithTimeout(10 * time.Second)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := clef.New(testAccount, sentinelToken, tt.opts...)
			require.NoError(t, err)
			res, err := testutil.Evaluate(t, c, secretRequest())
			pe := testutil.ProviderError(t, err)
			assert.Equal(t, tt.kind, pe.Kind)
			assert.Equal(t, tt.messages, pe.ProviderMessages())
			require.NotNil(t, res)
			record, mErr := json.Marshal(res)
			require.NoError(t, mErr)

			// Formatting the Client itself is covered verb by verb in
			// TestClientFormattingNeverLeaksSecrets.
			outputs := []struct{ label, out string }{
				{label: "error", out: err.Error()},
				{label: "fmt", out: fmt.Sprintf("%v %+v %#v %s %q", err, err, err, err, err)},
				{label: "go", out: pe.GoString()},
				{label: "record", out: string(record)},
			}
			for _, o := range outputs {
				for _, secret := range []string{
					sentinelToken, testAccount, "Bearer", "/client/v4", "@cf/cloudflare", "127.0.0.1", "https://",
					stateSentinel, instructionSentinel, descriptionSentinel, messageSentinel,
				} {
					assert.NotContains(t, o.out, secret, o.label)
				}
			}
			// The cause chain may name a socket address, but never the URL.
			cause := fmt.Sprint(errors.Unwrap(pe))
			for _, secret := range []string{sentinelToken, testAccount, "/client/v4", "@cf/cloudflare", stateSentinel, messageSentinel} {
				assert.NotContains(t, cause, secret)
			}
		})
	}
}

func TestInjectedClientIsNotModified(t *testing.T) {
	rep := okReply(envelope(outageResult))
	rep.header.Set("Set-Cookie", "session=synthetic; Path=/")
	srv := newServer(t, rep)
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	transport := srv.Client().Transport
	hc := &http.Client{Transport: transport, Jar: jar, Timeout: 7 * time.Second}

	c, err := clef.New(testAccount, testToken, clef.WithTestEndpoint(srv.URL()), clef.WithHTTPClient(hc))
	require.NoError(t, err)
	res, err := testutil.Evaluate(t, c, noulRequest())
	require.NoError(t, err)
	assert.Equal(t, discern.Complete, res.Status)

	assert.Nil(t, hc.CheckRedirect)
	assert.Same(t, jar, hc.Jar)
	assert.Equal(t, 7*time.Second, hc.Timeout)
	assert.Same(t, transport, hc.Transport)
	u, err := url.Parse(srv.URL())
	require.NoError(t, err)
	assert.Empty(t, jar.Cookies(u))
}

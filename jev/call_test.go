package jev_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/testutil"
	"github.com/mrz1836/go-discern/jev"
)

func TestCallSendsExactRequest(t *testing.T) {
	s := testutil.NewServer(t, reply(http.StatusOK, ticketResponse))
	_, err := testutil.Evaluate(t, serverClient(t, s), ticketRequest())
	require.NoError(t, err)

	reqs := s.Requests()
	require.Len(t, reqs, 1)
	r := reqs[0]
	assert.Equal(t, http.MethodPost, r.Method)
	assert.Equal(t, systemOnePath, r.URL, "the path, with no query")
	assert.Equal(t, []string{"Bearer test-key"}, r.Header.Values("Authorization"))
	assert.Equal(t, []string{"application/json"}, r.Header.Values("Content-Type"))
	assert.Equal(t, []string{"go-discern"}, r.Header.Values("User-Agent"))
	assert.Empty(t, r.Header.Values("Idempotency-Key"))
	assert.Empty(t, r.Header.Values("Cookie"))
	assert.Equal(t, ticketWire, string(r.Body)) //nolint:testifylint // byte-exact: JSONEq would ignore the key order under test
	assert.Equal(t, 1, s.Hits())
}

func TestCallSendsStateVerbatim(t *testing.T) {
	const questions = `"questions":{"outage":{"type":"noul","instructions":"Is production down?"}}}`
	tests := []struct {
		name  string
		state discern.Value
		want  string
	}{
		{
			name:  "JSON state keeps a 20-digit integer and key order",
			state: discern.JSON([]byte(`{"ticket_id":12345678901234567890,"queue":"billing","tags":["refund"]}`)),
			want:  `{"ticket_id":12345678901234567890,"queue":"billing","tags":["refund"]}`,
		},
		{
			name:  "text state does not escape HTML characters",
			state: discern.Text("Refund <pending> & overdue"),
			want:  `"Refund <pending> & overdue"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The in-memory transport runs on the calling goroutine, so it can
			// record the body without synchronization.
			var body []byte
			transport := &testutil.CountingTransport{Next: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					return nil, err
				}
				body = b
				return respond(http.StatusOK, `{"answers":{"outage":{"type":"noul","noul":0.02}}}`)(r)
			})}
			req := discern.Request{
				Providers: discern.JevOnly,
				State:     tt.state,
				Questions: []discern.Question{discern.Noul(qOutage, discern.Text("Is production down?"))},
			}
			_, err := testutil.Evaluate(t, inMemory(t, transport), req)
			require.NoError(t, err)
			assert.Equal(t, 1, transport.Attempts())
			assert.Equal(t, `{"model":"jev-1.13.0","state":`+tt.want+`,`+questions, string(body))
			assert.NotContains(t, string(body), `"state":"{`)
			assert.NotContains(t, string(body), `\u003c`)
			assert.NotContains(t, string(body), `\u0026`)
		})
	}
}

func TestCallDecodesSuccess(t *testing.T) {
	s := testutil.NewServer(t, reply(http.StatusOK, ticketResponse))
	res, err := testutil.Evaluate(t, serverClient(t, s), ticketRequest())
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.Equal(t, discern.Complete, res.Status)
	require.Len(t, res.Outcomes, 1)
	out := res.Outcomes[0]
	assert.Equal(t, discern.Jev, out.Provider)
	assert.Equal(t, pinnedModel, out.RequestedModel)
	assert.Equal(t, discern.OutcomeSucceeded, out.Status)
	assert.Nil(t, out.Failure)
	assert.Equal(t, ticketObservation(testRequestID), out.Observation)
	assert.Equal(t, 1, s.Hits())
}

func TestCallKeepsAbsentFieldsAbsent(t *testing.T) {
	s := testutil.NewServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sparseResponse))
	})
	res, err := testutil.Evaluate(t, serverClient(t, s), ticketRequest())
	require.NoError(t, err)

	obs, ok := res.Observation(discern.Jev)
	require.True(t, ok)
	assert.Empty(t, obs.Model)
	assert.Empty(t, obs.RequestID)
	assert.Nil(t, obs.Usage)
	queue, ok := obs.Choice(qQueue)
	require.True(t, ok)
	assert.Equal(t, optBilling, queue.Option)
	assert.Nil(t, queue.Confidence)
	severity, ok := obs.Score(qSeverity)
	require.True(t, ok)
	assert.Nil(t, severity.Confidence)
	require.Len(t, res.Outcomes, 1)
	assert.Equal(t, pinnedModel, res.Outcomes[0].RequestedModel)
}

func TestCallRecordsRequestedAndReturnedModel(t *testing.T) {
	s := testutil.NewServer(t, reply(http.StatusOK, ticketResponse))
	c, err := jev.New(testKey, jev.WithTestEndpoint(s.URL()), jev.WithHTTPClient(s.Client()))
	require.NoError(t, err)

	res, err := testutil.Evaluate(t, c, ticketRequest())
	require.NoError(t, err)
	require.Len(t, res.Outcomes, 1)
	out := res.Outcomes[0]
	assert.Equal(t, jev.DefaultModel, out.RequestedModel)
	require.NotNil(t, out.Observation)
	assert.Equal(t, pinnedModel, out.Observation.Model)

	reqs := s.Requests()
	require.Len(t, reqs, 1)
	assert.True(t, strings.HasPrefix(string(reqs[0].Body), `{"model":"jev-latest",`), "body %s", reqs[0].Body)
}

func TestCallRejectsNon2xx(t *testing.T) {
	const errorBody = `{"error":{"type":"rate_limit_error","code":4029,"message":"Too many requests"}}`
	tests := []struct {
		name      string
		status    int
		headers   []string
		wantRetry time.Duration
		wantHas   bool
	}{
		{name: "401 unauthorized", status: http.StatusUnauthorized},
		{name: "403 forbidden", status: http.StatusForbidden},
		{name: "422 unprocessable", status: http.StatusUnprocessableEntity},
		{name: "429 with Retry-After", status: http.StatusTooManyRequests, headers: []string{"Retry-After", "2"}, wantRetry: 2 * time.Second, wantHas: true},
		{name: "429 retry-after-ms wins", status: http.StatusTooManyRequests, headers: []string{"retry-after-ms", "1500", "Retry-After", "9"}, wantRetry: 1500 * time.Millisecond, wantHas: true},
		{name: "429 fractional retry-after-ms rounds up", status: http.StatusTooManyRequests, headers: []string{"retry-after-ms", "1500.25", "Retry-After", "9"}, wantRetry: 1501 * time.Millisecond, wantHas: true},
		{name: "500 server error", status: http.StatusInternalServerError},
		{name: "529 overloaded", status: 529},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testutil.NewServer(t, reply(tt.status, errorBody, tt.headers...))
			res, err := testutil.Evaluate(t, serverClient(t, s), ticketRequest())
			require.ErrorIs(t, err, discern.ErrRejected)
			assert.Equal(t, 1, s.Hits())

			pe := testutil.ProviderError(t, err)
			assert.Equal(t, discern.Jev, pe.Provider)
			assert.Equal(t, discern.FailureRejected, pe.Kind)
			assert.Equal(t, tt.status, pe.StatusCode)
			assert.Equal(t, testRequestID, pe.RequestID)
			assert.Equal(t, tt.wantRetry, pe.RetryAfter)
			assert.Equal(t, tt.wantHas, pe.HasRetryAfter)
			assert.Nil(t, pe.Codes)
			assert.Empty(t, pe.ProviderMessages())

			f := testutil.Failure(t, res)
			assert.Equal(t, discern.FailureRejected, f.Kind)
			assert.Equal(t, tt.status, f.StatusCode)
			assert.Equal(t, testRequestID, f.RequestID)
			if tt.wantHas {
				assert.Equal(t, new(tt.wantRetry), f.RetryAfter)
			} else {
				assert.Nil(t, f.RetryAfter)
			}
		})
	}
}

func TestCallReadsErrorDetailIntoProviderMessages(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		headers   []string
		messages  []string
		wantRetry time.Duration
	}{
		{
			name: "observed unknown model", status: http.StatusBadRequest,
			body:     `{"detail":{"error_type":"api_usage_error","message":"Unknown model: jev"}}`,
			messages: []string{"api_usage_error: Unknown model: jev"},
		},
		{
			name: "observed oversized state", status: http.StatusBadRequest,
			body:     `{"detail":{"error_type":"max_tokens_exceeded"}}`,
			messages: []string{"max_tokens_exceeded"},
		},
		{
			name: "validation errors", status: http.StatusUnprocessableEntity,
			body:     `{"detail":[{"loc":["body","model"],"msg":"Field required","type":"missing"}]}`,
			messages: []string{"missing: Field required"},
		},
		{
			name: "rate limit keeps its retry hint", status: http.StatusTooManyRequests,
			body: `{"detail":"Rate limit exceeded"}`, headers: []string{"Retry-After", "3"},
			messages: []string{"Rate limit exceeded"}, wantRetry: 3 * time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testutil.NewServer(t, reply(tt.status, tt.body, tt.headers...))
			res, err := testutil.Evaluate(t, serverClient(t, s), ticketRequest())
			require.ErrorIs(t, err, discern.ErrRejected)
			assert.Equal(t, fmt.Sprintf("discern: jev: rejected (HTTP %d)", tt.status), err.Error())

			pe := testutil.ProviderError(t, err)
			assert.Equal(t, tt.messages, pe.ProviderMessages())
			assert.Nil(t, pe.Codes)
			assert.Equal(t, tt.wantRetry, pe.RetryAfter)
			// Messages may echo request content: they stay out of every printed
			// form of the error and out of the record.
			for _, v := range []any{err, pe, *pe, res} {
				testutil.AssertNeverPrints(t, v, tt.messages...)
			}
			record, merr := json.Marshal(res)
			require.NoError(t, merr)
			testutil.AssertRedacted(t, string(record), tt.messages)
		})
	}
}

func TestCallRetryAfterHTTPDate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		at := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
		c := inMemory(t, respond(http.StatusServiceUnavailable, `{}`, "Retry-After", at))
		_, err := testutil.Evaluate(t, c, ticketRequest())
		require.ErrorIs(t, err, discern.ErrRejected)
		pe := testutil.ProviderError(t, err)
		assert.Equal(t, http.StatusServiceUnavailable, pe.StatusCode)
		assert.True(t, pe.HasRetryAfter)
		assert.Equal(t, 90*time.Second, pe.RetryAfter)
	})
}

func TestCallDoesNotFollowRedirects(t *testing.T) {
	target := testutil.NewServer(t, reply(http.StatusOK, ticketResponse))
	origin := testutil.NewServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL()+systemOnePath, http.StatusFound)
	})

	res, err := testutil.Evaluate(t, serverClient(t, origin), ticketRequest())
	require.ErrorIs(t, err, discern.ErrRejected)
	pe := testutil.ProviderError(t, err)
	assert.Equal(t, http.StatusFound, pe.StatusCode)
	assert.Equal(t, http.StatusFound, testutil.Failure(t, res).StatusCode)
	assert.Equal(t, 1, origin.Hits())
	assert.Equal(t, 0, target.Hits())

	// Control: a client that follows redirects does reach the target, so the zero
	// above is not an artifact of the test setup.
	ctrl, err := http.NewRequestWithContext(t.Context(), http.MethodPost, origin.URL()+systemOnePath, strings.NewReader(`{}`))
	require.NoError(t, err)
	resp, err := origin.Client().Do(ctrl)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, target.Hits())
	for _, r := range target.Requests() {
		assert.Empty(t, r.Header.Values("Authorization"))
	}
}

func TestCallRejectsMalformedResponse(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "empty body", body: ``, wantStatus: http.StatusOK},
		{name: "invalid JSON", body: `<html>Bad Gateway</html>`, wantStatus: http.StatusOK},
		{name: "truncated JSON", body: ticketResponse[:len(ticketResponse)/2], wantStatus: http.StatusOK},
		{name: "missing answers", body: `{"model":"jev-1.13.0","usage":{"input_tokens":304,"output_tokens":0}}`, wantStatus: http.StatusOK},
		{name: "extra answer ID", body: strings.Replace(ticketResponse, `"outage":`, `"priority":{"type":"noul","noul":0.5},"outage":`, 1), wantStatus: http.StatusOK},
		{name: "case-variant answers key", body: strings.Replace(ticketResponse, `"answers":`, `"Answers":`, 1), wantStatus: http.StatusOK},
		{name: "type mismatch", body: strings.Replace(ticketResponse, `"noul":0.07`, `"noul":"0.07"`, 1), wantStatus: http.StatusOK},
		{name: "probability above one", body: strings.Replace(ticketResponse, `"billing":0.91`, `"billing":1.5`, 1), wantStatus: http.StatusOK},
		{name: "model with a space", body: strings.Replace(ticketResponse, `"model":"jev-1.13.0"`, `"model":"jev 1.13.0"`, 1), wantStatus: http.StatusOK},
		{name: "model too long", body: strings.Replace(ticketResponse, `"model":"jev-1.13.0"`, `"model":"`+strings.Repeat("j", 257)+`"`, 1), wantStatus: http.StatusOK},
		// Decoding succeeds, but discern's own validation rejects the sum; the
		// request ID still reaches the error, and the status is not known there.
		{name: "probabilities do not sum to one", body: strings.Replace(ticketResponse, `"billing":0.91`, `"billing":0.5`, 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotEqual(t, ticketResponse, tt.body)
			s := testutil.NewServer(t, reply(http.StatusOK, tt.body))
			res, err := testutil.Evaluate(t, serverClient(t, s), ticketRequest())
			require.ErrorIs(t, err, discern.ErrMalformedResponse)
			assert.Equal(t, 1, s.Hits())

			pe := testutil.ProviderError(t, err)
			assert.Equal(t, discern.FailureMalformed, pe.Kind)
			assert.Equal(t, testRequestID, pe.RequestID)
			assert.Equal(t, tt.wantStatus, pe.StatusCode)

			f := testutil.Failure(t, res)
			assert.Equal(t, discern.FailureMalformed, f.Kind)
			assert.Equal(t, testRequestID, f.RequestID)
		})
	}
}

func TestCallRejectsOversizedResponse(t *testing.T) {
	require.Greater(t, len(ticketResponse), 256)
	s := testutil.NewServer(t, reply(http.StatusOK, ticketResponse))
	res, err := testutil.Evaluate(t, serverClient(t, s, jev.WithMaxResponseBytes(256)), ticketRequest())
	require.ErrorIs(t, err, discern.ErrMalformedResponse)
	pe := testutil.ProviderError(t, err)
	assert.Equal(t, discern.FailureTooLarge, pe.Kind)
	assert.Equal(t, http.StatusOK, pe.StatusCode)
	assert.Equal(t, testRequestID, pe.RequestID)
	assert.Equal(t, discern.FailureTooLarge, testutil.Failure(t, res).Kind)
	assert.Equal(t, 1, s.Hits())
}

func TestCallOversizedRefusalKeepsTheRetryHint(t *testing.T) {
	body := strings.Repeat(" ", 2048)
	c := inMemory(t, respond(http.StatusTooManyRequests, body, "Retry-After", "30"), jev.WithMaxResponseBytes(1024))
	res, err := testutil.Evaluate(t, c, ticketRequest())
	require.ErrorIs(t, err, discern.ErrMalformedResponse)
	pe := testutil.ProviderError(t, err)
	assert.Equal(t, discern.FailureTooLarge, pe.Kind)
	assert.Equal(t, http.StatusTooManyRequests, pe.StatusCode)
	assert.True(t, pe.HasRetryAfter)
	assert.Equal(t, 30*time.Second, pe.RetryAfter)
	assert.Equal(t, new(30*time.Second), testutil.Failure(t, res).RetryAfter)
}

func TestCallDropsInvalidRequestIDs(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{name: "valid", id: "req-01HX7Q2K9M", want: "req-01HX7Q2K9M"},
		{name: "longest", id: strings.Repeat("r", 256), want: strings.Repeat("r", 256)},
		{name: "space", id: "req 1"},
		{name: "too long", id: strings.Repeat("r", 257)},
		{name: "non-ASCII", id: "réq-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := inMemory(t, respond(http.StatusOK, ticketResponse, requestIDHeader, tt.id))
			req := ticketRequest()
			call, err := c.Prepare(&req)
			require.NoError(t, err)
			obs, err := call(t.Context())
			require.NoError(t, err)
			assert.Equal(t, tt.want, obs.RequestID)

			c = inMemory(t, respond(http.StatusServiceUnavailable, `{}`, requestIDHeader, tt.id))
			call, err = c.Prepare(&req)
			require.NoError(t, err)
			_, err = call(t.Context())
			assert.Equal(t, tt.want, testutil.ProviderError(t, err).RequestID)
		})
	}
}

func TestCallTimeoutAndCancellation(t *testing.T) {
	tests := []struct {
		name          string
		timeout       time.Duration
		clientTimeout time.Duration // the injected http.Client's Timeout; 0 means none
		callerLimit   time.Duration // 0 means no caller deadline
		cancel        bool          // the caller cancels once the request is in flight
		wantKind      discern.FailureKind
		wantErr       error
		wantElapsed   time.Duration
	}{
		{name: "adapter timeout", timeout: 2 * time.Second, wantKind: discern.FailureTimeout, wantErr: context.DeadlineExceeded, wantElapsed: 2 * time.Second},
		{name: "injected client timeout", timeout: 30 * time.Second, clientTimeout: 2 * time.Second, wantKind: discern.FailureTimeout, wantErr: context.DeadlineExceeded, wantElapsed: 2 * time.Second},
		{name: "caller deadline", timeout: 30 * time.Second, callerLimit: time.Second, wantKind: discern.FailureDeadlineExceeded, wantErr: context.DeadlineExceeded, wantElapsed: time.Second},
		{name: "caller deadline before client timeout", timeout: 30 * time.Second, clientTimeout: 2 * time.Second, callerLimit: time.Second, wantKind: discern.FailureDeadlineExceeded, wantErr: context.DeadlineExceeded, wantElapsed: time.Second},
		{name: "caller canceled", timeout: 30 * time.Second, cancel: true, wantKind: discern.FailureCanceled, wantErr: context.Canceled},
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
				transport := &testutil.CountingTransport{Next: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					if tt.cancel {
						cancel()
					}
					return testutil.BlockUntilDone(r)
				})}
				opts := []jev.Option{jev.WithTimeout(tt.timeout)}
				if tt.clientTimeout > 0 {
					opts = append(opts, jev.WithHTTPClient(&http.Client{Transport: transport, Timeout: tt.clientTimeout}))
				}
				ev := testutil.Evaluator(t, discern.WithAdapter(inMemory(t, transport, opts...)))

				start := time.Now()
				res, err := ev.Evaluate(ctx, ticketRequest())
				assert.Equal(t, tt.wantElapsed, time.Since(start))
				require.ErrorIs(t, err, tt.wantErr)
				pe := testutil.ProviderError(t, err)
				assert.Equal(t, tt.wantKind, pe.Kind)
				assert.Equal(t, discern.Jev, pe.Provider)
				assert.Equal(t, tt.wantKind, testutil.Failure(t, res).Kind)
				assert.Equal(t, 1, transport.Attempts())
			})
		})
	}
}

func TestCallMakesOneAttempt(t *testing.T) {
	tests := []struct {
		name     string
		next     http.RoundTripper
		wantKind discern.FailureKind
	}{
		{name: "timeout", next: testutil.RoundTripFunc(testutil.BlockUntilDone), wantKind: discern.FailureTimeout},
		{name: "429", next: respond(http.StatusTooManyRequests, `{}`, "Retry-After", "0"), wantKind: discern.FailureRejected},
		{name: "503", next: respond(http.StatusServiceUnavailable, `{}`), wantKind: discern.FailureRejected},
		{name: "transport error", next: testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errConnectionReset }), wantKind: discern.FailureTransport},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				transport := &testutil.CountingTransport{Next: tt.next}
				res, err := testutil.Evaluate(t, inMemory(t, transport, jev.WithTimeout(time.Second)), ticketRequest())
				require.Error(t, err)
				assert.Equal(t, tt.wantKind, testutil.ProviderError(t, err).Kind)
				assert.Equal(t, tt.wantKind, testutil.Failure(t, res).Kind)
				assert.Equal(t, 1, transport.Attempts())
			})
		})
	}
}

func TestSecretsNeverLeak(t *testing.T) {
	// Formatting a Client is covered verb by verb in
	// TestClientFormattingNeverLeaksSecrets; this test covers what a call
	// produces: its error and the stored result record.
	echo := `{"error":"bad key ` + sentinelKey + ` for ` + stateSentinel + ` / ` + instructionSentinel + `"}`
	tests := []struct {
		name    string
		rt      http.RoundTripper
		wantErr bool
	}{
		{name: "transport error", rt: testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errConnectionReset }), wantErr: true},
		{name: "rejected with echoing body", rt: respond(http.StatusUnauthorized, echo), wantErr: true},
		{name: "malformed echoing body", rt: respond(http.StatusOK, `{"answers":{"queue":"`+stateSentinel+`"}}`), wantErr: true},
		{name: "success", rt: respond(http.StatusOK, ticketResponse)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := jev.New(sentinelKey, jev.WithModel(pinnedModel), jev.WithHTTPClient(&http.Client{Transport: tt.rt}))
			require.NoError(t, err)
			req := ticketRequest()
			req.State = discern.Text("Ticket body " + stateSentinel)
			req.Questions[0].Instructions = discern.Text("Route this ticket " + instructionSentinel)

			res, err := testutil.Evaluate(t, c, req)
			require.NotNil(t, res)
			record, jerr := json.Marshal(res)
			require.NoError(t, jerr)
			printed := []string{string(record)}
			if tt.wantErr {
				require.Error(t, err)
				printed = append(printed, err.Error(), fmt.Sprintf("%v %+v %#v %s %q %x", err, err, err, err, err, err))
			} else {
				require.NoError(t, err)
			}
			for _, s := range printed {
				for _, secret := range []string{sentinelKey, stateSentinel, instructionSentinel, "SENTINEL", "Bearer", "api.typesafe.ai", systemOnePath} {
					assert.NotContains(t, s, secret)
				}
			}
		})
	}
}

func TestTransportErrorKeepsCauseOutOfMessage(t *testing.T) {
	c := inMemory(t, testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errConnectionReset }))
	_, err := testutil.Evaluate(t, c, ticketRequest())
	require.ErrorIs(t, err, errConnectionReset)
	pe := testutil.ProviderError(t, err)
	assert.Equal(t, discern.FailureTransport, pe.Kind)
	assert.Equal(t, "discern: jev: transport", err.Error())
}

func TestInjectedClientIsNotModified(t *testing.T) {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	transport := &testutil.CountingTransport{Next: respond(http.StatusOK, ticketResponse, "Set-Cookie", "session=abc; Path=/; Secure")}
	hc := &http.Client{Transport: transport, Jar: jar, Timeout: 45 * time.Second}

	c, err := jev.New(testKey, jev.WithModel(pinnedModel), jev.WithHTTPClient(hc))
	require.NoError(t, err)
	_, err = testutil.Evaluate(t, c, ticketRequest())
	require.NoError(t, err)

	assert.Equal(t, 1, transport.Attempts())
	assert.Nil(t, hc.CheckRedirect)
	assert.Same(t, jar, hc.Jar)
	assert.Same(t, transport, hc.Transport)
	assert.Equal(t, 45*time.Second, hc.Timeout)
	u, err := url.Parse("https://api.typesafe.ai" + systemOnePath)
	require.NoError(t, err)
	assert.Empty(t, jar.Cookies(u))
}

func TestConcurrentEvaluate(t *testing.T) {
	const workers = 16
	s := testutil.NewServer(t, reply(http.StatusOK, ticketResponse))
	ev := testutil.Evaluator(t, discern.WithAdapter(serverClient(t, s)))

	var (
		wg       sync.WaitGroup
		errs     = make([]error, workers)
		statuses = make([]discern.ResultStatus, workers)
	)
	for i := range workers {
		wg.Go(func() {
			res, err := ev.Evaluate(t.Context(), ticketRequest())
			errs[i] = err
			if res != nil {
				statuses[i] = res.Status
			}
		})
	}
	wg.Wait()

	for i := range workers {
		require.NoError(t, errs[i], "worker %d", i)
		assert.Equal(t, discern.Complete, statuses[i], "worker %d", i)
	}
	assert.Equal(t, workers, s.Hits())
}

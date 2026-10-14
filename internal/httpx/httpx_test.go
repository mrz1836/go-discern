package httpx_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/httpx"
	"github.com/mrz1836/go-discern/internal/testutil"
)

var (
	errReset = errors.New("connection reset by peer")
	errClose = errors.New("close failed")
)

const (
	testBody    = `{"model":"jev-1","state":"refund <now> & later","questions":{}}`
	testAccount = "0123456789abcdef0123456789abcdef"

	// requestIDName is the request ID header of baseRequest.
	requestIDName = "X-Request-Id"
)

// trackingBody records reads and closes. When done is set, Read stalls until it
// is closed and then fails with doneErr.
type trackingBody struct {
	r        io.Reader
	readErr  error
	closeErr error
	done     <-chan struct{}
	doneErr  func() error
	reads    atomic.Int32
	closes   atomic.Int32
}

// stalledBody returns a body whose reads block until ctx is done.
func stalledBody(ctx context.Context) *trackingBody {
	return &trackingBody{done: ctx.Done(), doneErr: ctx.Err}
}

func (b *trackingBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	if b.done != nil {
		<-b.done
		return 0, b.doneErr()
	}
	n, err := b.r.Read(p)
	if errors.Is(err, io.EOF) && b.readErr != nil {
		return n, b.readErr
	}
	return n, err
}

func (b *trackingBody) Close() error {
	b.closes.Add(1)
	return b.closeErr
}

func newBody(s string) *trackingBody { return &trackingBody{r: strings.NewReader(s)} }

// fakeClient returns a client whose transport answers every request with resp
// built from the request.
func fakeClient(fn func(*http.Request) (*http.Response, error)) *http.Client {
	return httpx.NewClient(&http.Client{Transport: testutil.RoundTripFunc(fn)})
}

func response(r *http.Request, status int, body io.ReadCloser, contentLength int64) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Header:        http.Header{},
		Body:          body,
		ContentLength: contentLength,
		Request:       r,
	}
}

func baseRequest(url string) httpx.Request {
	return httpx.Request{
		Provider:         discern.Jev,
		URL:              url,
		Authorization:    "Bearer test-key",
		Body:             []byte(testBody),
		Timeout:          5 * time.Second,
		MaxBytes:         1024,
		RequestIDHeaders: []string{requestIDName, "Cf-Ai-Req-Id"},
		RayIDHeader:      "Cf-Ray",
	}
}

func TestDoSendsExactRequest(t *testing.T) {
	srv := testutil.NewServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cf-Ai-Req-Id", "req-123")
		w.Header().Set("Cf-Ray", "ray-456")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"answers":{}}`)
	})

	sent := baseRequest(srv.URL() + "/v1/systemone")
	resp, err := httpx.Do(t.Context(), httpx.NewClient(srv.Client()), sent)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.JSONEq(t, `{"answers":{}}`, string(resp.Body))
	assert.Equal(t, "req-123", resp.RequestID)
	assert.Equal(t, "ray-456", resp.RayID)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	got := srv.Requests()
	require.Len(t, got, 1)
	r := got[0]
	assert.Equal(t, http.MethodPost, r.Method)
	assert.Equal(t, "/v1/systemone", r.URL)
	assert.Equal(t, []string{"Bearer test-key"}, r.Header.Values("Authorization"))
	assert.Equal(t, []string{"application/json"}, r.Header.Values("Content-Type"))
	assert.Equal(t, []string{"application/json"}, r.Header.Values("Accept"))
	assert.Equal(t, []string{httpx.UserAgent}, r.Header.Values("User-Agent"))
	assert.Empty(t, r.Header.Values("Idempotency-Key"))
	assert.Empty(t, r.Header.Values("X-Idempotency-Key"))
	assert.Equal(t, sent.Body, r.Body)
}

func TestDoOmitsEmptyAuthorization(t *testing.T) {
	var sawAuth atomic.Bool
	hc := fakeClient(func(r *http.Request) (*http.Response, error) {
		_, ok := r.Header["Authorization"]
		sawAuth.Store(ok)
		return response(r, http.StatusOK, newBody(`{}`), -1), nil
	})
	req := baseRequest("https://127.0.0.1/v1/systemone")
	req.Authorization = ""
	_, err := httpx.Do(t.Context(), hc, req)
	require.NoError(t, err)
	assert.False(t, sawAuth.Load())
}

func TestDoRequestIDHeaders(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		names   []string
		ray     string
		wantID  string
		wantRay string
	}{
		{name: "first header wins", headers: map[string]string{requestIDName: "a", "Cf-Ai-Req-Id": "b"}, names: []string{requestIDName, "Cf-Ai-Req-Id"}, wantID: "a"},
		{name: "falls through empty", headers: map[string]string{requestIDName: "", "Cf-Ai-Req-Id": "b"}, names: []string{requestIDName, "Cf-Ai-Req-Id"}, wantID: "b"},
		{name: "case-insensitive names", headers: map[string]string{requestIDName: "a"}, names: []string{"x-request-id"}, wantID: "a"},
		{name: "none configured", headers: map[string]string{requestIDName: "a"}},
		{name: "ray header", headers: map[string]string{"Cf-Ray": "8a1b-SJC"}, ray: "cf-ray", wantRay: "8a1b-SJC"},
		{name: "ray header absent", ray: "Cf-Ray"},
		{name: "invalid first header falls through", headers: map[string]string{requestIDName: "has space", "Cf-Ai-Req-Id": "b"}, names: []string{requestIDName, "Cf-Ai-Req-Id"}, wantID: "b"},
		{name: "longest ID kept", headers: map[string]string{requestIDName: strings.Repeat("r", 256)}, names: []string{requestIDName}, wantID: strings.Repeat("r", 256)},
		{name: "overlong ID dropped", headers: map[string]string{requestIDName: strings.Repeat("r", 257)}, names: []string{requestIDName}},
		{name: "control character dropped", headers: map[string]string{requestIDName: "req\x01"}, names: []string{requestIDName}},
		{name: "non-ASCII dropped", headers: map[string]string{requestIDName: "réq"}, names: []string{requestIDName}},
		{name: "invalid ray dropped", headers: map[string]string{"Cf-Ray": "8a1b SJC"}, ray: "Cf-Ray"},
		{name: "overlong ray dropped", headers: map[string]string{"Cf-Ray": strings.Repeat("r", 257)}, ray: "Cf-Ray"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hc := fakeClient(func(r *http.Request) (*http.Response, error) {
				resp := response(r, http.StatusOK, newBody(`{}`), 2)
				for k, v := range tt.headers {
					resp.Header.Set(k, v)
				}
				return resp, nil
			})
			req := baseRequest("https://127.0.0.1/v1/systemone")
			req.RequestIDHeaders, req.RayIDHeader = tt.names, tt.ray
			resp, err := httpx.Do(t.Context(), hc, req)
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, resp.RequestID)
			assert.Equal(t, tt.wantRay, resp.RayID)
		})
	}
}

func TestDoDoesNotFollowRedirects(t *testing.T) {
	for _, status := range []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			target := testutil.NewServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			origin := testutil.NewServer(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL()+"/stolen", status)
			})

			var body *trackingBody
			inner := origin.Client().Transport
			base := &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				resp, err := inner.RoundTrip(r)
				if err != nil {
					return nil, err
				}
				body = &trackingBody{r: resp.Body}
				resp.Body = body
				return resp, nil
			})}

			resp, err := httpx.Do(t.Context(), httpx.NewClient(base), baseRequest(origin.URL()+"/v1/systemone"))
			require.NoError(t, err)
			assert.Equal(t, status, resp.StatusCode)
			assert.Equal(t, target.URL()+"/stolen", resp.Header.Get("Location"))
			assert.Zero(t, target.Hits())
			for _, r := range target.Requests() {
				assert.Empty(t, r.Header.Get("Authorization"))
			}
			require.NotNil(t, body)
			assert.Equal(t, int32(1), body.closes.Load())
		})
	}
}

func TestNewClientCopiesWithoutMutating(t *testing.T) {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	transport := &testutil.CountingTransport{Next: http.DefaultTransport}
	original := &http.Client{Transport: transport, Jar: jar, Timeout: 7 * time.Second}

	c := httpx.NewClient(original)
	require.NotSame(t, original, c)
	assert.Nil(t, original.CheckRedirect)
	assert.Same(t, jar, original.Jar)
	assert.Equal(t, 7*time.Second, original.Timeout)

	assert.Nil(t, c.Jar)
	assert.Equal(t, 7*time.Second, c.Timeout)
	got, ok := c.Transport.(*testutil.CountingTransport)
	require.True(t, ok)
	assert.Same(t, transport, got)
	require.NotNil(t, c.CheckRedirect)
	require.ErrorIs(t, c.CheckRedirect(nil, nil), http.ErrUseLastResponse)
}

func TestNewClientNil(t *testing.T) {
	c := httpx.NewClient(nil)
	require.NotNil(t, c)
	assert.Equal(t, http.DefaultTransport, c.Transport)
	assert.Nil(t, c.Jar)
	assert.Zero(t, c.Timeout)
	require.NotNil(t, c.CheckRedirect)
	require.ErrorIs(t, c.CheckRedirect(nil, nil), http.ErrUseLastResponse)
}

func TestDoClosesBodyOnEveryPath(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          *trackingBody
		contentLength int64
		wantErr       bool
		wantKind      discern.FailureKind
		wantReads     bool
	}{
		{name: "success", status: http.StatusOK, body: newBody(`{"answers":{}}`), contentLength: -1, wantReads: true},
		{name: "non-2xx", status: http.StatusTooManyRequests, body: newBody(`{"error":"slow down"}`), contentLength: -1, wantReads: true},
		{name: "redirect", status: http.StatusFound, body: newBody(`moved`), contentLength: 5, wantReads: true},
		{name: "overflow by content length", status: http.StatusOK, body: newBody(`{}`), contentLength: 1 << 40, wantErr: true, wantKind: discern.FailureTooLarge},
		{name: "overflow while reading", status: http.StatusOK, body: newBody(strings.Repeat("a", 1025)), contentLength: -1, wantErr: true, wantKind: discern.FailureTooLarge, wantReads: true},
		{name: "read error", status: http.StatusOK, body: &trackingBody{r: strings.NewReader(`{"ans`), readErr: errReset}, contentLength: -1, wantErr: true, wantKind: discern.FailureTransport, wantReads: true},
		{name: "read error with declared length", status: http.StatusOK, body: &trackingBody{r: strings.NewReader(`{"ans`), readErr: errReset}, contentLength: 14, wantErr: true, wantKind: discern.FailureTransport, wantReads: true},
		{name: "close error after full read", status: http.StatusOK, body: &trackingBody{r: strings.NewReader(`{}`), closeErr: errClose}, contentLength: 2, wantReads: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &testutil.CountingTransport{Next: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				resp := response(r, tt.status, tt.body, tt.contentLength)
				resp.Header.Set(requestIDName, "req-1")
				resp.Header.Set("Cf-Ray", "ray-1")
				return resp, nil
			})}
			resp, err := httpx.Do(t.Context(), httpx.NewClient(&http.Client{Transport: transport}), baseRequest("https://127.0.0.1/v1/systemone"))
			assert.Equal(t, int32(1), tt.body.closes.Load())
			assert.Equal(t, 1, transport.Attempts())
			assert.Equal(t, tt.wantReads, tt.body.reads.Load() > 0)
			if !tt.wantErr {
				require.NoError(t, err)
				assert.Equal(t, tt.status, resp.StatusCode)
				return
			}
			assert.Nil(t, resp)
			pe := testutil.ProviderError(t, err)
			assert.Equal(t, tt.wantKind, pe.Kind)
			assert.Equal(t, tt.status, pe.StatusCode)
			assert.Equal(t, "req-1", pe.RequestID)
			assert.Equal(t, "ray-1", pe.RayID)
		})
	}
}

func TestDoKeepsTheRetryHintOfAnUnreadableRefusal(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          io.ReadCloser
		contentLength int64
		wantKind      discern.FailureKind
		wantHint      bool
	}{
		{name: "declared length over the limit", status: http.StatusTooManyRequests, body: newBody(`{}`), contentLength: 1 << 40, wantKind: discern.FailureTooLarge, wantHint: true},
		{name: "body over the limit", status: http.StatusServiceUnavailable, body: newBody(strings.Repeat("x", 2048)), contentLength: -1, wantKind: discern.FailureTooLarge, wantHint: true},
		{name: "read error", status: http.StatusServiceUnavailable, body: &trackingBody{r: strings.NewReader(`{"e`), readErr: errReset}, contentLength: -1, wantKind: discern.FailureTransport, wantHint: true},
		{name: "redirect over the limit", status: http.StatusFound, body: newBody(strings.Repeat("x", 2048)), contentLength: -1, wantKind: discern.FailureTooLarge, wantHint: true},
		{name: "success over the limit", status: http.StatusOK, body: newBody(strings.Repeat("x", 2048)), contentLength: -1, wantKind: discern.FailureTooLarge},
		{name: "success read error", status: http.StatusOK, body: &trackingBody{r: strings.NewReader(`{"e`), readErr: errReset}, contentLength: -1, wantKind: discern.FailureTransport},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hc := fakeClient(func(r *http.Request) (*http.Response, error) {
				resp := response(r, tt.status, tt.body, tt.contentLength)
				resp.Header.Set("Retry-After", "7")
				resp.Header.Set(requestIDName, "req-1")
				return resp, nil
			})
			resp, err := httpx.Do(t.Context(), hc, baseRequest("https://127.0.0.1/v1/systemone"))
			assert.Nil(t, resp)
			pe := testutil.ProviderError(t, err)
			assert.Equal(t, tt.wantKind, pe.Kind)
			assert.Equal(t, tt.status, pe.StatusCode)
			assert.Equal(t, "req-1", pe.RequestID)
			assert.Equal(t, tt.wantHint, pe.HasRetryAfter)
			if tt.wantHint {
				assert.Equal(t, 7*time.Second, pe.RetryAfter)
			} else {
				assert.Zero(t, pe.RetryAfter)
			}
		})
	}
}

func TestDoRetryHintOfAStalledRefusalIsDatedAtTheFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		at := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
		hc := fakeClient(func(r *http.Request) (*http.Response, error) {
			resp := response(r, http.StatusServiceUnavailable, stalledBody(r.Context()), -1)
			resp.Header.Set("Retry-After", at)
			return resp, nil
		})
		req := baseRequest("https://127.0.0.1/v1/systemone")
		req.Timeout = 30 * time.Second
		_, err := httpx.Do(t.Context(), hc, req)
		pe := testutil.ProviderError(t, err)
		assert.Equal(t, discern.FailureTimeout, pe.Kind)
		assert.True(t, pe.HasRetryAfter)
		assert.Equal(t, 60*time.Second, pe.RetryAfter, "the date is measured from when the read failed")
	})
}

func TestDoReadErrorCause(t *testing.T) {
	hc := fakeClient(func(r *http.Request) (*http.Response, error) {
		return response(r, http.StatusOK, &trackingBody{r: strings.NewReader(`{"a`), readErr: errReset}, -1), nil
	})
	_, err := httpx.Do(t.Context(), hc, baseRequest("https://127.0.0.1/v1/systemone"))
	pe := testutil.ProviderError(t, err)
	assert.Equal(t, discern.FailureTransport, pe.Kind)
	require.ErrorIs(t, err, errReset)
	assert.Equal(t, "discern: jev: transport (HTTP 200)", pe.Error())
}

func TestDoResponseSizeLimit(t *testing.T) {
	tests := []struct {
		name          string
		maxBytes      int64
		size          int
		contentLength int64
		wantTooLarge  bool
	}{
		{name: "exactly max with length", maxBytes: 16, size: 16, contentLength: 16},
		{name: "exactly max unknown length", maxBytes: 16, size: 16, contentLength: -1},
		{name: "one over unknown length", maxBytes: 16, size: 17, contentLength: -1, wantTooLarge: true},
		{name: "one over with length", maxBytes: 16, size: 17, contentLength: 17, wantTooLarge: true},
		{name: "understated length", maxBytes: 16, size: 17, contentLength: 3, wantTooLarge: true},
		{name: "empty body", maxBytes: 16, size: 0, contentLength: 0},
		{name: "zero limit clamps to one", maxBytes: 0, size: 1, contentLength: -1},
		{name: "zero limit still bounded", maxBytes: 0, size: 2, contentLength: -1, wantTooLarge: true},
		{name: "negative limit clamps to one", maxBytes: -5, size: 2, contentLength: -1, wantTooLarge: true},
		{name: "huge limit clamps without overflow", maxBytes: 1<<63 - 1, size: 64, contentLength: -1},
		{name: "huge limit still rejects declared length", maxBytes: 1<<63 - 1, size: 1, contentLength: httpx.MaxResponseBytesLimit + 1, wantTooLarge: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := strings.Repeat("x", tt.size)
			body := newBody(payload)
			hc := fakeClient(func(r *http.Request) (*http.Response, error) {
				return response(r, http.StatusOK, body, tt.contentLength), nil
			})
			req := baseRequest("https://127.0.0.1/v1/systemone")
			req.MaxBytes = tt.maxBytes
			resp, err := httpx.Do(t.Context(), hc, req)
			assert.Equal(t, int32(1), body.closes.Load())
			if !tt.wantTooLarge {
				require.NoError(t, err)
				assert.Equal(t, payload, string(resp.Body))
				return
			}
			require.ErrorIs(t, err, discern.ErrMalformedResponse)
			assert.Equal(t, discern.FailureTooLarge, testutil.ProviderError(t, err).Kind)
		})
	}
}

// TestDoTimeoutsAndCancellation covers every way a call can run out of time or
// be canceled, while awaiting headers or reading the body. The call's own
// timeout and an injected http.Client.Timeout are both timeouts; a caller
// deadline or cancellation that comes first is the caller's. The cause is
// always the bare context error, never the standard library's wrapped text.
func TestDoTimeoutsAndCancellation(t *testing.T) {
	tests := []struct {
		name          string
		timeout       time.Duration // the call's own timeout; 0 selects the default
		clientTimeout time.Duration // an injected http.Client.Timeout; 0 means none
		callerLimit   time.Duration // a caller deadline; 0 means none
		cancel        bool          // the caller cancels once the request is sent
		status        int           // when set, headers arrive with it and the body stalls
		wantKind      discern.FailureKind
		wantErr       error
		wantElapsed   time.Duration
	}{
		{name: "call timeout awaiting headers", timeout: 2 * time.Second, wantKind: discern.FailureTimeout, wantErr: context.DeadlineExceeded, wantElapsed: 2 * time.Second},
		{name: "call timeout covers a stalled body", timeout: 3 * time.Second, status: http.StatusOK, wantKind: discern.FailureTimeout, wantErr: context.DeadlineExceeded, wantElapsed: 3 * time.Second},
		{name: "default timeout", wantKind: discern.FailureTimeout, wantErr: context.DeadlineExceeded, wantElapsed: httpx.DefaultTimeout},
		{name: "caller canceled awaiting headers", timeout: 5 * time.Second, cancel: true, wantKind: discern.FailureCanceled, wantErr: context.Canceled},
		{name: "caller canceled during the body read", timeout: 5 * time.Second, cancel: true, status: http.StatusAccepted, wantKind: discern.FailureCanceled, wantErr: context.Canceled},
		{name: "caller deadline", timeout: 30 * time.Second, callerLimit: time.Second, wantKind: discern.FailureDeadlineExceeded, wantErr: context.DeadlineExceeded, wantElapsed: time.Second},
		{name: "client timeout awaiting headers", timeout: 30 * time.Second, clientTimeout: 2 * time.Second, wantKind: discern.FailureTimeout, wantErr: context.DeadlineExceeded, wantElapsed: 2 * time.Second},
		{name: "client timeout reading the body", timeout: 30 * time.Second, clientTimeout: 2 * time.Second, status: http.StatusOK, wantKind: discern.FailureTimeout, wantErr: context.DeadlineExceeded, wantElapsed: 2 * time.Second},
		{name: "caller deadline before client timeout", timeout: 30 * time.Second, clientTimeout: 2 * time.Second, callerLimit: time.Second, wantKind: discern.FailureDeadlineExceeded, wantErr: context.DeadlineExceeded, wantElapsed: time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tt.callerLimit > 0 {
					var cancelDeadline context.CancelFunc
					ctx, cancelDeadline = context.WithTimeout(ctx, tt.callerLimit)
					defer cancelDeadline()
				}
				var body *trackingBody
				transport := &testutil.CountingTransport{Next: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					if tt.cancel {
						cancel()
					}
					if tt.status != 0 {
						body = stalledBody(r.Context())
						resp := response(r, tt.status, body, -1)
						resp.Header.Set(requestIDName, "req-stalled")
						return resp, nil
					}
					<-r.Context().Done()
					return nil, r.Context().Err()
				})}
				req := baseRequest("https://127.0.0.1/v1/systemone")
				req.Timeout = tt.timeout
				hc := httpx.NewClient(&http.Client{Transport: transport, Timeout: tt.clientTimeout})

				start := time.Now()
				resp, err := httpx.Do(ctx, hc, req)
				assert.Nil(t, resp)
				assert.Equal(t, tt.wantElapsed, time.Since(start))
				pe := testutil.ProviderError(t, err)
				assert.Equal(t, tt.wantKind, pe.Kind)
				assert.Equal(t, discern.Jev, pe.Provider)
				require.ErrorIs(t, err, tt.wantErr)
				require.EqualError(t, errors.Unwrap(err), tt.wantErr.Error())
				if tt.status != 0 {
					assert.Equal(t, tt.status, pe.StatusCode)
					assert.Equal(t, "req-stalled", pe.RequestID)
					require.NotNil(t, body)
					assert.Equal(t, int32(1), body.closes.Load())
				}
				assert.Equal(t, 1, transport.Attempts())
			})
		})
	}
}

func TestDoSingleAttemptOnRetryableStatus(t *testing.T) {
	for _, status := range []int{429, 500, 502, 503, 504, 529} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			transport := &testutil.CountingTransport{Next: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				resp := response(r, status, newBody(`{"error":"busy"}`), -1)
				resp.Header.Set("Retry-After", "1")
				return resp, nil
			})}
			resp, err := httpx.Do(t.Context(), httpx.NewClient(&http.Client{Transport: transport}), baseRequest("https://127.0.0.1/v1/systemone"))
			require.NoError(t, err)
			assert.Equal(t, status, resp.StatusCode)
			assert.JSONEq(t, `{"error":"busy"}`, string(resp.Body))
			assert.Equal(t, 1, transport.Attempts())
		})
	}
}

func TestDoSingleAttemptAgainstServer(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := testutil.NewServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(status)
			})
			resp, err := httpx.Do(t.Context(), httpx.NewClient(srv.Client()), baseRequest(srv.URL()+"/v1/systemone"))
			require.NoError(t, err)
			assert.Equal(t, status, resp.StatusCode)
			assert.Empty(t, resp.Body)
			assert.Equal(t, 1, srv.Hits())
		})
	}
}

// TestDoTransportFailures covers exchanges that fail before any response. Each
// is a transport failure that keeps a cause, and no *url.Error, URL or
// credential reaches the error, however it is printed.
func TestDoTransportFailures(t *testing.T) {
	accountURL := "https://127.0.0.1/client/v4/accounts/" + testAccount
	tests := []struct {
		name    string
		client  func(t *testing.T) *http.Client
		url     string
		wantErr error // a cause that must stay reachable, if any
	}{
		{
			name: "dial failure hides the URL",
			client: func(t *testing.T) *http.Client {
				transport := &http.Transport{}
				t.Cleanup(transport.CloseIdleConnections)
				return httpx.NewClient(&http.Client{Transport: transport})
			},
			url: "https://127.0.0.1:1/client/v4/accounts/" + testAccount + "/ai/run/@cf/clef",
		},
		{
			name: "non-URL error keeps its cause",
			client: func(*testing.T) *http.Client {
				return &http.Client{Transport: testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) {
					return nil, errReset
				})}
			},
			url:     accountURL,
			wantErr: errReset,
		},
		{
			name: "URL error without a cause",
			client: func(*testing.T) *http.Client {
				return &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
					return nil, &url.Error{Op: "Post", URL: r.URL.String(), Err: nil}
				})}
			},
			url: accountURL + "/ai/run/@cf/clef",
		},
		{
			name:   "invalid URL hides the URL",
			client: func(*testing.T) *http.Client { return httpx.NewClient(nil) },
			url:    accountURL + "/\x7f",
		},
		{
			name:   "nil client uses the default transport",
			client: func(*testing.T) *http.Client { return nil },
			url:    "https://127.0.0.1:1/v1/systemone",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := httpx.Do(t.Context(), tt.client(t), baseRequest(tt.url))
			pe := testutil.ProviderError(t, err)
			assert.Equal(t, discern.FailureTransport, pe.Kind)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			}
			var ue *url.Error
			require.NotErrorAs(t, err, &ue, "every *url.Error is stripped, even one without a cause")
			cause := errors.Unwrap(pe)
			require.Error(t, cause, "a transport failure always keeps a cause")
			for _, msg := range []string{pe.Error(), cause.Error(), fmt.Sprintf("%#v", pe), fmt.Sprintf("%v", err)} {
				assert.NotContains(t, msg, testAccount)
				assert.NotContains(t, msg, "/client/v4")
				assert.NotContains(t, msg, "test-key")
			}
		})
	}
}

func TestRejected(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	codes := []int{10000, 7003}
	messages := []string{"SECRET-MESSAGE: quota exceeded for ticket billing"}
	resp := &httpx.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": {"7"}},
		Body:       []byte(`{"errors":[{"message":"SECRET-BODY"}]}`),
		RequestID:  "req-1",
		RayID:      "ray-1",
	}
	pe := httpx.Rejected(discern.Clef, resp, now, codes, messages)
	codes[0] = 1
	messages[0] = "changed"

	assert.Equal(t, discern.Clef, pe.Provider)
	assert.Equal(t, discern.FailureRejected, pe.Kind)
	assert.Equal(t, http.StatusTooManyRequests, pe.StatusCode)
	assert.Equal(t, "req-1", pe.RequestID)
	assert.Equal(t, "ray-1", pe.RayID)
	assert.Equal(t, 7*time.Second, pe.RetryAfter)
	assert.True(t, pe.HasRetryAfter)
	assert.Equal(t, []int{10000, 7003}, pe.Codes)
	require.ErrorIs(t, pe, discern.ErrRejected)
	require.ErrorIs(t, pe, httpx.ErrStatus)
	assert.Equal(t, []string{"SECRET-MESSAGE: quota exceeded for ticket billing"}, pe.ProviderMessages())
	for _, s := range []string{pe.Error(), fmt.Sprintf("%#v", pe), fmt.Sprintf("%v", pe)} {
		assert.NotContains(t, s, "SECRET-MESSAGE")
		assert.NotContains(t, s, "SECRET-BODY")
	}
	assert.Equal(t, "discern: clef: rejected (HTTP 429)", pe.Error())
}

func TestRejectedWithoutHints(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	pe := httpx.Rejected(discern.Jev, &httpx.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}, now, nil, nil)
	assert.Equal(t, http.StatusForbidden, pe.StatusCode)
	assert.False(t, pe.HasRetryAfter)
	assert.Zero(t, pe.RetryAfter)
	assert.Nil(t, pe.Codes)
	assert.Empty(t, pe.ProviderMessages())

	pe = httpx.Rejected(discern.Jev, &httpx.Response{StatusCode: http.StatusOK, Header: http.Header{"Retry-After": {"0"}}}, now, []int{}, nil)
	assert.True(t, pe.HasRetryAfter)
	assert.Zero(t, pe.RetryAfter)
	assert.Equal(t, []int{}, pe.Codes)

	pe = httpx.Rejected(discern.Jev, nil, now, nil, nil)
	assert.Equal(t, discern.FailureRejected, pe.Kind)
	assert.Zero(t, pe.StatusCode)
}

func TestDoBodyBytesVerbatim(t *testing.T) {
	payload := []byte("{\"state\":\"<b>&amp;</b> \\u003c\",\"n\":12345678901234567890}\n")
	// The in-memory transport runs on the calling goroutine, so it can record
	// the body without synchronization.
	var got []byte
	hc := fakeClient(func(r *http.Request) (*http.Response, error) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		got = b
		return response(r, http.StatusOK, newBody(`{}`), -1), nil
	})
	req := baseRequest("https://127.0.0.1/v1/systemone")
	req.Body = payload
	_, err := httpx.Do(t.Context(), hc, req)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

// staticClient answers every request in memory with status 200, body and the
// declared content length (-1 for unknown).
func staticClient(body string, contentLength int64) *http.Client {
	return fakeClient(func(r *http.Request) (*http.Response, error) {
		return response(r, http.StatusOK, io.NopCloser(strings.NewReader(body)), contentLength), nil
	})
}

func TestDoReadsBodiesOfEveryShape(t *testing.T) {
	const payload = `{"answers":{"urgent":{"type":"noul","noul":0.25}}}`
	tests := []struct {
		name          string
		body          func() io.Reader
		contentLength int64
		want          string
	}{
		{name: "one byte per read", body: func() io.Reader { return iotest.OneByteReader(strings.NewReader(payload)) }, contentLength: -1, want: payload},
		{name: "one byte per read with length", body: func() io.Reader { return iotest.OneByteReader(strings.NewReader(payload)) }, contentLength: int64(len(payload)), want: payload},
		{name: "data with EOF", body: func() io.Reader { return iotest.DataErrReader(strings.NewReader(payload)) }, contentLength: int64(len(payload)), want: payload},
		{name: "overstated length", body: func() io.Reader { return strings.NewReader(payload) }, contentLength: 900, want: payload},
		{name: "understated length", body: func() io.Reader { return strings.NewReader(payload) }, contentLength: 3, want: payload},
		{name: "empty with length", body: func() io.Reader { return strings.NewReader("") }, contentLength: 0, want: ""},
		{name: "longer than the first buffer", body: func() io.Reader { return strings.NewReader(strings.Repeat("y", 1000)) }, contentLength: -1, want: strings.Repeat("y", 1000)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hc := fakeClient(func(r *http.Request) (*http.Response, error) {
				return response(r, http.StatusOK, io.NopCloser(tt.body()), tt.contentLength), nil
			})
			resp, err := httpx.Do(t.Context(), hc, baseRequest("https://127.0.0.1/v1/systemone"))
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(resp.Body))
		})
	}
}

func TestDoRequestHeadersAreIndependent(t *testing.T) {
	// Each call gets its own header values, and growing one value cannot
	// overwrite the next.
	var (
		accepts []*string
		after   []http.Header
	)
	hc := fakeClient(func(r *http.Request) (*http.Response, error) {
		accepts = append(accepts, &r.Header["Accept"][0])
		r.Header["Content-Type"] = append(r.Header["Content-Type"], "text/plain")
		after = append(after, r.Header.Clone())
		return response(r, http.StatusOK, newBody(`{}`), 2), nil
	})
	for range 2 {
		_, err := httpx.Do(t.Context(), hc, baseRequest("https://127.0.0.1/v1/systemone"))
		require.NoError(t, err)
	}
	require.Len(t, after, 2)
	for _, h := range after {
		assert.Equal(t, []string{"application/json", "text/plain"}, h.Values("Content-Type"))
		assert.Equal(t, []string{"application/json"}, h.Values("Accept"))
		assert.Equal(t, []string{httpx.UserAgent}, h.Values("User-Agent"))
		assert.Equal(t, []string{"Bearer test-key"}, h.Values("Authorization"))
	}
	assert.NotSame(t, accepts[0], accepts[1])
}

func TestMalformed(t *testing.T) {
	resp := &httpx.Response{StatusCode: http.StatusOK, Header: http.Header{"Retry-After": {"5"}}, RequestID: "req-1", RayID: "ray-1"}
	pe := httpx.Malformed(discern.Clef, resp, errReset)
	assert.Equal(t, discern.Clef, pe.Provider)
	assert.Equal(t, discern.FailureMalformed, pe.Kind)
	assert.Equal(t, http.StatusOK, pe.StatusCode)
	assert.Equal(t, "req-1", pe.RequestID)
	assert.Equal(t, "ray-1", pe.RayID)
	assert.False(t, pe.HasRetryAfter)
	require.ErrorIs(t, pe, discern.ErrMalformedResponse)
	require.ErrorIs(t, pe, errReset)
	assert.Equal(t, "discern: clef: malformed_response (HTTP 200)", pe.Error())

	pe = httpx.Malformed(discern.Jev, nil, errReset)
	assert.Equal(t, discern.FailureMalformed, pe.Kind)
	assert.Zero(t, pe.StatusCode)
}

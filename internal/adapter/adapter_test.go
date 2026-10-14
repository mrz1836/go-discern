package adapter_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/adapter"
	"github.com/mrz1836/go-discern/internal/httpx"
	"github.com/mrz1836/go-discern/internal/testutil"
)

const (
	testURL           = "https://127.0.0.1/v1/systemone"
	testAuthorization = "Bearer test-key"
)

var (
	errDecode = errors.New("synthetic decode failure")
	errReset  = errors.New("synthetic connection reset")
)

// option is an adapter-style Option type over adapter.Config.
type option func(*adapter.Config)

// transport answers every request in memory and records it. RoundTrip runs on
// the calling goroutine, so the recording needs no locking; the counter is
// atomic only to be safe to read from anywhere.
type transport struct {
	status   int
	header   http.Header
	body     string
	err      error
	attempts atomic.Int32
	seen     []testutil.Request
}

func (tr *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.attempts.Add(1)
	tr.seen = append(tr.seen, testutil.Record(r))
	if tr.err != nil {
		return nil, tr.err
	}
	return &http.Response{
		StatusCode: tr.status, Header: tr.header.Clone(), Request: r,
		Body: io.NopCloser(strings.NewReader(tr.body)), ContentLength: int64(len(tr.body)),
	}, nil
}

// decodeOK returns an empty Observation, recording the body it was given.
func decodeOK(got *string) func(*httpx.Response, *discern.Request) (*discern.Observation, error) {
	return func(resp *httpx.Response, _ *discern.Request) (*discern.Observation, error) {
		*got = string(resp.Body)
		return &discern.Observation{Model: "jev-1"}, nil
	}
}

// sender returns a Sender over tr with the default settings and spec's
// provider, URL and credentials filled in.
func sender(tr *transport, spec adapter.Spec) adapter.Sender {
	cfg := adapter.Configure("jev-1", []option{func(c *adapter.Config) { c.HTTPClient = &http.Client{Transport: tr} }})
	spec.Provider, spec.URL, spec.Authorization = discern.Jev, testURL, testAuthorization
	return adapter.NewSender(&cfg, spec)
}

func TestConfigure(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cfg := adapter.Configure[option]("jev-latest", nil)
		assert.Equal(t, adapter.Config{Model: "jev-latest", Timeout: httpx.DefaultTimeout, MaxBytes: httpx.DefaultMaxResponseBytes}, cfg)
	})
	t.Run("in order, skipping nil", func(t *testing.T) {
		hc := &http.Client{}
		cfg := adapter.Configure("jev-latest", []option{
			nil,
			func(c *adapter.Config) { c.Model, c.Timeout = "first", time.Second },
			nil,
			func(c *adapter.Config) { c.Model, c.HTTPClient = "second", hc },
		})
		assert.Equal(t, "second", cfg.Model)
		assert.Equal(t, time.Second, cfg.Timeout)
		assert.Same(t, hc, cfg.HTTPClient)
		assert.Equal(t, int64(httpx.DefaultMaxResponseBytes), cfg.MaxBytes)
	})
}

func TestOrigin(t *testing.T) {
	const production = "https://api.typesafe.ai"
	valid := func() adapter.Config { return adapter.Configure[option]("jev-1", nil) }
	tests := []struct {
		name   string
		mutate func(c *adapter.Config)
		want   string
		field  string
	}{
		{name: "production", mutate: func(*adapter.Config) {}, want: production},
		{name: "loopback endpoint", mutate: func(c *adapter.Config) { c.Endpoint, c.HasEndpoint = "https://127.0.0.1:8443/", true }, want: "https://127.0.0.1:8443"},
		{name: "smallest limits", mutate: func(c *adapter.Config) { c.Timeout, c.MaxBytes = time.Nanosecond, 1 }, want: production},
		{name: "largest limit", mutate: func(c *adapter.Config) { c.MaxBytes = httpx.MaxResponseBytesLimit }, want: production},
		{name: "zero timeout", mutate: func(c *adapter.Config) { c.Timeout = 0 }, field: "timeout"},
		{name: "negative timeout", mutate: func(c *adapter.Config) { c.Timeout = -time.Second }, field: "timeout"},
		{name: "zero limit", mutate: func(c *adapter.Config) { c.MaxBytes = 0 }, field: "max_response_bytes"},
		{name: "limit too large", mutate: func(c *adapter.Config) { c.MaxBytes = httpx.MaxResponseBytesLimit + 1 }, field: "max_response_bytes"},
		{name: "empty endpoint", mutate: func(c *adapter.Config) { c.HasEndpoint = true }, field: "endpoint"},
		{name: "public endpoint", mutate: func(c *adapter.Config) { c.Endpoint, c.HasEndpoint = "https://example.com", true }, field: "endpoint"},
		{name: "timeout before endpoint", mutate: func(c *adapter.Config) { c.Timeout, c.Endpoint, c.HasEndpoint = 0, "x", true }, field: "timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid()
			tt.mutate(&cfg)
			got, err := cfg.Origin(production)
			if tt.field == "" {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
				return
			}
			assert.Empty(t, got)
			require.ErrorIs(t, err, discern.ErrInvalidConfig)
			ve := testutil.ValidationError(t, err)
			assert.Equal(t, tt.field, ve.Field)
			assert.NotContains(t, err.Error(), "example.com")
		})
	}
}

func TestCheckCredential(t *testing.T) {
	require.NoError(t, adapter.CheckCredential("api_key", "test-key"))
	for _, bad := range []string{"", "test key", "test-key\n", strings.Repeat("k", 4097)} {
		err := adapter.CheckCredential("api_key", bad)
		require.ErrorIs(t, err, discern.ErrInvalidConfig)
		ve := testutil.ValidationError(t, err)
		assert.Equal(t, "api_key", ve.Field)
		if bad != "" {
			assert.NotContains(t, err.Error(), bad)
		}
	}
}

func TestDescribe(t *testing.T) {
	assert.Equal(t, `jev.Client{model: "jev-1.13.0"}`, adapter.Describe("jev", "jev-1.13.0"))
	assert.Equal(t, `clef.Client{model: ""}`, adapter.Describe("clef", ""))
	assert.Equal(t, `jev.Client{model: "a\"b"}`, adapter.Describe("jev", `a"b`))
}

func TestSenderCheck(t *testing.T) {
	var zero adapter.Sender
	err := zero.Check(&discern.Request{})
	require.ErrorIs(t, err, discern.ErrInvalidConfig)
	ve := testutil.ValidationError(t, err)
	assert.Equal(t, "client", ve.Field)

	s := sender(&transport{status: http.StatusOK}, adapter.Spec{})
	err = s.Check(nil)
	require.ErrorIs(t, err, discern.ErrInvalidRequest)
	ve = testutil.ValidationError(t, err)
	assert.Equal(t, "request", ve.Field)
	require.NoError(t, s.Check(&discern.Request{}))
}

func TestSenderCallSucceeds(t *testing.T) {
	tr := &transport{
		status: http.StatusOK,
		header: http.Header{"X-Request-Id": {"req-1"}, "Cf-Ray": {"ray-1"}},
		body:   `{"answers":{}}`,
	}
	var decoded string
	// Lowercase names are canonicalized once, when the Sender is built.
	s := sender(tr, adapter.Spec{RequestIDHeader: "x-request-id", RayIDHeader: "cf-ray", Decode: decodeOK(&decoded)})
	sent := []byte(`{"model":"jev-1"}`)
	call := s.Call(&discern.Request{}, sent)
	assert.Equal(t, int32(0), tr.attempts.Load())

	obs, err := call(t.Context())
	require.NoError(t, err)
	assert.Equal(t, &discern.Observation{Model: "jev-1", RequestID: "req-1", RayID: "ray-1"}, obs)
	assert.Equal(t, tr.body, decoded, "Decode receives the response body unchanged")
	assert.Equal(t, int32(1), tr.attempts.Load())
	require.Len(t, tr.seen, 1)
	assert.Equal(t, testURL, tr.seen[0].URL)
	assert.Equal(t, testAuthorization, tr.seen[0].Header.Get("Authorization"))
	assert.Equal(t, sent, tr.seen[0].Body, "the request body is sent unchanged")

	_, err = call(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int32(2), tr.attempts.Load())
}

func TestSenderCallFails(t *testing.T) {
	refusal := func([]byte) ([]int, []string) { return []int{3040}, []string{"Capacity exceeded"} }
	// A plain decode error comes back as a malformed response carrying the
	// response metadata; a ProviderError, such as a refusal in a 2xx body, comes
	// back as it is.
	decodeErr := func(*httpx.Response, *discern.Request) (*discern.Observation, error) {
		return nil, errDecode
	}
	decodeRefusal := func(resp *httpx.Response, _ *discern.Request) (*discern.Observation, error) {
		return nil, adapter.Rejected(discern.Jev, resp, []int{1000}, []string{"Refused"})
	}
	requestID := http.Header{"X-Request-Id": {"req-1"}}
	tests := []struct {
		name          string
		tr            *transport
		spec          adapter.Spec
		wantKind      discern.FailureKind
		wantStatus    int
		wantRequestID string
		wantCodes     []int
		wantMessages  []string
		wantCause     error
	}{
		{
			name: "rejected with an opaque body", tr: &transport{status: http.StatusTooManyRequests, body: `{"error":"slow down"}`},
			wantKind: discern.FailureRejected, wantStatus: http.StatusTooManyRequests,
		},
		{
			name: "rejected with codes", tr: &transport{status: http.StatusTooManyRequests, body: `{}`}, spec: adapter.Spec{Refusal: refusal},
			wantKind: discern.FailureRejected, wantStatus: http.StatusTooManyRequests, wantCodes: []int{3040}, wantMessages: []string{"Capacity exceeded"},
		},
		{
			name: "redirect", tr: &transport{status: http.StatusFound, header: http.Header{"Location": {"https://127.0.0.2/"}}},
			wantKind: discern.FailureRejected, wantStatus: http.StatusFound,
		},
		{
			name: "decode error", tr: &transport{status: http.StatusOK, header: requestID, body: `{`},
			spec:     adapter.Spec{RequestIDHeader: "x-request-id", Decode: decodeErr},
			wantKind: discern.FailureMalformed, wantStatus: http.StatusOK, wantRequestID: "req-1", wantCause: errDecode,
		},
		{
			name: "refusal in a 2xx body", tr: &transport{status: http.StatusOK, header: requestID, body: `{}`},
			spec:     adapter.Spec{RequestIDHeader: "x-request-id", Decode: decodeRefusal},
			wantKind: discern.FailureRejected, wantStatus: http.StatusOK, wantRequestID: "req-1",
			wantCodes: []int{1000}, wantMessages: []string{"Refused"}, wantCause: httpx.ErrStatus,
		},
		{
			name: "transport error", tr: &transport{err: errReset},
			wantKind: discern.FailureTransport, wantCause: errReset,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var decoded string
			if tt.spec.Decode == nil {
				tt.spec.Decode = decodeOK(&decoded)
			}
			obs, err := sender(tt.tr, tt.spec).Call(&discern.Request{}, nil)(t.Context())
			assert.Nil(t, obs)
			pe := testutil.ProviderError(t, err)
			assert.Equal(t, discern.Jev, pe.Provider)
			assert.Equal(t, tt.wantKind, pe.Kind)
			assert.Equal(t, tt.wantStatus, pe.StatusCode)
			assert.Equal(t, tt.wantRequestID, pe.RequestID)
			assert.Equal(t, tt.wantCodes, pe.Codes)
			assert.Equal(t, tt.wantMessages, pe.ProviderMessages())
			if tt.wantCause != nil {
				require.ErrorIs(t, err, tt.wantCause)
			}
			assert.Empty(t, decoded, "a failed call must not reach a successful decode")
			assert.Equal(t, int32(1), tt.tr.attempts.Load())
		})
	}
}

func TestCheckResponseSize(t *testing.T) {
	req := &discern.Request{Questions: []discern.Question{discern.Noul("urgent", discern.Text("Is it urgent?"))}}
	shortest := int64(len(`{"answers":{"urgent":{"type":"noul","noul":0}}}`))
	require.NoError(t, adapter.CheckResponseSize(req, shortest), "a limit that fits the shortest answer")
	require.NoError(t, adapter.CheckResponseSize(req, httpx.DefaultMaxResponseBytes))

	err := adapter.CheckResponseSize(req, shortest-1)
	require.ErrorIs(t, err, discern.ErrUnsupported)
	ve := testutil.ValidationError(t, err)
	assert.Equal(t, "request", ve.Field)
	assert.Equal(t, "the response would exceed the response size limit; raise WithMaxResponseBytes", ve.Reason)
}

func TestRejectedDatesTheRetryHint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		at := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
		resp := &httpx.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Retry-After": {at}}, RequestID: "req-1"}
		pe := adapter.Rejected(discern.Clef, resp, nil, nil)
		assert.Equal(t, discern.FailureRejected, pe.Kind)
		assert.True(t, pe.HasRetryAfter)
		assert.Equal(t, 90*time.Second, pe.RetryAfter)
		assert.Equal(t, "req-1", pe.RequestID)
	})
}

func TestSenderTimeoutLeavesNothingRunning(t *testing.T) {
	// The bubble fails if the call leaves any goroutine behind.
	synctest.Test(t, func(t *testing.T) {
		block := testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		cfg := adapter.Configure("jev-1", []option{func(c *adapter.Config) {
			c.HTTPClient, c.Timeout = &http.Client{Transport: block}, 2*time.Second
		}})
		var decoded string
		s := adapter.NewSender(&cfg, adapter.Spec{Provider: discern.Jev, URL: testURL, Decode: decodeOK(&decoded)})
		start := time.Now()
		_, err := s.Call(&discern.Request{}, nil)(t.Context())
		assert.Equal(t, 2*time.Second, time.Since(start))
		assert.Equal(t, discern.FailureTimeout, testutil.ProviderError(t, err).Kind)
	})
}

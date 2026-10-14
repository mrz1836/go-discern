package testutil_test

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
)

// sentinel is the planted secret the leak assertions must catch.
const sentinel = "SECRET-SENTINEL-5e1d"

var errPlanted = errors.New("planted failure")

// recorder is a testing.TB that records failures instead of failing, so a test
// can watch an assertion helper fail. Methods it does not override panic,
// through the nil embedded TB.
type recorder struct {
	testing.TB

	failures []string
}

func (r *recorder) Helper() {}

func (r *recorder) Name() string { return "recorder" }

func (r *recorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func (r *recorder) FailNow() { r.failures = append(r.failures, "FailNow") }

// leaky prints nothing secret through String, but holds the sentinel in an
// unexported field that reflection reaches.
type leaky struct {
	secret string
}

func (leaky) String() string { return "leaky{redacted}" }

// sealed keeps its secret behind a closure, which fmt cannot print.
type sealed struct {
	secret func() string
}

func (sealed) String() string { return "sealed{redacted}" }

func TestAssertRedacted(t *testing.T) {
	h := hex.EncodeToString([]byte(sentinel))
	tests := []struct {
		name   string
		out    string
		leaked bool
	}{
		{name: "clean", out: "nothing to see"},
		{name: "plain", out: "x " + sentinel + " y", leaked: true},
		{name: "lower-case hex", out: "x " + h + " y", leaked: true},
		{name: "upper-case hex", out: "x " + strings.ToUpper(h) + " y", leaked: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{}
			ok := testutil.AssertRedacted(rec, tt.out, []string{"other", sentinel})
			assert.Equal(t, !tt.leaked, ok)
			assert.Equal(t, tt.leaked, len(rec.failures) > 0, "failures: %v", rec.failures)
		})
	}
}

func TestAssertNeverPrints(t *testing.T) {
	t.Run("reflection reaches an unexported field", func(t *testing.T) {
		rec := &recorder{}
		assert.False(t, testutil.AssertNeverPrints(rec, leaky{secret: sentinel}, sentinel))
		require.NotEmpty(t, rec.failures)
		assert.Contains(t, rec.failures[0], "on testutil_test.leaky")
	})
	t.Run("a closure hides the secret", func(t *testing.T) {
		rec := &recorder{}
		v := sealed{secret: func() string { return sentinel }}
		for _, s := range testutil.Shapes(&v) {
			assert.True(t, testutil.AssertNeverPrints(rec, s.Value, sentinel), s.Name)
		}
		assert.Empty(t, rec.failures)
	})
}

func TestShapes(t *testing.T) {
	v := leaky{secret: sentinel}
	shapes := testutil.Shapes(&v)
	require.Len(t, shapes, 10)
	names := map[string]bool{}
	for _, s := range shapes {
		names[s.Name] = true
		if s.Name == "pointer to pointer" {
			continue // fmt prints only its address
		}
		rec := &recorder{}
		assert.False(t, testutil.AssertNeverPrints(rec, s.Value, sentinel), "every shape reaches the field: %s", s.Name)
	}
	assert.Len(t, names, len(shapes), "shape names are unique")
	assert.Len(t, testutil.FormatVerbs(), 15)
}

func TestAssertDiffRedacted(t *testing.T) {
	t.Run("a diff prints the secret", func(t *testing.T) {
		rec := &recorder{}
		a, b := leaky{secret: sentinel}, leaky{secret: "other"}
		assert.False(t, testutil.AssertDiffRedacted(rec, &a, &b, sentinel))
		assert.NotEmpty(t, rec.failures)
	})
	t.Run("a diff without secrets", func(t *testing.T) {
		rec := &recorder{}
		a := sealed{secret: func() string { return sentinel }}
		b := sealed{secret: func() string { return "other" }}
		assert.True(t, testutil.AssertDiffRedacted(rec, &a, &b, sentinel))
		assert.Empty(t, rec.failures)
	})
	t.Run("equal values print nothing to check", func(t *testing.T) {
		rec := &recorder{}
		a := leaky{secret: "same"}
		assert.False(t, testutil.AssertDiffRedacted(rec, &a, &a, sentinel))
		assert.NotEmpty(t, rec.failures)
	})
}

func TestTokenWalk(t *testing.T) {
	tests := []struct {
		name  string
		data  string
		valid bool
		dup   bool
	}{
		{name: "object", data: `{"a":1,"b":[{"a":2}]}`, valid: true},
		{name: "scalar", data: `1`, valid: true},
		{name: "repeated key", data: `{"a":1,"a":2}`, valid: true, dup: true},
		{name: "repeated key once decoded", data: testutil.Esc(`{"~u0061":1,"a":2}`), valid: true, dup: true},
		{name: "repeated key in a nested array", data: `{"a":[{"b":1,"b":1}]}`, valid: true, dup: true},
		{name: "same key in sibling objects", data: `[{"a":1},{"a":2}]`, valid: true},
		{name: "trailing value", data: `{"a":1} 2`},
		{name: "truncated", data: `[1,2`},
		{name: "repeated key, then invalid", data: `{"a":1,"a":2,}`, dup: true},
		{name: "invalid UTF-8", data: "\"\xff\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valid, dup := testutil.TokenWalk([]byte(tt.data))
			assert.Equal(t, tt.valid, valid, "valid")
			assert.Equal(t, tt.dup, dup, "dup")
		})
	}
	assert.Equal(t, `"\u0061"`, testutil.Esc(`"~u0061"`))
}

func TestErrorHelpers(t *testing.T) {
	pe := discern.NewProviderError(discern.Jev, discern.FailureTransport, errPlanted)
	assert.Same(t, pe, testutil.ProviderError(t, fmt.Errorf("wrapped: %w", pe)))
	ve := &discern.ValidationError{Err: discern.ErrInvalidRequest, Field: "state"}
	assert.Same(t, ve, testutil.ValidationError(t, fmt.Errorf("wrapped: %w", ve)))

	rec := &recorder{}
	assert.Nil(t, testutil.ProviderError(rec, errPlanted))
	assert.Nil(t, testutil.ValidationError(rec, errPlanted))
	assert.Len(t, rec.failures, 4, "each fails with a message and FailNow")
}

func TestEvaluateAndFailure(t *testing.T) {
	res, err := testutil.Evaluate(t, discerntest.Failing(discern.Jev, nil), discern.Request{
		Providers: discern.JevOnly, State: discern.Text("ticket"),
		Questions: []discern.Question{discern.Noul("urgent", discern.Text("Is it urgent?"))},
	})
	require.Error(t, err)
	assert.Equal(t, discern.FailureTransport, testutil.Failure(t, res).Kind)
	assert.NotNil(t, testutil.Evaluator(t, discern.WithAdapter(discerntest.Failing(discern.Clef, nil))))
}

func TestHTTPFakes(t *testing.T) {
	srv := testutil.NewServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	ct := &testutil.CountingTransport{Next: srv.Client().Transport}
	hc := &http.Client{Transport: ct}
	for _, body := range []string{"first", "second"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL()+"/path?q=1", strings.NewReader(body))
		require.NoError(t, err)
		resp, err := hc.Do(req)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		require.NoError(t, resp.Body.Close())
	}
	assert.Equal(t, 2, ct.Attempts())
	assert.Equal(t, 2, srv.Hits())
	reqs := srv.Requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, http.MethodPost, reqs[1].Method)
	assert.Equal(t, "/path?q=1", reqs[1].URL)
	assert.Equal(t, "second", string(reqs[1].Body))

	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://127.0.0.1/", strings.NewReader("12345"))
	require.NoError(t, err)
	assert.Equal(t, int64(5), testutil.Drain(r))
	r.Body = nil
	assert.Zero(t, testutil.Drain(r))
	assert.Equal(t, testutil.Request{Method: http.MethodPost, URL: "https://127.0.0.1/", Header: http.Header{}}, testutil.Record(r))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	resp, err := testutil.RoundTripFunc(testutil.BlockUntilDone).RoundTrip(r.WithContext(ctx))
	if resp != nil {
		_ = resp.Body.Close()
	}
	assert.Nil(t, resp)
	require.ErrorIs(t, err, context.Canceled)
}

func TestAllocationHelpers(t *testing.T) {
	testutil.SkipUnderRace(t)
	var sink []byte
	allocs, allocated := testutil.AllocStats(10, func() { sink = make([]byte, 1024) })
	assert.InDelta(t, 1, allocs, 0.5)
	assert.GreaterOrEqual(t, allocated, 1024.0)
	assert.GreaterOrEqual(t, testutil.BytesPerOp(func() { sink = make([]byte, 1024) }), int64(1024))
	testutil.CheckCeilings(t, []testutil.Ceiling{{Name: "one allocation", Max: 1, Run: func() { sink = make([]byte, 1024) }}})
	_ = sink
}

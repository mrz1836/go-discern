package httpx_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/httpx"
	"github.com/mrz1836/go-discern/internal/testutil"
)

// endlessBody streams 'x' forever and counts the bytes read from it. Do reads
// the body on the calling goroutine, so the count needs no synchronization.
type endlessBody struct {
	read int64
}

func (b *endlessBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	b.read += int64(len(p))
	return len(p), nil
}

func (b *endlessBody) Close() error { return nil }

func TestDoAllocationsStayBounded(t *testing.T) {
	testutil.SkipUnderRace(t)
	const body = `{"answers":{}}`
	req := baseRequest("https://127.0.0.1/v1/systemone")
	do := func(contentLength int64) func() {
		hc := staticClient(body, contentLength)
		return func() { _, _ = httpx.Do(t.Context(), hc, req) }
	}
	// The counts include the in-memory transport's response; the ceilings sit
	// about 25% above the measured values.
	testutil.CheckCeilings(t, []testutil.Ceiling{
		{Name: "declared length", Max: 31, Run: do(int64(len(body)))},
		{Name: "unknown length", Max: 31, Run: do(-1)},
	})
}

func TestDoEndlessBodyReadsAtMostTheLimit(t *testing.T) {
	const limit = 1 << 20
	var body *endlessBody
	hc := fakeClient(func(r *http.Request) (*http.Response, error) {
		body = &endlessBody{}
		return response(r, http.StatusOK, body, -1), nil
	})
	req := baseRequest("https://127.0.0.1/v1/systemone")
	req.MaxBytes = limit

	resp, err := httpx.Do(t.Context(), hc, req)
	assert.Nil(t, resp)
	require.ErrorIs(t, err, discern.ErrMalformedResponse)
	assert.Equal(t, discern.FailureTooLarge, testutil.ProviderError(t, err).Kind)
	require.NotNil(t, body)
	assert.Equal(t, int64(limit+1), body.read)

	testutil.SkipUnderRace(t)
	perOp := testutil.BytesPerOp(func() { _, _ = httpx.Do(t.Context(), hc, req) })
	t.Logf("%d bytes allocated to reject an endless body at a %d-byte limit", perOp, limit)
	// Chunks up to the limit plus one right-sized copy: about twice the limit.
	assert.Less(t, perOp, int64(5*limit/2))
}

func TestDoHugeContentLengthReadsNothing(t *testing.T) {
	var body *endlessBody
	hc := fakeClient(func(r *http.Request) (*http.Response, error) {
		body = &endlessBody{}
		return response(r, http.StatusOK, body, 1<<40), nil
	})
	req := baseRequest("https://127.0.0.1/v1/systemone")
	req.MaxBytes = 1 << 20

	_, err := httpx.Do(t.Context(), hc, req)
	assert.Equal(t, discern.FailureTooLarge, testutil.ProviderError(t, err).Kind)
	require.NotNil(t, body)
	assert.Zero(t, body.read)

	testutil.SkipUnderRace(t)
	perOp := testutil.BytesPerOp(func() { _, _ = httpx.Do(t.Context(), hc, req) })
	t.Logf("%d bytes allocated to reject a declared length of 1 TiB", perOp)
	assert.Less(t, perOp, int64(16<<10))
}

func TestDoDeclaredLengthReadsIntoOneBuffer(t *testing.T) {
	testutil.SkipUnderRace(t)
	payload := strings.Repeat("x", 256<<10)
	tests := []struct {
		name          string
		contentLength int64
		maxFactor     float64
	}{
		// One exact buffer, plus the fixed cost of the exchange.
		{name: "declared length", contentLength: int64(len(payload)), maxFactor: 1.1},
		// Growing chunks plus one right-sized copy: about twice the size.
		{name: "unknown length", contentLength: -1, maxFactor: 2.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hc := staticClient(payload, tt.contentLength)
			req := baseRequest("https://127.0.0.1/v1/systemone")
			req.MaxBytes = 1 << 20
			resp, err := httpx.Do(t.Context(), hc, req)
			require.NoError(t, err)
			require.Equal(t, payload, string(resp.Body))

			perOp := testutil.BytesPerOp(func() { _, _ = httpx.Do(t.Context(), hc, req) })
			t.Logf("%d bytes allocated for a %d-byte body", perOp, len(payload))
			assert.Less(t, float64(perOp), tt.maxFactor*float64(len(payload)))
		})
	}
}

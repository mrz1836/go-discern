package clef_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
	"github.com/mrz1836/go-discern/internal/testutil"
)

func TestAllocationsStayBounded(t *testing.T) {
	testutil.SkipUnderRace(t)
	c := okTransport(envelope(ticketResult(clef.ModelClef))).client(t)
	req := ticketRequest()
	call, err := c.Prepare(&req)
	require.NoError(t, err)
	ctx := t.Context()

	// Ceilings sit about 25% above the measured counts. The call's count
	// includes the in-memory transport's own response, header and body reader.
	testutil.CheckCeilings(t, []testutil.Ceiling{
		{Name: "Prepare", Max: 4, Run: func() { _, _ = c.Prepare(&req) }},
		{Name: "Call", Max: 57, Run: func() { _, _ = call(ctx) }},
	})
}

func TestCallsLeaveNoGoroutinesBehind(t *testing.T) {
	// Each call runs in a synctest bubble with an in-memory transport. A call
	// that started a goroutine and left it blocked would deadlock the bubble
	// and fail the test.
	tests := []struct {
		name     string
		rt       http.RoundTripper
		cancel   bool
		wantKind discern.FailureKind // empty for success
	}{
		{name: "success", rt: okTransport(envelope(outageResult))},
		{name: "rejected", rt: &countingTransport{status: http.StatusTooManyRequests, header: metadataHeader(), body: capacityEnvelope}, wantKind: discern.FailureRejected},
		{name: "refused on HTTP 200", rt: okTransport(capacityEnvelope), wantKind: discern.FailureRejected},
		{name: "malformed", rt: okTransport(envelope(`{"answers":{}}`)), wantKind: discern.FailureMalformed},
		{name: "too large", rt: okTransport(strings.Repeat(" ", 2048)), wantKind: discern.FailureTooLarge},
		{name: "transport error", rt: &countingTransport{err: errSyntheticReset}, wantKind: discern.FailureTransport},
		{name: "timeout", rt: &blockingTransport{}, wantKind: discern.FailureTimeout},
		{name: "canceled", rt: &blockingTransport{}, cancel: true, wantKind: discern.FailureCanceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, err := clef.New(testAccount, testToken,
					clef.WithHTTPClient(&http.Client{Transport: tt.rt}), clef.WithTimeout(time.Second), clef.WithMaxResponseBytes(1024))
				require.NoError(t, err)
				req := noulRequest()
				call, err := c.Prepare(&req)
				require.NoError(t, err)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tt.cancel {
					time.AfterFunc(100*time.Millisecond, cancel)
				}

				obs, err := call(ctx)
				if tt.wantKind == "" {
					require.NoError(t, err)
					require.NotNil(t, obs)
					assert.Equal(t, testRequestID, obs.RequestID)
					assert.Equal(t, testRayID, obs.RayID)
					return
				}
				assert.Nil(t, obs)
				assert.Equal(t, tt.wantKind, testutil.ProviderError(t, err).Kind)
			})
		})
	}
}

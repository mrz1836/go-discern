package jev_test

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
	"github.com/mrz1836/go-discern/internal/testutil"
	"github.com/mrz1836/go-discern/jev"
)

func TestAllocationsStayBounded(t *testing.T) {
	testutil.SkipUnderRace(t)
	c := inMemory(t, respond(http.StatusOK, ticketResponse))
	req := ticketRequest()
	call, err := c.Prepare(&req)
	require.NoError(t, err)
	ctx := t.Context()

	// Ceilings sit about 25% above the measured counts. The call's count
	// includes the in-memory transport's own response, header and body reader.
	testutil.CheckCeilings(t, []testutil.Ceiling{
		{Name: "Prepare", Max: 4, Run: func() { _, _ = c.Prepare(&req) }},
		{Name: "Call", Max: 53, Run: func() { _, _ = call(ctx) }},
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
		{name: "success", rt: respond(http.StatusOK, ticketResponse)},
		{name: "rejected", rt: respond(http.StatusServiceUnavailable, `{}`, "Retry-After", "1"), wantKind: discern.FailureRejected},
		{name: "malformed", rt: respond(http.StatusOK, `{"answers":{}}`), wantKind: discern.FailureMalformed},
		{name: "too large", rt: respond(http.StatusOK, strings.Repeat(" ", 2048)), wantKind: discern.FailureTooLarge},
		{name: "transport error", rt: testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errConnectionReset }), wantKind: discern.FailureTransport},
		{name: "timeout", rt: testutil.RoundTripFunc(testutil.BlockUntilDone), wantKind: discern.FailureTimeout},
		{name: "canceled", rt: testutil.RoundTripFunc(testutil.BlockUntilDone), cancel: true, wantKind: discern.FailureCanceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := inMemory(t, tt.rt, jev.WithTimeout(time.Second), jev.WithMaxResponseBytes(1024))
				req := ticketRequest()
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
					assert.Equal(t, ticketObservation(testRequestID), obs)
					return
				}
				assert.Nil(t, obs)
				assert.Equal(t, tt.wantKind, testutil.ProviderError(t, err).Kind)
			})
		})
	}
}

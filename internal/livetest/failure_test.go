//go:build live

package livetest_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
	"github.com/mrz1836/go-discern/internal/testutil"
	"github.com/mrz1836/go-discern/jev"
)

// errorTests checks how each provider refuses a call and how timeouts and
// oversized responses are reported.
func (s *suite) errorTests(t *testing.T) {
	s.run(t, "jev/bad-key", needJev, s.errJevBadKey)
	// SDK examples use "jev" and "jev-1.13", but the API refuses both, as it
	// refuses an unknown version.
	for _, model := range []string{"jev-0.0.1", "jev", "jev-1.13"} {
		s.run(t, "jev/unknown-model/"+model, needJev, func(t *testing.T) { s.errJevUnknownModel(t, model) })
	}
	s.run(t, "jev/adapter-timeout", needJev, func(t *testing.T) {
		res, err := s.evaluate(t, evaluator(t, s.jev(t, jev.WithTimeout(time.Millisecond))), refundRequest(discern.JevOnly, ticket))
		assertTimeout(t, res, err, discern.Jev)
	})
	s.run(t, "jev/injected-client-timeout", needJev, func(t *testing.T) {
		c := s.jev(t, jev.WithHTTPClient(&http.Client{Timeout: 5 * time.Millisecond}))
		res, err := s.evaluate(t, evaluator(t, c), refundRequest(discern.JevOnly, ticket))
		assertTimeout(t, res, err, discern.Jev)
	})
	s.run(t, "jev/response-too-large", needJev, s.errJevResponseTooLarge)
	s.run(t, "clef/bad-token", needClef, func(t *testing.T) {
		s.errClefAuth(t, newClef(t, s.account, badClefToken, clef.ModelFlash))
	})
	s.run(t, "clef/wrong-account", needClef, func(t *testing.T) {
		s.errClefAuth(t, newClef(t, wrongAccount, s.token, clef.ModelFlash))
	})
	s.run(t, "clef/adapter-timeout", needClef, func(t *testing.T) {
		res, err := s.evaluate(t, evaluator(t, s.clef(t, clef.ModelFlash, clef.WithTimeout(time.Millisecond))),
			refundRequest(discern.ClefOnly, ticket))
		assertTimeout(t, res, err, discern.Clef)
	})
	s.run(t, "clef/caller-deadline", needClef, s.errClefCallerDeadline)
}

func (s *suite) errJevBadKey(t *testing.T) {
	res, err := s.evaluate(t, evaluator(t, newJev(t, badJevKey)), refundRequest(discern.JevOnly, ticket))
	f := requireRejected(t, res, err, http.StatusUnauthorized)
	require.EqualError(t, err, "discern: jev: rejected (HTTP 401)")
	assert.NotEmpty(t, f.RequestID, changed(res, err))
	assert.Empty(t, f.RayID, changed(res, err))
}

func (s *suite) errJevUnknownModel(t *testing.T, model string) {
	res, err := s.evaluate(t, evaluator(t, s.jev(t, jev.WithModel(model))), refundRequest(discern.JevOnly, ticket))
	requireRejected(t, res, err, http.StatusBadRequest)
	assertMessage(t, res, err, "api_usage_error: Unknown model: "+model)
}

// errJevResponseTooLarge raises the response limit from below the smallest
// possible answer until the local precheck lets the call through; the real
// answer is larger, so the call fails as too large after it is billed.
func (s *suite) errJevResponseTooLarge(t *testing.T) {
	req := ticketRequest(discern.JevOnly)
	for limit := int64(64); limit <= 4096; limit += 16 {
		res, err := s.call(t.Context(), evaluator(t, s.jev(t, jev.WithMaxResponseBytes(limit))), req)
		if res == nil {
			require.ErrorIs(t, err, discern.ErrUnsupported, "limit %d", limit)
			continue
		}
		s.inspect(t, res, err)
		t.Logf("the first limit past the local precheck is %d bytes", limit)
		f := testutil.Failure(t, res)
		assert.Equal(t, discern.FailureTooLarge, f.Kind, changed(res, err))
		assert.Equal(t, http.StatusOK, f.StatusCode, changed(res, err))
		assert.NotEmpty(t, f.RequestID, changed(res, err))
		return
	}
	t.Fatal("the local precheck refused every limit up to 4096 bytes")
}

// errClefAuth checks Cloudflare's refusal of bad credentials: HTTP 401, code
// 10000 and a ray ID, without a request ID.
func (s *suite) errClefAuth(t *testing.T, c *clef.Client) {
	res, err := s.evaluate(t, evaluator(t, c), refundRequest(discern.ClefOnly, ticket))
	f := requireRejected(t, res, err, http.StatusUnauthorized)
	require.EqualError(t, err, "discern: clef: rejected (HTTP 401)")
	assert.Contains(t, f.Codes, clefAuthError, changed(res, err))
	assertMessage(t, res, err, "Authentication error")
	assert.NotEmpty(t, f.RayID, changed(res, err))
	assert.Empty(t, f.RequestID, changed(res, err))
}

func (s *suite) errClefCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer cancel()
	res, err := s.call(ctx, evaluator(t, s.clef(t, clef.ModelFlash)), refundRequest(discern.ClefOnly, ticket))
	s.inspect(t, res, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	f := testutil.Failure(t, res)
	assert.Equal(t, discern.FailureDeadlineExceeded, f.Kind)
	require.EqualError(t, err, "discern: clef: deadline_exceeded")
}

// assertTimeout checks a call that outlived its own timeout.
func assertTimeout(t *testing.T, res *discern.Result, err error, p discern.Provider) {
	t.Helper()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	f := testutil.Failure(t, res)
	assert.Equal(t, discern.FailureTimeout, f.Kind)
	require.EqualError(t, err, "discern: "+string(p)+": timeout")
}

//go:build live

package livetest_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
)

// largeTests probes how each model handles state near and past its context
// window, as observed in October 2026: clef and clef-flash refuse state that
// does not fit, clef-omni truncates it to its 64,000-token window, and Jev
// refuses state past its limit. They need DISCERN_LIVE_LARGE=1.
func (s *suite) largeTests(t *testing.T) {
	const (
		about31k = 1100 // log lines: about 31,000 tokens
		about73k = 2500 // log lines: about 73,000 tokens
	)
	tests := []struct {
		name, target string
		lines        int
		status, code int    // a refusal's HTTP status and Cloudflare code
		message      string // part of the refusal's provider message
		inputTokens  int64  // the exact input tokens reported, when known
	}{
		{name: "31k/jev", target: targetJev, lines: about31k},
		{name: "31k/clef", target: clef.ModelClef, lines: about31k},
		{
			name: "31k/clef-flash", target: clef.ModelFlash, lines: about31k,
			status: http.StatusUnprocessableEntity, code: 3030, message: "the maximum is 24576",
		},
		{name: "31k/clef-omni", target: clef.ModelOmni, lines: about31k},
		{name: "73k/jev", target: targetJev, lines: about73k, status: http.StatusBadRequest, message: "max_tokens_exceeded"},
		{
			name: "73k/clef", target: clef.ModelClef, lines: about73k,
			status: http.StatusUnprocessableEntity, code: 3030, message: "the maximum is 65536",
		},
		{
			name: "73k/clef-flash", target: clef.ModelFlash, lines: about73k,
			status: http.StatusRequestEntityTooLarge, code: 5021, message: "context window limit (24576)",
		},
		{name: "73k/clef-omni", target: clef.ModelOmni, lines: about73k, inputTokens: 64000},
	}
	for _, tt := range tests {
		s.run(t, tt.name, needLarge|needFor(tt.target), func(t *testing.T) {
			ev, ps, p := s.target(t, tt.target)
			res, err := s.evaluate(t, ev, discern.Request{
				Providers: ps,
				State:     discern.Text(logLines(tt.lines)),
				Questions: []discern.Question{
					discern.Noul("export_bug", discern.Text("Do customers report an invoice export error?")),
				},
			})
			if tt.status != 0 {
				f := requireRejected(t, res, err, tt.status)
				if tt.code != 0 {
					assert.Contains(t, f.Codes, tt.code, changed(res, err))
				}
				assertMessage(t, res, err, tt.message)
				return
			}
			requireComplete(t, res, err)
			assertYes(t, res, p, "export_bug")
			if tt.inputTokens != 0 {
				obs := requireObservation(t, res, p)
				require.NotNil(t, obs.Usage, changed(res, err))
				require.NotNil(t, obs.Usage.InputTokens, changed(res, err))
				assert.Equal(t, tt.inputTokens, *obs.Usage.InputTokens, changed(res, err))
			}
		})
	}
}

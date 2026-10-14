package clef_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
	"github.com/mrz1836/go-discern/internal/testutil"
)

func FuzzClefEnvelope(f *testing.F) {
	f.Add(uint16(0), []byte(envelope(ticketResult(clef.ModelClef))))
	f.Add(uint16(0), []byte(envelope(outageResult)))
	f.Add(uint16(0), []byte(capacityEnvelope))
	f.Add(uint16(201), []byte(`{"result":null,"success":false,"errors":[{"code":10000,"message":"Authentication error"}],"messages":[]}`))
	f.Add(uint16(0), []byte(`{"success":true}`))
	f.Add(uint16(0), []byte(`{"success":true,"result":null}`))
	f.Add(uint16(0), []byte(`{"success":true,"errors":[{"code":1e400}]}`))
	f.Add(uint16(0), []byte(envelope(`{"answers":{"queue":{"type":"choice","choice":"billing","probabilities":{"billing":1e400}}}}`)))
	f.Add(uint16(0), []byte(envelope(`{"answers":{"severity":{"type":"score","score":-0,"probabilities":{"0":1},"legend":[]}}}`)))
	f.Add(uint16(300), []byte(`<html>`))
	f.Add(uint16(0), []byte(``))
	f.Fuzz(func(t *testing.T, status uint16, body []byte) {
		ct := &countingTransport{status: http.StatusOK + int(status%400), header: metadataHeader(), body: string(body)}
		c := ct.client(t)
		req := ticketRequest()
		call, err := c.Prepare(&req)
		require.NoError(t, err)
		require.NotPanics(t, func() {
			obs, callErr := call(t.Context())
			if callErr == nil {
				require.NotNil(t, obs)
				require.Len(t, obs.Answers, len(req.Questions))
				return
			}
			assert.Nil(t, obs)
			pe := testutil.ProviderError(t, callErr)
			assert.Contains(t, []discern.FailureKind{discern.FailureRejected, discern.FailureMalformed}, pe.Kind)
			assert.NotContains(t, callErr.Error(), testToken)
		})
		assert.Equal(t, int32(1), ct.attempts.Load())
	})
}

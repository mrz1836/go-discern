package jev_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/testutil"
	"github.com/mrz1836/go-discern/jev"
)

func FuzzJevResponse(f *testing.F) {
	seeds := []string{
		ticketResponse,
		sparseResponse,
		ticketResponse[:len(ticketResponse)/2],
		``,
		`null`,
		`{}`,
		`[]`,
		`{"answers":{}}`,
		`{"answers":null}`,
		`{"answers":{"queue":{"type":"choice"}}}`,
		`{"answers":{"queue":1,"outage":2,"severity":3}}`,
		`{"model":7,"answers":{},"usage":{"input_tokens":-1}}`,
		`{"answers":{},"answers":{}}`,
		"\xff\xfe",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	req := ticketRequest()
	f.Fuzz(func(t *testing.T, body []byte) {
		c, err := jev.New(testKey, jev.WithModel(pinnedModel), jev.WithHTTPClient(&http.Client{Transport: respond(http.StatusOK, string(body))}))
		require.NoError(t, err)
		call, err := c.Prepare(&req)
		require.NoError(t, err)
		require.NotPanics(t, func() {
			obs, callErr := call(t.Context())
			if callErr != nil {
				pe := testutil.ProviderError(t, callErr)
				require.Nil(t, obs)
				require.Equal(t, discern.Jev, pe.Provider)
				return
			}
			require.NotNil(t, obs)
			require.Len(t, obs.Answers, len(req.Questions))
			require.Equal(t, testRequestID, obs.RequestID)
			for i, a := range obs.Answers {
				require.Equal(t, req.Questions[i].ID, a.QuestionID)
				require.Equal(t, req.Questions[i].Kind, a.Kind)
			}
		})
	})
}

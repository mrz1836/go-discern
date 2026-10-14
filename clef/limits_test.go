package clef_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
	"github.com/mrz1836/go-discern/internal/testutil"
)

// wideChoiceRequest has questions Choice questions, each with options options
// whose IDs are idLength bytes long: within every documented limit, yet with a
// shortest possible answer of about questions*options*(idLength+5) bytes.
func wideChoiceRequest(questions, options, idLength int) discern.Request {
	qs := make([]discern.Question, questions)
	for i := range qs {
		opts := make([]discern.ChoiceOption, options)
		for j := range opts {
			id := fmt.Sprintf("%03d", j)
			opts[j] = discern.Opt(id+strings.Repeat("o", idLength-len(id)), discern.Text("d"))
		}
		qs[i] = discern.Choice(fmt.Sprintf("q%02d", i), discern.Text("Which one?"), opts...)
	}
	return discern.Request{Providers: discern.ClefOnly, State: discern.Text(ticketState), Questions: qs}
}

func TestPrepareRejectsRequestsWhoseAnswersCannotFitTheLimit(t *testing.T) {
	// The most questions, options and option ID bytes Clef and discern allow:
	// the shortest answer is about 4.3 MB, over the default 4 MiB limit.
	req := wideChoiceRequest(clef.MaxQuestions, discern.MaxChoiceOptions, discern.MaxIdentifierLength)

	t.Run("default limit makes no call", func(t *testing.T) {
		ct := &countingTransport{status: http.StatusServiceUnavailable, header: metadataHeader(), body: capacityEnvelope}
		c := ct.client(t)
		_, err := c.Prepare(&req)
		require.ErrorIs(t, err, discern.ErrUnsupported)
		ve := testutil.ValidationError(t, err)
		assert.Equal(t, "request", ve.Field)
		assert.Contains(t, ve.Reason, "WithMaxResponseBytes")

		res, err := testutil.Evaluate(t, c, req)
		assert.Nil(t, res)
		require.ErrorIs(t, err, discern.ErrUnsupported)
		assert.Equal(t, int32(0), ct.attempts.Load())
	})
	t.Run("a raised limit makes the call", func(t *testing.T) {
		ct := &countingTransport{status: http.StatusServiceUnavailable, header: metadataHeader(), body: capacityEnvelope}
		c, err := clef.New(testAccount, testToken, clef.WithHTTPClient(&http.Client{Transport: ct}), clef.WithMaxResponseBytes(16<<20))
		require.NoError(t, err)
		res, err := testutil.Evaluate(t, c, req)
		require.ErrorIs(t, err, discern.ErrRejected)
		require.NotNil(t, res)
		assert.Equal(t, int32(1), ct.attempts.Load())
	})
	t.Run("ordinary requests fit small limits", func(t *testing.T) {
		ticket := ticketRequest()
		ct := okTransport(envelope(ticketResult(clef.ModelClef)))
		c, err := clef.New(testAccount, testToken, clef.WithHTTPClient(&http.Client{Transport: ct}), clef.WithMaxResponseBytes(256))
		require.NoError(t, err)
		_, err = c.Prepare(&ticket)
		require.NoError(t, err)
		wide := wideChoiceRequest(8, discern.MaxChoiceOptions, discern.MaxIdentifierLength)
		_, err = ct.client(t).Prepare(&wide)
		require.NoError(t, err)
	})
}

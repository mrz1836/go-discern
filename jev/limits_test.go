package jev_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/testutil"
	"github.com/mrz1836/go-discern/jev"
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
	return discern.Request{Providers: discern.JevOnly, State: discern.Text("ticket"), Questions: qs}
}

func TestPrepareRejectsRequestsWhoseAnswersCannotFitTheLimit(t *testing.T) {
	// 64 questions of 255 options with 256-byte IDs: the shortest answer is
	// about 4.3 MB, over the default 4 MiB limit.
	req := wideChoiceRequest(64, discern.MaxChoiceOptions, discern.MaxIdentifierLength)

	t.Run("default limit makes no call", func(t *testing.T) {
		ct := &testutil.CountingTransport{Next: respond(http.StatusServiceUnavailable, `{}`)}
		c := inMemory(t, ct)
		_, err := c.Prepare(&req)
		require.ErrorIs(t, err, discern.ErrUnsupported)
		ve := testutil.ValidationError(t, err)
		assert.Equal(t, "request", ve.Field)
		assert.Contains(t, ve.Reason, "WithMaxResponseBytes")

		res, err := testutil.Evaluate(t, c, req)
		assert.Nil(t, res)
		require.ErrorIs(t, err, discern.ErrUnsupported)
		assert.Equal(t, 0, ct.Attempts())
	})
	t.Run("a raised limit makes the call", func(t *testing.T) {
		ct := &testutil.CountingTransport{Next: respond(http.StatusServiceUnavailable, `{}`)}
		c := inMemory(t, ct, jev.WithMaxResponseBytes(16<<20))
		res, err := testutil.Evaluate(t, c, req)
		require.ErrorIs(t, err, discern.ErrRejected)
		require.NotNil(t, res)
		assert.Equal(t, 1, ct.Attempts())
	})
	t.Run("ordinary requests fit small limits", func(t *testing.T) {
		ticket := ticketRequest()
		_, err := inMemory(t, respond(http.StatusOK, ticketResponse), jev.WithMaxResponseBytes(256)).Prepare(&ticket)
		require.NoError(t, err)
		wide := wideChoiceRequest(8, discern.MaxChoiceOptions, discern.MaxIdentifierLength)
		_, err = inMemory(t, respond(http.StatusOK, ticketResponse)).Prepare(&wide)
		require.NoError(t, err)
	})
}

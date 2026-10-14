package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
	"github.com/mrz1836/go-discern/jev"
)

var errTicketRefused = errors.New("ticket refused by the provider")

// testAccount is a well-formed, fake Cloudflare account ID.
const testAccount = "0123456789abcdef0123456789abcdef"

// refusing is an adapter that refuses, in Prepare, the request with one
// correlation ID and prepares every other request with the adapter it wraps.
type refusing struct {
	discern.Adapter

	correlationID string
}

// Prepare refuses the request with the configured correlation ID.
func (a refusing) Prepare(req *discern.Request) (discern.Call, error) {
	if req.CorrelationID == a.correlationID {
		return nil, errTicketRefused
	}
	return a.Adapter.Prepare(req)
}

// offline returns the program's offline Jev and Clef fakes.
func offline(t *testing.T) (jevFake, clefFake *discerntest.Fake) {
	t.Helper()
	adapters, err := providers(false)
	require.NoError(t, err)
	jevFake, ok := adapters[0].(*discerntest.Fake)
	require.True(t, ok)
	clefFake, ok = adapters[1].(*discerntest.Fake)
	require.True(t, ok)
	return jevFake, clefFake
}

// setLiveEnv sets the live opt-in and the provider credentials for one test.
func setLiveEnv(t *testing.T, live, apiKey, accountID, apiToken string) {
	t.Helper()
	t.Setenv("DISCERN_LIVE", live)
	t.Setenv("TYPESAFE_API_KEY", apiKey)
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", accountID)
	t.Setenv("CLOUDFLARE_API_TOKEN", apiToken)
}

func TestRunOffline(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, run(t.Context(), &out, false))
	got := out.String()
	for _, want := range []string{
		"status complete",
		"queue: accepted [providers_agree]",
		"severity: accepted [providers_agree]",
		"overall: accepted (not covered: [outage])",
		"replayed with routing-v4-strict: queue is below_threshold",
		`"schema_version": "discern.result/v1"`,
		`"redacted": [`,
		"ticket-1043 -> billing (0.87)",
	} {
		assert.Contains(t, got, want)
	}
	assert.NotContains(t, got, "Charged twice", "the record must not contain the ticket text")
	assert.NotContains(t, got, `"correlation_id": "ticket-1042"`)
}

func TestRunLiveRequiresOptIn(t *testing.T) {
	t.Setenv("DISCERN_LIVE", "")
	var out bytes.Buffer
	err := run(t.Context(), &out, true)
	require.ErrorIs(t, err, errLiveNotEnabled)
	assert.Empty(t, out.String())
}

func TestRunWithPartialFailure(t *testing.T) {
	jevFake, _ := offline(t)
	clefDown := discerntest.Failing(discern.Clef, nil)
	var out bytes.Buffer
	require.NoError(t, runWith(t.Context(), &out, [2]discern.Adapter{jevFake, clefDown}),
		"a partial failure still yields a usable result")
	got := out.String()
	for _, want := range []string{
		"status partial",
		"jev (jev-1.13.0):\n  queue billing [billing 0.91]",
		"clef (fake): failed: transport\n",
		"one provider failed; the other's observation is still usable\n",
		"replayed with routing-v4-strict: queue is ",
		`"status": "partial"`,
		"\nbatch:\n  failed: ",
	} {
		assert.Contains(t, got, want)
	}
	assert.Equal(t, 2, strings.Count(got, "\n  failed: "), "each batch ticket reports its own failure")
	assert.Equal(t, 1, jevFake.Calls(), "the batch runs on Clef alone")
	assert.Equal(t, 3, clefDown.Calls(), "one call for the ticket and one per batch ticket")
}

func TestRunWithBothProvidersFailed(t *testing.T) {
	jevDown, clefDown := discerntest.Failing(discern.Jev, nil), discerntest.Failing(discern.Clef, nil)
	var out bytes.Buffer
	err := runWith(t.Context(), &out, [2]discern.Adapter{jevDown, clefDown})
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "both providers failed: "), err.Error())
	pe := testutil.ProviderError(t, err)
	assert.Equal(t, discern.FailureTransport, pe.Kind)
	require.NotErrorIs(t, err, discern.ErrPartialFailure)

	got := out.String()
	for _, want := range []string{
		"status failed",
		"jev (fake): failed: transport\n",
		"clef (fake): failed: transport\n",
		"overall: ",
	} {
		assert.Contains(t, got, want, "the failed result is reported before the error is returned")
	}
	assert.NotContains(t, got, "replayed with")
	assert.NotContains(t, got, "batch:")
	assert.Equal(t, 1, jevDown.Calls())
	assert.Equal(t, 1, clefDown.Calls())
}

func TestRunWithNothingSentScenarios(t *testing.T) {
	tests := []struct {
		name   string
		cancel bool   // cancel the context before the run
		refuse string // correlation ID the Jev adapter refuses to prepare
		want   []error
	}{
		{"context already canceled", true, "", []error{context.Canceled}},
		{"adapter refuses the ticket", false, "ticket-1042", []error{discern.ErrUnsupported, errTicketRefused}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			if tt.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			jevFake, clefFake := offline(t)
			var out bytes.Buffer
			err := runWith(ctx, &out, [2]discern.Adapter{refusing{Adapter: jevFake, correlationID: tt.refuse}, clefFake})
			for _, want := range tt.want {
				require.ErrorIs(t, err, want)
			}
			assert.True(t, strings.HasPrefix(err.Error(), "nothing was sent: "), err.Error())
			assert.Empty(t, out.String(), "there is no result to report")
			assert.Zero(t, jevFake.Calls())
			assert.Zero(t, clefFake.Calls())
		})
	}
}

func TestRunWithRefusedBatchMakesNoBatchCall(t *testing.T) {
	jevFake, clefFake := offline(t)
	var out bytes.Buffer
	err := runWith(t.Context(), &out, [2]discern.Adapter{jevFake, refusing{Adapter: clefFake, correlationID: "ticket-1044"}})
	require.ErrorIs(t, err, discern.ErrUnsupported)
	require.ErrorIs(t, err, errTicketRefused)
	assert.Contains(t, err.Error(), "batch item 1")
	got := out.String()
	assert.Contains(t, got, "record (", "the single ticket completes before the batch")
	assert.NotContains(t, got, "batch:")
	assert.Equal(t, 1, jevFake.Calls())
	assert.Equal(t, 1, clefFake.Calls(), "a batch with an item the adapter refuses makes no call at all")
}

func TestRunWithInvalidAdapters(t *testing.T) {
	jevFake, _ := offline(t)
	var out bytes.Buffer
	err := runWith(t.Context(), &out, [2]discern.Adapter{jevFake, discerntest.Static(discern.Jev, discern.Observation{})})
	require.ErrorIs(t, err, discern.ErrInvalidConfig)
	assert.Empty(t, out.String())
	assert.Zero(t, jevFake.Calls())
}

func TestProbabilityScenarios(t *testing.T) {
	probs := []discern.OptionProbability{
		{Option: "billing", Probability: 0.87},
		{Option: "technical", Probability: 0.13},
	}
	tests := []struct {
		name   string
		answer discern.ChoiceAnswer
		want   float64
	}{
		{"chosen option listed first", discern.ChoiceAnswer{Option: "billing", Probabilities: probs}, 0.87},
		{"chosen option listed later", discern.ChoiceAnswer{Option: "technical", Probabilities: probs}, 0.13},
		{"chosen option not listed", discern.ChoiceAnswer{Option: "account", Probabilities: probs}, 0},
		{"no probabilities", discern.ChoiceAnswer{Option: "billing"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, probability(tt.answer), 0)
		})
	}
}

func TestProvidersLiveRejectsInvalidSetup(t *testing.T) {
	tests := []struct {
		name                      string
		live, key, account, token string
		want                      error
		field                     string // the rejected configuration field, if any
	}{
		{"opt-in must be exactly 1", "true", "test-key", testAccount, "test-token", errLiveNotEnabled, ""},
		{"missing API key", "1", "", testAccount, "test-token", discern.ErrInvalidConfig, "api_key"},
		{"malformed account ID", "1", "test-key", "account-1", "test-token", discern.ErrInvalidConfig, "account_id"},
		{"missing API token", "1", "test-key", testAccount, "", discern.ErrInvalidConfig, "api_token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setLiveEnv(t, tt.live, tt.key, tt.account, tt.token)
			adapters, err := providers(true)
			require.ErrorIs(t, err, tt.want)
			assert.Equal(t, [2]discern.Adapter{}, adapters)
			if tt.field != "" {
				ve := testutil.ValidationError(t, err)
				assert.Equal(t, tt.field, ve.Field)
			}
		})
	}
}

// TestProvidersLiveBuildsClients only builds the live clients: their
// constructors make no calls, and this test never prepares or sends a request.
func TestProvidersLiveBuildsClients(t *testing.T) {
	setLiveEnv(t, "1", "test-key", testAccount, "test-token")
	adapters, err := providers(true)
	require.NoError(t, err)
	require.IsType(t, &jev.Client{}, adapters[0])
	require.IsType(t, &clef.Client{}, adapters[1])
	assert.Equal(t, discern.Jev, adapters[0].Provider())
	assert.Equal(t, "jev-1.13.0", adapters[0].Model())
	assert.Equal(t, discern.Clef, adapters[1].Provider())
	assert.Equal(t, clef.ModelFlash, adapters[1].Model())
}

func TestRealMainScenarios(t *testing.T) {
	t.Setenv("DISCERN_LIVE", "")
	tests := []struct {
		name           string
		args           []string
		code           int
		stdout, stderr string // a substring of each stream; "" means the stream is empty
	}{
		{"offline run", []string{"examples"}, 0, "overall: accepted (not covered: [outage])", ""},
		{"live run without opt-in", []string{"examples", "-live"}, 1, "", errLiveNotEnabled.Error() + "\n"},
		{"unknown flag", []string{"examples", "-verbose"}, 2, "", "flag provided but not defined: -verbose"},
		{"help", []string{"examples", "-h"}, 0, "", "Usage of examples:\n  -live\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			assert.Equal(t, tt.code, realMain(tt.args, &stdout, &stderr))
			for _, s := range []struct{ got, want string }{{stdout.String(), tt.stdout}, {stderr.String(), tt.stderr}} {
				if s.want == "" {
					assert.Empty(t, s.got)
				} else {
					assert.Contains(t, s.got, s.want)
				}
			}
		})
	}
}

//go:build live

package livetest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
	"github.com/mrz1836/go-discern/internal/testutil"
	"github.com/mrz1836/go-discern/jev"
)

// Prices as of October 2026: Jev bills input tokens only ($0.042 per million;
// output is free), and Workers AI bills neurons ($0.011 per 1,000).
const (
	jevUSDPerInputToken = 0.042 / 1e6
	clefUSDPerNeuron    = 0.011 / 1000
	defaultMaxUSD       = 0.10
)

// Planted credentials. The bad ones are sent on purpose and must be refused;
// the placeholders build adapters for tests that must fail before any call.
const (
	badJevKey      = "tsk_live_invalid_0000000000000000000000000000"
	badClefToken   = "invalid-cloudflare-token-000000000000000000000"
	wrongAccount   = "0123456789abcdef0123456789abcdef"
	placeholderKey = "placeholder-credential-never-sent"
)

// need lists what a test requires beyond DISCERN_LIVE=1.
type need uint8

const (
	needJev need = 1 << iota
	needClef
	needLarge

	needBoth = needJev | needClef
)

// suite is one run of the live tests: the credentials, the budget and what the
// providers have reported so far. Its counters are safe for concurrent use.
type suite struct {
	apiKey, account, token string
	large                  bool
	maxUSD                 float64

	mu        sync.Mutex
	calls     int
	jevTokens int64
	neurons   float64
	limited   map[discern.Provider]string

	// policyResult is the dual/policy result that dual/record replays.
	policyResult *discern.Result
}

// newSuite applies the run-wide gates, skipping t unless live tests were
// enabled on purpose, and reads the credentials and budget.
func newSuite(t *testing.T) *suite {
	t.Helper()
	if os.Getenv("DISCERN_LIVE") != "1" {
		t.Skip("live tests make billable provider calls: set DISCERN_LIVE=1 to run them (credentials alone never do)")
	}
	if os.Getenv("CI") != "" && os.Getenv("DISCERN_LIVE_ALLOW_CI") != "1" {
		t.Skip("CI is set: also set DISCERN_LIVE_ALLOW_CI=1 to run live tests in CI")
	}
	maxUSD := defaultMaxUSD
	if v := os.Getenv("DISCERN_LIVE_MAX_USD"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
			t.Skipf("DISCERN_LIVE_MAX_USD=%q must be a positive number of US dollars, such as 0.10", v)
		}
		maxUSD = f
	}
	return &suite{
		apiKey:  os.Getenv("TYPESAFE_API_KEY"),
		account: os.Getenv("CLOUDFLARE_ACCOUNT_ID"),
		token:   os.Getenv("CLOUDFLARE_API_TOKEN"),
		large:   os.Getenv("DISCERN_LIVE_LARGE") == "1",
		maxUSD:  maxUSD,
		limited: map[discern.Provider]string{},
	}
}

// TestLive runs the live contract suite. See the package documentation for
// the gates and the cost.
func TestLive(t *testing.T) {
	s := newSuite(t)
	t.Cleanup(func() { t.Log(s.summary()) })
	t.Run("local", s.localTests)
	t.Run("jev", s.jevTests)
	t.Run("clef", s.clefTests)
	t.Run("dual", s.dualTests)
	t.Run("limits", s.limitTests)
	t.Run("err", s.errorTests)
	t.Run("large", s.largeTests)
}

// run runs f as the subtest name once the gates for n pass.
func (s *suite) run(t *testing.T, name string, n need, f func(t *testing.T)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		s.gate(t, n)
		f(t)
	})
}

// gate skips t when a credential, the large tier, the budget or a provider's
// rate limit stands in the way of what n needs.
func (s *suite) gate(t *testing.T, n need) {
	t.Helper()
	if n&needJev != 0 && s.apiKey == "" {
		t.Skip("set TYPESAFE_API_KEY to run the live Jev tests")
	}
	if n&needClef != 0 && (s.account == "" || s.token == "") {
		t.Skip("set CLOUDFLARE_ACCOUNT_ID and CLOUDFLARE_API_TOKEN to run the live Clef tests")
	}
	if n&needLarge != 0 && !s.large {
		t.Skip("the large-state probes cost the most: set DISCERN_LIVE_LARGE=1 to run them")
	}
	if reason := s.blocked(n); reason != "" {
		t.Skip(reason)
	}
}

// blocked returns why a test that calls the providers in n must not run: the
// budget is spent or a provider rate-limited the run. It is empty otherwise.
func (s *suite) blocked(n need) string {
	if n&needBoth == 0 {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if spent := s.spentLocked(); spent > s.maxUSD {
		return fmt.Sprintf("estimated spend ~%s is past the %s budget: raise DISCERN_LIVE_MAX_USD to run the rest", usd(spent), usd(s.maxUSD))
	}
	if n&needJev != 0 && s.limited[discern.Jev] != "" {
		return s.limited[discern.Jev]
	}
	if n&needClef != 0 && s.limited[discern.Clef] != "" {
		return s.limited[discern.Clef]
	}
	return ""
}

func (s *suite) spentLocked() float64 {
	return float64(s.jevTokens)*jevUSDPerInputToken + s.neurons*clefUSDPerNeuron
}

func (s *suite) summary() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("live spend: %d provider calls; Jev input tokens %d (~%s); Clef neurons %.2f (~%s); estimated ~%s of the %s budget",
		s.calls, s.jevTokens, usd(float64(s.jevTokens)*jevUSDPerInputToken), s.neurons, usd(s.neurons*clefUSDPerNeuron),
		usd(s.spentLocked()), usd(s.maxUSD))
}

// call evaluates req and records the calls, usage and any rate limit. It is
// safe for concurrent use and does not touch the test.
func (s *suite) call(ctx context.Context, ev *discern.Evaluator, req discern.Request, opts ...discern.EvalOption) (*discern.Result, error) {
	res, err := ev.Evaluate(ctx, req, opts...)
	s.record(res)
	return res, err
}

func (s *suite) record(res *discern.Result) {
	if res == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls += len(res.Outcomes)
	for _, o := range res.Outcomes {
		if f := o.Failure; f != nil && f.StatusCode == http.StatusTooManyRequests {
			s.limited[o.Provider] = rateLimitReason(o.Provider, f)
		}
		if o.Observation == nil || o.Observation.Usage == nil {
			continue
		}
		u := o.Observation.Usage
		if o.Provider == discern.Jev && u.InputTokens != nil {
			s.jevTokens += *u.InputTokens
		}
		if o.Provider == discern.Clef && u.Neurons != nil {
			s.neurons += *u.Neurons
		}
	}
}

// evaluate evaluates req on the test's context, checks the outcome for leaked
// credentials, logs its metadata, and skips the test if a provider answered
// HTTP 429.
func (s *suite) evaluate(t *testing.T, ev *discern.Evaluator, req discern.Request, opts ...discern.EvalOption) (*discern.Result, error) {
	t.Helper()
	res, err := s.call(t.Context(), ev, req, opts...)
	s.inspect(t, res, err)
	return res, err
}

// inspect checks a finished evaluation for leaked credentials, logs its
// metadata, and skips the test if a provider answered HTTP 429.
func (s *suite) inspect(t *testing.T, res *discern.Result, err error) {
	t.Helper()
	s.checkLeaks(t, res, err)
	t.Log(describe(res, err))
	if res == nil {
		return
	}
	for _, o := range res.Outcomes {
		if f := o.Failure; f != nil && f.StatusCode == http.StatusTooManyRequests {
			t.Skip(rateLimitReason(o.Provider, f))
		}
	}
}

// checkLeaks fails t if a credential, real or planted, appears in err, its
// provider messages, or res under any fmt verb, in hex, or in res's JSON.
func (s *suite) checkLeaks(t *testing.T, res *discern.Result, err error) {
	t.Helper()
	secrets := []string{badJevKey, badClefToken}
	for _, c := range []string{s.apiKey, s.token, s.account} {
		if c != "" {
			secrets = append(secrets, c)
		}
	}
	testutil.AssertNeverPrints(t, err, secrets...)
	testutil.AssertRedacted(t, strings.Join(providerMessages(err), "\n"), secrets, "provider messages")
	if res == nil {
		return
	}
	testutil.AssertNeverPrints(t, res, secrets...)
	b, jerr := json.Marshal(res)
	require.NoError(t, jerr)
	testutil.AssertRedacted(t, string(b), secrets, "result JSON")
}

// usd formats an amount in US dollars to the nearest millionth.
func usd(x float64) string {
	return "$" + strconv.FormatFloat(math.Round(x*1e6)/1e6, 'f', -1, 64)
}

func rateLimitReason(p discern.Provider, f *discern.Failure) string {
	retry := "no retry hint"
	if f.RetryAfter != nil {
		retry = "retry after " + f.RetryAfter.String()
	}
	return fmt.Sprintf("%s answered HTTP 429 (%s): skipping its remaining live tests", p, retry)
}

// Adapters. Jev clients pin a versioned model unless opts choose another.

func (s *suite) jev(t *testing.T, opts ...jev.Option) *jev.Client {
	t.Helper()
	return newJev(t, s.apiKey, opts...)
}

func (s *suite) clef(t *testing.T, model string, opts ...clef.Option) *clef.Client {
	t.Helper()
	return newClef(t, s.account, s.token, model, opts...)
}

func newJev(t *testing.T, apiKey string, opts ...jev.Option) *jev.Client {
	t.Helper()
	c, err := jev.New(apiKey, append([]jev.Option{jev.WithModel(pinnedJev)}, opts...)...)
	require.NoError(t, err)
	return c
}

func newClef(t *testing.T, account, token, model string, opts ...clef.Option) *clef.Client {
	t.Helper()
	c, err := clef.New(account, token, append([]clef.Option{clef.WithModel(model)}, opts...)...)
	require.NoError(t, err)
	return c
}

func evaluator(t *testing.T, adapters ...discern.Adapter) *discern.Evaluator {
	t.Helper()
	opts := make([]discern.Option, 0, len(adapters))
	for _, a := range adapters {
		opts = append(opts, discern.WithAdapter(a))
	}
	return testutil.Evaluator(t, opts...)
}

// providerMessages returns the provider's own error texts carried by err.
func providerMessages(err error) []string {
	if pe, ok := errors.AsType[*discern.ProviderError](err); ok && pe != nil {
		return pe.ProviderMessages()
	}
	return nil
}

// changed annotates an assertion about provider behavior, so its failure reads
// as a possible contract change with what was observed.
func changed(res *discern.Result, err error) string {
	return "provider behavior changed? observed: " + describe(res, err)
}

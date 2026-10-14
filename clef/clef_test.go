package clef_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/clef"
	"github.com/mrz1836/go-discern/internal/testutil"
)

func TestNewRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		account string
		token   string
		opts    []clef.Option
		field   string
		reason  string // the exact reason, when the row checks it
		echo    string // an input that must not appear in the error
	}{
		{name: "empty account ID", token: testToken, field: "account_id"},
		{name: "short account ID", account: testAccount[:31], token: testToken, field: "account_id", echo: testAccount[:31]},
		{name: "long account ID", account: testAccount + "0", token: testToken, field: "account_id", echo: testAccount + "0"},
		{name: "non-hex account ID", account: "0123456789abcdef0123456789abcdeg", token: testToken, field: "account_id", echo: "0123456789abcdef0123456789abcdeg"},
		{name: "account ID with slash", account: "0123456789abcdef/123456789abcdef", token: testToken, field: "account_id", echo: "0123456789abcdef/123456789abcdef"},
		{name: "account ID with dot-dot", account: "0123456789abcdef0123456789abcd..", token: testToken, field: "account_id", echo: "0123456789abcdef0123456789abcd.."},
		{name: "empty token", account: testAccount, field: "api_token"},
		{name: "token with space", account: testAccount, token: "test token", field: "api_token", echo: "test token"},
		{name: "token with newline", account: testAccount, token: "test-token\n", field: "api_token", echo: "test-token\n"},
		{name: "model unknown", account: testAccount, token: testToken, opts: []clef.Option{clef.WithModel("clef-pro")}, field: "model", reason: "must be clef, clef-flash or clef-omni", echo: "clef-pro"},
		{name: "model with white space", account: testAccount, token: testToken, opts: []clef.Option{clef.WithModel("\tclef-omni")}, field: "model", echo: "\tclef-omni"},
		{name: "model full name", account: testAccount, token: testToken, opts: []clef.Option{clef.WithModel("@cf/cloudflare/clef")}, field: "model", echo: "@cf/cloudflare/clef"},
		{name: "model wrong case", account: testAccount, token: testToken, opts: []clef.Option{clef.WithModel("Clef")}, field: "model", echo: "Clef"},
		{name: "model empty", account: testAccount, token: testToken, opts: []clef.Option{clef.WithModel("")}, field: "model"},
		{name: "timeout zero", account: testAccount, token: testToken, opts: []clef.Option{clef.WithTimeout(0)}, field: "timeout"},
		{name: "timeout negative", account: testAccount, token: testToken, opts: []clef.Option{clef.WithTimeout(-time.Second)}, field: "timeout"},
		{name: "max response bytes zero", account: testAccount, token: testToken, opts: []clef.Option{clef.WithMaxResponseBytes(0)}, field: "max_response_bytes"},
		{name: "max response bytes negative", account: testAccount, token: testToken, opts: []clef.Option{clef.WithMaxResponseBytes(-1)}, field: "max_response_bytes"},
		{name: "max response bytes over limit", account: testAccount, token: testToken, opts: []clef.Option{clef.WithMaxResponseBytes(64<<20 + 1)}, field: "max_response_bytes"},
		{name: "endpoint http", account: testAccount, token: testToken, opts: []clef.Option{clef.WithTestEndpoint("http://127.0.0.1:1")}, field: "endpoint", echo: "http://127.0.0.1:1"},
		{name: "endpoint localhost", account: testAccount, token: testToken, opts: []clef.Option{clef.WithTestEndpoint("https://localhost:1")}, field: "endpoint", echo: "localhost"},
		{name: "endpoint production", account: testAccount, token: testToken, opts: []clef.Option{clef.WithTestEndpoint("https://api.cloudflare.com")}, field: "endpoint", echo: "api.cloudflare.com"},
		{name: "endpoint with path", account: testAccount, token: testToken, opts: []clef.Option{clef.WithTestEndpoint("https://127.0.0.1:1/client/v4")}, field: "endpoint", echo: "/client/v4"},
		{name: "endpoint empty", account: testAccount, token: testToken, opts: []clef.Option{clef.WithTestEndpoint("")}, field: "endpoint"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := clef.New(tt.account, tt.token, tt.opts...)
			assert.Nil(t, c)
			require.ErrorIs(t, err, discern.ErrInvalidConfig)
			ve := testutil.ValidationError(t, err)
			assert.Equal(t, tt.field, ve.Field)
			assert.NotEmpty(t, ve.Reason)
			if tt.reason != "" {
				assert.Equal(t, tt.reason, ve.Reason)
			}
			if tt.echo != "" {
				assert.NotContains(t, err.Error(), tt.echo)
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	c, err := clef.New(testAccount, testToken)
	require.NoError(t, err)
	assert.Equal(t, clef.ModelClef, c.Model())
	assert.Equal(t, discern.Clef, c.Provider())
	assert.Equal(t, `clef.Client{model: "clef"}`, c.String())
	assert.Equal(t, `clef.Client{model: "clef"}`, c.GoString())
}

func TestNewAppliesOptionsInOrder(t *testing.T) {
	tests := []struct {
		name  string
		opts  []clef.Option
		model string
	}{
		{name: "nil options skipped", opts: []clef.Option{nil, clef.WithModel(clef.ModelFlash), nil}, model: clef.ModelFlash},
		{name: "last model wins", opts: []clef.Option{clef.WithModel(clef.ModelFlash), clef.WithModel(clef.ModelClef)}, model: clef.ModelClef},
		{name: "omni model", opts: []clef.Option{clef.WithModel(clef.ModelOmni)}, model: clef.ModelOmni},
		{name: "invalid model replaced", opts: []clef.Option{clef.WithModel("clef-pro"), clef.WithModel(clef.ModelFlash)}, model: clef.ModelFlash},
		{name: "invalid timeout replaced", opts: []clef.Option{clef.WithTimeout(0), clef.WithTimeout(time.Second)}, model: clef.ModelClef},
		{name: "invalid max response bytes replaced", opts: []clef.Option{clef.WithMaxResponseBytes(0), clef.WithMaxResponseBytes(1 << 10)}, model: clef.ModelClef},
		{name: "largest max response bytes", opts: []clef.Option{clef.WithMaxResponseBytes(64 << 20)}, model: clef.ModelClef},
		{name: "invalid endpoint replaced", opts: []clef.Option{clef.WithTestEndpoint("http://127.0.0.1:1"), clef.WithTestEndpoint("https://127.0.0.1:1")}, model: clef.ModelClef},
		{name: "ipv6 loopback endpoint", opts: []clef.Option{clef.WithTestEndpoint("https://[::1]:8443/")}, model: clef.ModelClef},
		{name: "nil http client", opts: []clef.Option{clef.WithHTTPClient(nil)}, model: clef.ModelClef},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := clef.New(testAccount, testToken, tt.opts...)
			require.NoError(t, err)
			assert.Equal(t, tt.model, c.Model())
			assert.Equal(t, discern.Clef, c.Provider())
		})
	}
}

func TestNewLastTransportAndLimitWin(t *testing.T) {
	first := okTransport(envelope(outageResult))
	second := okTransport(envelope(outageResult))
	// 64 bytes fit the shortest answer to one Noul question, so Prepare lets
	// the call through, but not this response.
	require.Greater(t, len(envelope(outageResult)), 64)
	c, err := clef.New(testAccount, testToken,
		clef.WithHTTPClient(&http.Client{Transport: first}),
		clef.WithHTTPClient(&http.Client{Transport: second}),
		clef.WithMaxResponseBytes(1<<20),
		clef.WithMaxResponseBytes(64),
	)
	require.NoError(t, err)
	_, err = testutil.Evaluate(t, c, noulRequest())
	assert.Equal(t, discern.FailureTooLarge, testutil.ProviderError(t, err).Kind)
	assert.Equal(t, int32(0), first.attempts.Load())
	assert.Equal(t, int32(1), second.attempts.Load())
}

func TestNewLastEndpointWins(t *testing.T) {
	unused := newServer(t, okReply(envelope(outageResult)))
	used := newServer(t, okReply(envelope(outageResult)))
	c, err := clef.New(testAccount, testToken,
		clef.WithTestEndpoint(unused.URL()),
		clef.WithTestEndpoint(used.URL()),
		clef.WithHTTPClient(used.Client()),
	)
	require.NoError(t, err)
	_, err = testutil.Evaluate(t, c, noulRequest())
	require.NoError(t, err)
	assert.Equal(t, 0, unused.Hits())
	assert.Equal(t, 1, used.Hits())
}

func TestClientImplementsAdapter(t *testing.T) {
	c, err := clef.New(testAccount, testToken, clef.WithModel(clef.ModelFlash))
	require.NoError(t, err)
	var a discern.Adapter = c
	assert.Equal(t, discern.Clef, a.Provider())
	assert.Equal(t, clef.ModelFlash, a.Model())
	var v discern.Adapter = *c
	assert.Equal(t, clef.ModelFlash, v.Model())
}

func TestClientFormattingShowsOnlyModel(t *testing.T) {
	c, err := clef.New(testAccount, testToken, clef.WithModel(clef.ModelFlash))
	require.NoError(t, err)
	want := `clef.Client{model: "clef-flash"}`
	assert.Equal(t, want, c.String())
	assert.Equal(t, want, c.GoString())
	assert.Equal(t, want, fmt.Sprint(c))
	assert.Equal(t, want+" "+want, fmt.Sprintf("%+v %#v", c, *c))
	assert.Equal(t, want+"|"+want, fmt.Sprintf("%s|%v", *c, c))
	// Verbs that bypass String are covered in TestClientFormattingNeverLeaksSecrets.
}

func TestPrepareRejectsInvalidRequest(t *testing.T) {
	ct := okTransport(envelope(outageResult))
	c := ct.client(t)
	unknownKind := noulRequest()
	unknownKind.Questions[0].Kind = "rank"

	tests := []struct {
		name     string
		client   clef.Client
		req      *discern.Request
		sentinel error
		field    string
	}{
		{name: "nil request", client: *c, req: nil, sentinel: discern.ErrInvalidRequest, field: "request"},
		{name: "unencodable request", client: *c, req: &unknownKind, sentinel: discern.ErrInvalidRequest, field: "questions[0].kind"},
		{name: "zero client", client: clef.Client{}, req: new(noulRequest()), sentinel: discern.ErrInvalidConfig, field: "client"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			call, err := tt.client.Prepare(tt.req)
			assert.Nil(t, call)
			require.ErrorIs(t, err, tt.sentinel)
			ve := testutil.ValidationError(t, err)
			assert.Equal(t, tt.field, ve.Field)
			assert.Equal(t, int32(0), ct.attempts.Load())
		})
	}
}

// TestZeroClientFailsLocally shows that a zero Client prints its empty model and
// fails an evaluation before any call.
func TestZeroClientFailsLocally(t *testing.T) {
	var c clef.Client
	assert.Equal(t, `clef.Client{model: ""}`, c.String())

	res, err := testutil.Evaluator(t, discern.WithAdapter(c)).Evaluate(t.Context(), noulRequest())
	assert.Nil(t, res)
	require.ErrorIs(t, err, discern.ErrInvalidConfig)
	require.ErrorIs(t, err, discern.ErrUnsupported)
}

func TestPrepareMakesNoIO(t *testing.T) {
	ct := okTransport(envelope(outageResult))
	c := ct.client(t)
	req := noulRequest()
	call, err := c.Prepare(&req)
	require.NoError(t, err)
	require.NotNil(t, call)
	assert.Equal(t, int32(0), ct.attempts.Load())

	obs, err := call(t.Context())
	require.NoError(t, err)
	require.NotNil(t, obs)
	assert.Equal(t, int32(1), ct.attempts.Load())

	// Each invocation is its own single attempt.
	_, err = call(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int32(2), ct.attempts.Load())
}

func TestPrepareQuestionLimit(t *testing.T) {
	tests := []struct {
		name      string
		questions int
		wantErr   bool
	}{
		{name: "one question", questions: 1},
		{name: "exactly the limit", questions: clef.MaxQuestions},
		{name: "one over the limit", questions: clef.MaxQuestions + 1, wantErr: true},
		{name: "far over the limit", questions: 200, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct := okTransport(envelope(manyAnswers(tt.questions)))
			req := manyQuestions(tt.questions, discern.ClefOnly)
			call, err := ct.client(t).Prepare(&req)
			if !tt.wantErr {
				require.NoError(t, err)
				require.NotNil(t, call)
				assert.Equal(t, int32(0), ct.attempts.Load())
				return
			}
			assert.Nil(t, call)
			require.ErrorIs(t, err, discern.ErrUnsupported)
			ve := testutil.ValidationError(t, err)
			assert.Equal(t, "questions", ve.Field)
			assert.Equal(t, "clef accepts at most 64 questions", ve.Reason)
			assert.Equal(t, int32(0), ct.attempts.Load())
		})
	}
}

func TestPrepareRequestBodyLimit(t *testing.T) {
	// The encoded body of stateRequest with an empty text state.
	overhead := len(`{"model":"clef","state":"","questions":{"outage":{"type":"noul","instructions":"` + outageInstructions + `"}}}`)

	t.Run("exactly the limit", func(t *testing.T) {
		ct := okTransport(envelope(outageResult))
		req := stateRequest(discern.Text(strings.Repeat("a", clef.MaxRequestBytes-overhead)))
		call, err := ct.client(t).Prepare(&req)
		require.NoError(t, err)
		assert.Equal(t, int32(0), ct.attempts.Load())
		_, err = call(t.Context())
		require.NoError(t, err)
		assert.Equal(t, int32(1), ct.attempts.Load())
		assert.Equal(t, int64(clef.MaxRequestBytes), ct.bodyBytes.Load())
	})

	t.Run("one byte over the limit", func(t *testing.T) {
		ct := okTransport(envelope(outageResult))
		req := stateRequest(discern.Text(strings.Repeat("a", clef.MaxRequestBytes-overhead+1)))
		call, err := ct.client(t).Prepare(&req)
		assert.Nil(t, call)
		require.ErrorIs(t, err, discern.ErrUnsupported)
		ve := testutil.ValidationError(t, err)
		assert.Equal(t, "request", ve.Field)
		assert.Contains(t, ve.Reason, "13 MiB")
		assert.Equal(t, int32(0), ct.attempts.Load())
	})
}

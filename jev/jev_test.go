package jev_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/testutil"
	"github.com/mrz1836/go-discern/jev"
)

// maxResponseLimit mirrors the largest accepted response limit, 64 MiB.
const maxResponseLimit = 64 << 20

func TestClientIsAdapter(t *testing.T) {
	var _ discern.Adapter = (*jev.Client)(nil)
	var _ discern.Adapter = jev.Client{}

	var a discern.Adapter = inMemory(t, respond(http.StatusOK, ticketResponse))
	assert.Equal(t, discern.Jev, a.Provider())
	assert.Equal(t, pinnedModel, a.Model())
}

func TestNewDefaults(t *testing.T) {
	c, err := jev.New(testKey)
	require.NoError(t, err)
	assert.Equal(t, jev.DefaultModel, c.Model())
	assert.Equal(t, "jev-latest", c.Model())
	assert.Equal(t, discern.Jev, c.Provider())
	assert.Equal(t, `jev.Client{model: "jev-latest"}`, c.String())
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		opts  []jev.Option
		field string
		value string // must not appear in the error message
	}{
		{name: "empty key", key: "", field: "api_key"},
		{name: "key with space", key: "test key", field: "api_key", value: "test key"},
		{name: "key with newline", key: "test-key\n", field: "api_key", value: "test-key\n"},
		{name: "key with tab", key: "test\tkey", field: "api_key", value: "test\tkey"},
		{name: "key too long", key: strings.Repeat("k", 4097), field: "api_key", value: strings.Repeat("k", 4097)},
		{name: "empty model", key: testKey, opts: []jev.Option{jev.WithModel("")}, field: "model"},
		{name: "model too long", key: testKey, opts: []jev.Option{jev.WithModel(strings.Repeat("m", 129))}, field: "model", value: strings.Repeat("m", 129)},
		{name: "model with space", key: testKey, opts: []jev.Option{jev.WithModel("jev 1.13.0")}, field: "model", value: "jev 1.13.0"},
		{name: "model with slash", key: testKey, opts: []jev.Option{jev.WithModel("typesafe/jev")}, field: "model", value: "typesafe/jev"},
		{name: "zero timeout", key: testKey, opts: []jev.Option{jev.WithTimeout(0)}, field: "timeout"},
		{name: "negative timeout", key: testKey, opts: []jev.Option{jev.WithTimeout(-time.Second)}, field: "timeout"},
		{name: "zero max bytes", key: testKey, opts: []jev.Option{jev.WithMaxResponseBytes(0)}, field: "max_response_bytes"},
		{name: "negative max bytes", key: testKey, opts: []jev.Option{jev.WithMaxResponseBytes(-1)}, field: "max_response_bytes"},
		{name: "max bytes above limit", key: testKey, opts: []jev.Option{jev.WithMaxResponseBytes(maxResponseLimit + 1)}, field: "max_response_bytes"},
		{name: "empty endpoint", key: testKey, opts: []jev.Option{jev.WithTestEndpoint("")}, field: "endpoint"},
		{name: "http endpoint", key: testKey, opts: []jev.Option{jev.WithTestEndpoint("http://127.0.0.1:1")}, field: "endpoint", value: "http://127.0.0.1:1"},
		{name: "localhost endpoint", key: testKey, opts: []jev.Option{jev.WithTestEndpoint("https://localhost:1")}, field: "endpoint", value: "https://localhost:1"},
		{name: "public endpoint", key: testKey, opts: []jev.Option{jev.WithTestEndpoint("https://example.com")}, field: "endpoint", value: "https://example.com"},
		{name: "endpoint with path", key: testKey, opts: []jev.Option{jev.WithTestEndpoint("https://127.0.0.1:8443/v1")}, field: "endpoint", value: "https://127.0.0.1:8443/v1"},
		{name: "endpoint with userinfo", key: testKey, opts: []jev.Option{jev.WithTestEndpoint("https://user@127.0.0.1:8443")}, field: "endpoint", value: "user@"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := jev.New(tt.key, tt.opts...)
			assert.Nil(t, c)
			require.ErrorIs(t, err, discern.ErrInvalidConfig)
			ve := testutil.ValidationError(t, err)
			assert.Equal(t, tt.field, ve.Field)
			assert.NotEmpty(t, ve.Reason)
			if tt.value != "" {
				assert.NotContains(t, err.Error(), tt.value)
			}
		})
	}
}

func TestNewAcceptsBoundaryValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		opts  []jev.Option
		model string
	}{
		{name: "longest key", key: strings.Repeat("k", 4096), model: jev.DefaultModel},
		{name: "visible ASCII key", key: "!~test-key_0123456789", model: jev.DefaultModel},
		{name: "pinned model", key: testKey, opts: []jev.Option{jev.WithModel(pinnedModel)}, model: pinnedModel},
		{name: "preview alias", key: testKey, opts: []jev.Option{jev.WithModel("jev-preview")}, model: "jev-preview"},
		{name: "full model charset", key: testKey, opts: []jev.Option{jev.WithModel("Jev_1.13.0-rc2")}, model: "Jev_1.13.0-rc2"},
		{name: "longest model", key: testKey, opts: []jev.Option{jev.WithModel(strings.Repeat("m", 128))}, model: strings.Repeat("m", 128)},
		{name: "smallest timeout", key: testKey, opts: []jev.Option{jev.WithTimeout(time.Nanosecond)}, model: jev.DefaultModel},
		{name: "smallest max bytes", key: testKey, opts: []jev.Option{jev.WithMaxResponseBytes(1)}, model: jev.DefaultModel},
		{name: "largest max bytes", key: testKey, opts: []jev.Option{jev.WithMaxResponseBytes(maxResponseLimit)}, model: jev.DefaultModel},
		{name: "IPv6 loopback endpoint", key: testKey, opts: []jev.Option{jev.WithTestEndpoint("https://[::1]:8443")}, model: jev.DefaultModel},
		{name: "loopback endpoint with slash", key: testKey, opts: []jev.Option{jev.WithTestEndpoint("https://127.0.0.2:8443/")}, model: jev.DefaultModel},
		{name: "nil HTTP client", key: testKey, opts: []jev.Option{jev.WithHTTPClient(nil)}, model: jev.DefaultModel},
		{name: "nil options are skipped", key: testKey, opts: []jev.Option{nil, jev.WithModel(pinnedModel), nil}, model: pinnedModel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := jev.New(tt.key, tt.opts...)
			require.NoError(t, err)
			assert.Equal(t, tt.model, c.Model())
			assert.Equal(t, discern.Jev, c.Provider())
		})
	}
}

func TestNewLastOptionWins(t *testing.T) {
	s := testutil.NewServer(t, reply(http.StatusOK, ticketResponse))
	c, err := jev.New(testKey,
		jev.WithModel("jev 1"), jev.WithModel("jev-preview"), jev.WithModel(pinnedModel),
		jev.WithTimeout(0), jev.WithTimeout(10*time.Second),
		jev.WithMaxResponseBytes(0), jev.WithMaxResponseBytes(1), jev.WithMaxResponseBytes(4096),
		jev.WithTestEndpoint("https://example.com"), jev.WithTestEndpoint(s.URL()),
		jev.WithHTTPClient(&http.Client{Transport: testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errUnexpectedCall
		})}),
		jev.WithHTTPClient(s.Client()),
	)
	require.NoError(t, err)
	assert.Equal(t, pinnedModel, c.Model())

	res, err := testutil.Evaluate(t, c, ticketRequest())
	require.NoError(t, err)
	assert.Equal(t, discern.Complete, res.Status)
	assert.Equal(t, 1, s.Hits())
}

func TestPrepareRejectsInvalidRequest(t *testing.T) {
	c := inMemory(t, testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errUnexpectedCall }))
	unknownKind := ticketRequest()
	unknownKind.Questions[1].Kind = "rank"

	tests := []struct {
		name     string
		client   jev.Client
		req      *discern.Request
		sentinel error
		field    string
	}{
		{name: "nil request", client: *c, req: nil, sentinel: discern.ErrInvalidRequest, field: "request"},
		{name: "unencodable request", client: *c, req: &unknownKind, sentinel: discern.ErrInvalidRequest, field: "questions[1].kind"},
		{name: "zero client", client: jev.Client{}, req: new(ticketRequest()), sentinel: discern.ErrInvalidConfig, field: "client"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			call, err := tt.client.Prepare(tt.req)
			assert.Nil(t, call)
			require.ErrorIs(t, err, tt.sentinel)
			ve := testutil.ValidationError(t, err)
			assert.Equal(t, tt.field, ve.Field)
		})
	}
}

func TestPrepareMakesNoIO(t *testing.T) {
	transport := &testutil.CountingTransport{Next: respond(http.StatusOK, ticketResponse)}
	c := inMemory(t, transport)
	req := ticketRequest()

	call, err := c.Prepare(&req)
	require.NoError(t, err)
	require.NotNil(t, call)
	assert.Equal(t, 0, transport.Attempts())

	obs, err := call(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, transport.Attempts())
	assert.Equal(t, ticketObservation(testRequestID), obs)
}

func TestPreparedCallIsReusable(t *testing.T) {
	s := testutil.NewServer(t, reply(http.StatusOK, ticketResponse))
	c := serverClient(t, s)
	req := ticketRequest()
	call, err := c.Prepare(&req)
	require.NoError(t, err)

	for range 2 {
		_, err = call(t.Context())
		require.NoError(t, err)
	}
	reqs := s.Requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, reqs[0].Body, reqs[1].Body)
	assert.Equal(t, 2, s.Hits())
}

func TestClientPrintsOnlyTheModel(t *testing.T) {
	c := inMemory(t, respond(http.StatusOK, ticketResponse))
	want := `jev.Client{model: "jev-1.13.0"}`
	assert.Equal(t, want, c.String())
	assert.Equal(t, want, c.GoString())

	tests := []struct {
		name string
		got  string
	}{
		{name: "pointer %v", got: fmt.Sprintf("%v", c)},
		{name: "pointer %+v", got: fmt.Sprintf("%+v", c)},
		{name: "pointer %#v", got: fmt.Sprintf("%#v", c)},
		{name: "value %v", got: fmt.Sprintf("%v", *c)},
		{name: "value %+v", got: fmt.Sprintf("%+v", *c)},
		{name: "value %#v", got: fmt.Sprintf("%#v", *c)},
		{name: "Sprint", got: fmt.Sprint(c)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, want, tt.got)
		})
	}
}

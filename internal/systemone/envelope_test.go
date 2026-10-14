package systemone_test

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern/internal/systemone"
	"github.com/mrz1836/go-discern/internal/testutil"
)

const (
	clefResult      = `{"model":"clef","answers":{"urgent":{"type":"noul","noul":0.75}}}`
	successEnvelope = `{"success":true,"result":` + clefResult + `,"errors":[],"messages":[]}`
	failureEnvelope = `{"success":false,"result":null,"errors":[` +
		`{"code":7003,"message":"No route for the URI","documentation_url":"https://example.invalid/docs"},` +
		`{"code":10000,"message":"Authentication error"}],"messages":[{"code":1,"message":"ignored"}]}`
)

// exported copies the exported fields of env, for comparison with a literal.
func exported(env *systemone.Envelope) systemone.Envelope {
	return systemone.Envelope{
		Success: env.Success, Result: env.Result, ErrorCount: env.ErrorCount, Codes: env.Codes, Messages: env.Messages,
	}
}

func TestDecodeEnvelopeSuccess(t *testing.T) {
	env, err := systemone.DecodeEnvelope([]byte(successEnvelope))
	require.NoError(t, err)
	assert.True(t, env.Success)
	assertWire(t, clefResult, env.Result)
	assert.Zero(t, env.ErrorCount)
	assert.Nil(t, env.Codes)
	assert.Nil(t, env.Messages)
}

func TestDecodeEnvelopeFailure(t *testing.T) {
	env, err := systemone.DecodeEnvelope([]byte(failureEnvelope))
	require.NoError(t, err)
	assert.False(t, env.Success)
	assert.Nil(t, env.Result)
	assert.Equal(t, 2, env.ErrorCount)
	assert.Equal(t, []int{7003, 10000}, env.Codes)
	assert.Equal(t, []string{"No route for the URI", "Authentication error"}, env.Messages)
}

func TestDecodeEnvelopeOptionalFields(t *testing.T) {
	tests := []struct {
		name string
		body string
		want systemone.Envelope
	}{
		{name: "only success", body: `{"success":true}`, want: systemone.Envelope{Success: true}},
		{name: "null result", body: `{"success":true,"result":null}`, want: systemone.Envelope{Success: true}},
		{name: "null errors", body: `{"success":false,"errors":null}`, want: systemone.Envelope{}},
		{
			name: "errors without code or message are counted",
			body: `{"success":false,"errors":[{"detail":"x"},{"code":null,"message":null},{"code":-1}]}`,
			want: systemone.Envelope{ErrorCount: 3, Codes: []int{-1}},
		},
		{
			name: "error with message only",
			body: `{"success":false,"errors":[{"message":"Quota exceeded"}]}`,
			want: systemone.Envelope{ErrorCount: 1, Messages: []string{"Quota exceeded"}},
		},
		{
			name: "empty error object on success",
			body: `{"success":true,"errors":[{}]}`,
			want: systemone.Envelope{Success: true, ErrorCount: 1},
		},
		{
			name: "empty errors array",
			body: `{"success":true,"errors":[ ]}`,
			want: systemone.Envelope{Success: true},
		},
		{
			name: "escaped keys and messages",
			body: `{"succ\u0065ss":true,"errors":[{"c\u006fde":7003,"message":"Quota \"exceeded\""}]}`,
			want: systemone.Envelope{Success: true, ErrorCount: 1, Codes: []int{7003}, Messages: []string{`Quota "exceeded"`}},
		},
		{
			name: "non-object result kept raw",
			body: `{"success":true,"result":[1, 2]}`,
			want: systemone.Envelope{Success: true, Result: []byte(`[1, 2]`)},
		},
		{
			name: "unknown fields and huge numbers ignored",
			body: `{"result_info":{"count":1e400},"success":true,"messages":"anything"}`,
			want: systemone.Envelope{Success: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := systemone.DecodeEnvelope([]byte(tt.body))
			require.NoError(t, err)
			assert.Equal(t, tt.want, exported(env))
		})
	}
}

func TestDecodeEnvelopeRejectsMalformed(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing success", body: `{"result":` + clefResult + `}`},
		{name: "null success", body: `{"success":null}`},
		{name: "string success", body: `{"success":"true"}`},
		{name: "numeric success", body: `{"success":1}`},
		{name: "case-variant success", body: `{"Success":true}`},
		{name: "duplicate success", body: `{"success":true,"success":false}`},
		{name: "duplicate inside result", body: `{"success":true,"result":{"answers":{},"answers":{}}}`},
		{name: "duplicate inside errors", body: `{"success":false,"errors":[{"code":1,"code":2}]}`},
		{name: "trailing object", body: successEnvelope + ` {}`},
		{name: "trailing garbage", body: successEnvelope + `x`},
		{name: "top-level array", body: `[` + successEnvelope + `]`},
		{name: "top-level null", body: `null`},
		{name: "empty", body: ``},
		{name: "truncated", body: successEnvelope[:20]},
		{name: "invalid UTF-8", body: "{\"success\":false,\"errors\":[{\"message\":\"\xff\"}]}"},

		// With success:true, errors[] is read strictly.
		{name: "errors object", body: `{"success":true,"errors":{"code":1}}`},
		{name: "errors string", body: `{"success":true,"errors":"bad"}`},
		{name: "error item not an object", body: `{"success":true,"errors":[7003]}`},
		{name: "error item a string", body: `{"success":true,"errors":["No such model"]}`},
		{name: "error code string", body: `{"success":true,"errors":[{"code":"7003"}]}`},
		{name: "error code fractional", body: `{"success":true,"errors":[{"code":7003.5}]}`},
		{name: "error code overflow", body: `{"success":true,"errors":[{"code":99999999999999999999}]}`},
		{name: "error code exponent overflow", body: `{"success":true,"errors":[{"code":1e19}]}`},
		{name: "error message number", body: `{"success":true,"errors":[{"message":42}]}`},
		{name: "error message object", body: `{"success":true,"errors":[{"message":{"text":"x"}}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := systemone.DecodeEnvelope([]byte(tt.body))
			require.ErrorIs(t, err, systemone.ErrMalformed)
			assert.Nil(t, env)
		})
	}
}

func TestDecodeEnvelopeErrorsNeverQuoteResponseContent(t *testing.T) {
	bodies := []string{
		`{"success":"SECRET-SENTINEL"}`,
		`{"success":true,"errors":[{"code":"SECRET-SENTINEL"}]}`,
		`{"success":true,"errors":[{"message":["SECRET-SENTINEL"]}]}`,
		`{"success":true,"errors":["SECRET-SENTINEL"]}`,
		`{"SECRET-SENTINEL":1,"SECRET-SENTINEL":1,"success":true}`,
		`{"success":true}"SECRET-SENTINEL"`,
		`{"success":true,"result":SECRET-SENTINEL}`,
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			_, err := systemone.DecodeEnvelope([]byte(body))
			require.ErrorIs(t, err, systemone.ErrMalformed)
			assert.NotContains(t, err.Error(), secretSentinel)
		})
	}
}

func FuzzSystemOneEnvelope(f *testing.F) {
	f.Add([]byte(successEnvelope))
	f.Add([]byte(failureEnvelope))
	f.Add([]byte(`{"success":true}`))
	f.Add([]byte(`{"success":true,"success":true}`))
	f.Add([]byte(`{"success":false,"errors":[{"code":1e400}]}`))
	f.Add([]byte(successEnvelope + `x`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, data []byte) {
		require.NotPanics(t, func() {
			env, err := systemone.DecodeEnvelope(data)
			if err != nil {
				require.ErrorIs(t, err, systemone.ErrMalformed)
				require.Nil(t, env)
				return
			}
			require.NotNil(t, env)
			if env.Result != nil {
				require.NotEqual(t, "null", string(env.Result))
			}
		})
	})
}

func TestEnvelopeDecodeResult(t *testing.T) {
	req := ticketRequest()
	body := envelopeBody()
	env, err := systemone.DecodeEnvelope(body)
	require.NoError(t, err)

	t.Run("checked result", func(t *testing.T) {
		obs, err := env.DecodeResult(req)
		require.NoError(t, err)
		assert.Equal(t, validObservation(), obs)
	})
	t.Run("nil request", func(t *testing.T) {
		obs, err := env.DecodeResult(nil)
		require.ErrorIs(t, err, systemone.ErrMalformed)
		assert.Nil(t, obs)
	})
	t.Run("replaced result is checked in full", func(t *testing.T) {
		replaced := *env
		replaced.Result = []byte(`{"answers":{},"answers":{}}`)
		_, err := replaced.DecodeResult(req)
		require.ErrorIs(t, err, systemone.ErrMalformed)
		assert.Contains(t, err.Error(), "duplicate key")
	})
	t.Run("replaced valid result", func(t *testing.T) {
		replaced := *env
		replaced.Result = []byte(validBody)
		obs, err := replaced.DecodeResult(req)
		require.NoError(t, err)
		assert.Equal(t, validObservation(), obs)
	})
	t.Run("absent result", func(t *testing.T) {
		empty, err := systemone.DecodeEnvelope([]byte(`{"success":true}`))
		require.NoError(t, err)
		_, err = empty.DecodeResult(req)
		require.ErrorIs(t, err, systemone.ErrMalformed)
	})
}

func TestDecodeEnvelopeUnsuccessfulIsAlwaysARefusal(t *testing.T) {
	// success is read first; with success:false, errors[] entries of any shape
	// never make the envelope malformed.
	tests := []struct {
		name string
		body string
		want systemone.Envelope
	}{
		{name: "string entries", body: `{"success":false,"errors":["No such model","Try again"]}`, want: systemone.Envelope{ErrorCount: 2, Messages: []string{"No such model", "Try again"}}},
		{name: "string code", body: `{"success":false,"errors":[{"code":"5007","message":"No such model"}]}`, want: systemone.Envelope{ErrorCount: 1, Messages: []string{"No such model"}}},
		{name: "integral code spelled with a fraction", body: `{"success":false,"errors":[{"code":5007.0}]}`, want: systemone.Envelope{ErrorCount: 1, Codes: []int{5007}}},
		{name: "fractional code", body: `{"success":false,"errors":[{"code":5007.5,"message":"x"}]}`, want: systemone.Envelope{ErrorCount: 1, Messages: []string{"x"}}},
		{name: "overflowing code", body: `{"success":false,"errors":[{"code":1e400}]}`, want: systemone.Envelope{ErrorCount: 1}},
		{name: "message of another type", body: `{"success":false,"errors":[{"code":1,"message":["x"]}]}`, want: systemone.Envelope{ErrorCount: 1, Codes: []int{1}}},
		{name: "entries of other types", body: `{"success":false,"errors":[7003,null,true,[1]]}`, want: systemone.Envelope{ErrorCount: 4}},
		{name: "errors not an array", body: `{"success":false,"errors":{"code":1}}`, want: systemone.Envelope{}},
		{name: "escaped field names", body: testutil.Esc(`{"success":false,"errors":[{"c~u006fde":7,"m~u0065ssage":"x"}]}`), want: systemone.Envelope{ErrorCount: 1, Codes: []int{7}, Messages: []string{"x"}}},
		{name: "result kept", body: `{"success":false,"result":{"answers":{}},"errors":[]}`, want: systemone.Envelope{Result: []byte(`{"answers":{}}`)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := systemone.DecodeEnvelope([]byte(tt.body))
			require.NoError(t, err)
			assert.Equal(t, tt.want, exported(env))
		})
	}
}

func TestDecodeEnvelopeSuccessfulErrorsAreStrict(t *testing.T) {
	env, err := systemone.DecodeEnvelope([]byte(`{"success":true,"errors":[{"code":7e3,"message":"Quota exceeded"},{"code":null}]}`))
	require.NoError(t, err)
	assert.Equal(t, systemone.Envelope{Success: true, ErrorCount: 2, Codes: []int{7000}, Messages: []string{"Quota exceeded"}}, exported(env))

	_, err = systemone.DecodeEnvelope([]byte(`{"success":true,"errors":[{},{"code":"7003"}]}`))
	require.ErrorIs(t, err, systemone.ErrMalformed)
	assert.Equal(t, "systemone: malformed response: errors[1].code: must be an integer", err.Error())

	_, err = systemone.DecodeEnvelope([]byte(`{"success":true,"errors":[{},{},7]}`))
	require.ErrorIs(t, err, systemone.ErrMalformed)
	assert.Equal(t, "systemone: malformed response: errors[2]: must be an object", err.Error())

	_, err = systemone.DecodeEnvelope([]byte(`{"success":true,"errors":[{"message":1}]}`))
	require.ErrorIs(t, err, systemone.ErrMalformed)
	assert.Equal(t, "systemone: malformed response: errors[0].message: must be a string", err.Error())
}

// manyErrors returns n errors[] entries, the i-th with code i and message
// "m<i>", joined by commas.
func manyErrors(n int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf(`{"code":%d,"message":"m%d"}`, i, i)
	}
	return strings.Join(items, ",")
}

func TestEnvelopeErrorsAreRetainedWithinLimits(t *testing.T) {
	wantCodes := make([]int, 64)
	wantMessages := make([]string, 64)
	for i := range wantCodes {
		wantCodes[i], wantMessages[i] = i, fmt.Sprintf("m%d", i)
	}
	for _, success := range []string{"true", "false"} {
		t.Run("success "+success, func(t *testing.T) {
			env, err := systemone.DecodeEnvelope([]byte(`{"success":` + success + `,"errors":[` + manyErrors(100) + `]}`))
			require.NoError(t, err)
			assert.Equal(t, 100, env.ErrorCount, "every entry is counted")
			assert.Equal(t, wantCodes, env.Codes, "the first 64 codes are kept")
			assert.Equal(t, wantMessages, env.Messages, "the first 64 messages are kept")
		})
	}
	t.Run("refusal body", func(t *testing.T) {
		codes, messages := systemone.DecodeRefusal([]byte(`{"errors":[` + manyErrors(100) + `]}`))
		assert.Equal(t, wantCodes, codes)
		assert.Equal(t, wantMessages, messages)
	})
}

func TestEnvelopeMessagesAreCutAtARuneBoundary(t *testing.T) {
	// 1023 ASCII bytes and then a two-byte rune that would end at byte 1025.
	straddling := strings.Repeat("a", 1023) + "é" + strings.Repeat("b", 100)
	tests := []struct {
		name    string
		message string // the JSON string
		want    string
	}{
		{name: "short", message: `"Quota exceeded"`, want: "Quota exceeded"},
		{name: "exactly 1 KiB", message: `"` + strings.Repeat("a", 1024) + `"`, want: strings.Repeat("a", 1024)},
		{name: "longer", message: `"` + strings.Repeat("a", 5000) + `"`, want: strings.Repeat("a", 1024)},
		{name: "a rune across the limit", message: `"` + straddling + `"`, want: strings.Repeat("a", 1023)},
		{name: "escaped", message: `"` + strings.Repeat(testutil.Esc(`~n`), 2000) + `"`, want: strings.Repeat("\n", 1024)},
		{name: "a four-byte rune across the limit", message: `"` + strings.Repeat("a", 1022) + "😀" + `"`, want: strings.Repeat("a", 1022)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := systemone.DecodeEnvelope([]byte(`{"success":false,"errors":[{"message":` + tt.message + `}]}`))
			require.NoError(t, err)
			require.Len(t, env.Messages, 1)
			assert.Equal(t, tt.want, env.Messages[0])
			assert.True(t, utf8.ValidString(env.Messages[0]))
		})
	}
}

func TestDecodeRefusal(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		codes    []int
		messages []string
	}{
		{name: "envelope", body: failureEnvelope, codes: []int{7003, 10000}, messages: []string{"No route for the URI", "Authentication error"}},
		{name: "envelope without success", body: `{"errors":[{"code":5007,"message":"No such model"}]}`, codes: []int{5007}, messages: []string{"No such model"}},
		{name: "success not a boolean", body: `{"success":"no","errors":[{"code":5007}]}`, codes: []int{5007}},
		{name: "successful envelope with errors", body: `{"success":true,"errors":[{"code":3036}]}`, codes: []int{3036}},
		{name: "string entries and codes", body: `{"errors":["No such model",{"code":"5007"},{"code":5007.0}]}`, codes: []int{5007}, messages: []string{"No such model"}},
		{name: "escaped errors key", body: testutil.Esc(`{"err~u006frs":[{"code":1}]}`), codes: []int{1}},
		{name: "last repeated key wins", body: `{"errors":[{"code":1}],"errors":[{"code":2,"code":3}]}`, codes: []int{3}},
		{name: "empty errors", body: `{"errors":[]}`},
		{name: "null errors", body: `{"errors":null}`},
		{name: "errors not an array", body: `{"errors":{"code":1}}`},
		{name: "no errors", body: `{"result":null,"success":false}`},
		{name: "top-level array", body: `[{"errors":[{"code":1}]}]`},
		{name: "HTML page", body: `<html><body>Bad gateway</body></html>`},
		{name: "plain text", body: `upstream connect error`},
		{name: "empty", body: ``},
		{name: "truncated", body: `{"errors":[{"code":1}`},
		{name: "invalid UTF-8", body: "{\"errors\":[{\"code\":1,\"message\":\"\xff\"}]}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			codes, messages := systemone.DecodeRefusal([]byte(tt.body))
			assert.Equal(t, tt.codes, codes)
			assert.Equal(t, tt.messages, messages)
		})
	}
}

func FuzzDecodeRefusal(f *testing.F) {
	f.Add([]byte(failureEnvelope))
	f.Add([]byte(`{"errors":["No such model",{"code":5007.0,"message":"x"}]}`))
	f.Add([]byte(`{"errors":[{"code":1}],"errors":[{"code":2}]}`))
	f.Add([]byte(`<html>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		codes, messages := systemone.DecodeRefusal(data)
		require.LessOrEqual(t, len(codes), 64)
		require.LessOrEqual(t, len(messages), 64)
		for _, m := range messages {
			require.LessOrEqual(t, len(m), 1024)
			require.True(t, utf8.ValidString(m))
		}
	})
}

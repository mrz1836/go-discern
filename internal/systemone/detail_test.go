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

// Jev refusal bodies observed live.
const (
	jevUnknownModel = `{"detail":{"error_type":"api_usage_error","message":"Unknown model: jev"}}`
	jevOversized    = `{"detail":{"error_type":"max_tokens_exceeded"}}`
)

func TestDecodeDetailRefusal(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		messages []string
	}{
		{name: "observed unknown model", body: jevUnknownModel, messages: []string{"api_usage_error: Unknown model: jev"}},
		{name: "observed too many tokens", body: jevOversized, messages: []string{"max_tokens_exceeded"}},
		{name: "message only", body: `{"detail":{"message":"Bad request"}}`, messages: []string{"Bad request"}},
		{name: "string detail", body: `{"detail":"Not Found"}`, messages: []string{"Not Found"}},
		{
			name: "validation array", body: `{"detail":[` +
				`{"loc":["body","model"],"msg":"Field required","type":"missing"},` +
				`{"loc":["body","questions"],"msg":"Input should be a valid dictionary"},` +
				`{"type":"value_error"},` +
				`{"loc":["body"]},"plain",7,null]}`,
			messages: []string{"missing: Field required", "Input should be a valid dictionary", "value_error"},
		},
		{name: "escaped keys and values", body: testutil.Esc(`{"det~u0061il":{"error~u005ftype":"a~tb","message":"line~nbreak"}}`), messages: []string{"a\tb: line\nbreak"}},
		{name: "last repeated key wins", body: `{"detail":"first","detail":{"error_type":"x","error_type":"y"}}`, messages: []string{"y"}},
		{name: "other fields ignored", body: `{"error":"nope","detail":{"error_type":"e","code":429,"extra":{"message":"inner"}}}`, messages: []string{"e"}},
		{name: "wrong value types", body: `{"detail":{"error_type":7,"message":["x"]}}`},
		{name: "empty strings", body: `{"detail":{"error_type":"","message":""}}`},
		{name: "empty string detail", body: `{"detail":""}`},
		{name: "empty object detail", body: `{"detail":{}}`},
		{name: "empty array detail", body: `{"detail":[]}`},
		{name: "null detail", body: `{"detail":null}`},
		{name: "number detail", body: `{"detail":404}`},
		{name: "no detail", body: `{"message":"Unknown model"}`},
		{name: "Cloudflare envelope", body: `{"success":false,"errors":[{"code":1,"message":"m"}]}`},
		{name: "top-level array", body: `[{"detail":"x"}]`},
		{name: "top-level string", body: `"detail"`},
		{name: "HTML page", body: `<html><body>Bad gateway</body></html>`},
		{name: "empty", body: ``},
		{name: "truncated", body: `{"detail":{"error_type":"x"`},
		{name: "invalid UTF-8", body: "{\"detail\":\"\xff\"}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			codes, messages := systemone.DecodeDetailRefusal([]byte(tt.body))
			assert.Nil(t, codes)
			assert.Equal(t, tt.messages, messages)
		})
	}
}

func TestDecodeDetailRefusalBounds(t *testing.T) {
	t.Run("a long message is cut to 1 KiB at a rune boundary", func(t *testing.T) {
		body := `{"detail":{"error_type":"e","message":"` + strings.Repeat("a", 1019) + "😀" + strings.Repeat("b", 4000) + `"}}`
		_, messages := systemone.DecodeDetailRefusal([]byte(body))
		require.Len(t, messages, 1)
		assert.Equal(t, "e: "+strings.Repeat("a", 1019), messages[0])
		assert.True(t, utf8.ValidString(messages[0]))
	})
	t.Run("a long string detail is cut to 1 KiB", func(t *testing.T) {
		_, messages := systemone.DecodeDetailRefusal([]byte(`{"detail":"` + strings.Repeat("x", 5000) + `"}`))
		require.Len(t, messages, 1)
		assert.Equal(t, strings.Repeat("x", 1024), messages[0])
	})
	t.Run("only the first 64 entries are kept", func(t *testing.T) {
		items := make([]string, 100)
		for i := range items {
			items[i] = fmt.Sprintf(`{"msg":"m%d","type":"t"}`, i)
		}
		_, messages := systemone.DecodeDetailRefusal([]byte(`{"detail":[` + strings.Join(items, ",") + `]}`))
		require.Len(t, messages, 64)
		assert.Equal(t, "t: m0", messages[0])
		assert.Equal(t, "t: m63", messages[63])
	})
}

func FuzzDecodeDetailRefusal(f *testing.F) {
	f.Add([]byte(jevUnknownModel))
	f.Add([]byte(jevOversized))
	f.Add([]byte(`{"detail":[{"loc":["body"],"msg":"Field required","type":"missing"},"x"]}`))
	f.Add([]byte(`{"detail":"Not Found","detail":{"message":"m"}}`))
	f.Add([]byte(`<html>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		codes, messages := systemone.DecodeDetailRefusal(data)
		require.Nil(t, codes)
		require.LessOrEqual(t, len(messages), 64)
		for _, m := range messages {
			require.NotEmpty(t, m)
			require.LessOrEqual(t, len(m), 1024)
			require.True(t, utf8.ValidString(m))
		}
	})
}

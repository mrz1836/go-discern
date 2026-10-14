package httpx_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern/internal/httpx"
	"github.com/mrz1836/go-discern/internal/ident"
)

func TestValidID(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want bool
	}{
		{name: "request ID", s: "req-01HX7Q2K9M", want: true},
		{name: "ray ID", s: "8c1f2a3b4d5e6f70-SJC", want: true},
		{name: "visible punctuation", s: "!~a/b=c+d", want: true},
		{name: "single byte", s: "r", want: true},
		{name: "longest", s: strings.Repeat("r", ident.MaxProviderID), want: true},
		{name: "empty", s: ""},
		{name: "too long", s: strings.Repeat("r", ident.MaxProviderID+1)},
		{name: "space", s: "req 1"},
		{name: "tab", s: "req\t1"},
		{name: "line feed", s: "req-1\n"},
		{name: "nul", s: "req\x001"},
		{name: "delete", s: "req\x7f"},
		{name: "non-ASCII", s: "réq-1"},
		{name: "invalid UTF-8", s: "req\xff"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, httpx.ValidID(tt.s))
		})
	}
}

func TestEndpointValidation(t *testing.T) {
	accepted := []struct {
		raw  string
		want string
	}{
		{raw: "https://127.0.0.1:8443", want: "https://127.0.0.1:8443"},
		{raw: "https://127.5.6.7", want: "https://127.5.6.7"},
		{raw: "https://[::1]:9000", want: "https://[::1]:9000"},
		{raw: "https://127.0.0.1/", want: "https://127.0.0.1"},
		{raw: "https://[::1]", want: "https://[::1]"},
		{raw: "https://127.255.255.254:65535/", want: "https://127.255.255.254:65535"},
		{raw: "HTTPS://127.0.0.1:1", want: "https://127.0.0.1:1"},
	}
	for _, tt := range accepted {
		t.Run("accepts "+tt.raw, func(t *testing.T) {
			got, err := httpx.TestEndpoint(tt.raw)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	rejected := []string{
		"",
		"garbage",
		"://127.0.0.1",
		"http://127.0.0.1:8443",
		"ftp://127.0.0.1",
		"https://localhost",
		"https://localhost:8443",
		"https://10.0.0.1",
		"https://128.0.0.1",
		"https://126.255.255.255",
		"https://0.0.0.0",
		"https://[::2]",
		"https://[::]:9000",
		"https://[::ffff:127.0.0.1]",
		"https://[::1%25lo0]",
		"https://[0:0:0:0:0:0:0:1]",
		"https://[127.0.0.1]",
		"https://::1",
		"https://127.1",
		"https://127.000.000.001",
		"https://user@127.0.0.1",
		"https://user:pass@127.0.0.1",
		"https://@127.0.0.1",
		"https://127.0.0.1?x=1",
		"https://127.0.0.1/?",
		"https://127.0.0.1#frag",
		"https://127.0.0.1/#",
		"https://127.0.0.1/v1",
		"https://127.0.0.1//",
		"https://127.0.0.1/%2F",
		"https://127.0.0.1:",
		"https://127.0.0.1:0",
		"https://127.0.0.1:65536",
		"https://127.0.0.1:08443",
		"https://127.0.0.1:port",
		"https://127.0.0.1\\@evil",
		"https:127.0.0.1",
		"https:///v1",
		"https://example.com",
		" https://127.0.0.1",
	}
	for _, raw := range rejected {
		t.Run("rejects "+raw, func(t *testing.T) {
			got, err := httpx.TestEndpoint(raw)
			require.ErrorIs(t, err, httpx.ErrEndpoint)
			assert.Empty(t, got)
			if raw != "" {
				assert.NotContains(t, err.Error(), raw)
			}
		})
	}
}

func TestValidCredential(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want bool
	}{
		{name: "simple", s: "test-key", want: true},
		{name: "visible punctuation", s: "abc!~_.-+/=", want: true},
		{name: "single byte", s: "k", want: true},
		{name: "maximum length", s: strings.Repeat("k", 4096), want: true},
		{name: "empty", s: ""},
		{name: "too long", s: strings.Repeat("k", 4097)},
		{name: "space", s: "test key"},
		{name: "leading space", s: " test-key"},
		{name: "tab", s: "test\tkey"},
		{name: "carriage return", s: "test-key\r"},
		{name: "line feed", s: "test-key\n"},
		{name: "header injection", s: "test-key\r\nX-Evil: 1"},
		{name: "nul", s: "test\x00key"},
		{name: "delete", s: "test\x7fkey"},
		{name: "non-ASCII", s: "tëst-key"},
		{name: "high byte", s: "test\xffkey"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, httpx.ValidCredential(tt.s))
		})
	}
}

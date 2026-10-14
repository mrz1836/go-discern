package httpx_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/mrz1836/go-discern/internal/httpx"
)

func TestRetryAfterHints(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	date := func(d time.Duration) string { return now.Add(d).Format(http.TimeFormat) }
	tests := []struct {
		name   string
		ms     string
		header string
		want   time.Duration
		wantOK bool
	}{
		{name: "absent"},
		{name: "empty both", ms: " ", header: ""},
		{name: "milliseconds", ms: "1500", want: 1500 * time.Millisecond, wantOK: true},
		{name: "milliseconds take precedence", ms: "250", header: "10", want: 250 * time.Millisecond, wantOK: true},
		{name: "milliseconds zero", ms: "0", header: "10", want: 0, wantOK: true},
		{name: "milliseconds negative clamps to zero", ms: "-5", header: "10", want: 0, wantOK: true},
		{name: "milliseconds garbage falls through", ms: "soon", header: "10", want: 10 * time.Second, wantOK: true},
		{name: "milliseconds fraction rounds up", ms: "1500.5", want: 1501 * time.Millisecond, wantOK: true},
		{name: "milliseconds small fraction rounds up", ms: "0.001", want: time.Millisecond, wantOK: true},
		{name: "milliseconds zero fraction", ms: "1500.000", want: 1500 * time.Millisecond, wantOK: true},
		{name: "milliseconds fractional zero", ms: "0.0", header: "10", want: 0, wantOK: true},
		{name: "milliseconds fraction takes precedence", ms: "1.5", header: "3", want: 2 * time.Millisecond, wantOK: true},
		{name: "milliseconds negative fraction clamps to zero", ms: "-1.5", header: "3", want: 0, wantOK: true},
		{name: "milliseconds fraction at the maximum clamps", ms: "86400000.5", want: httpx.MaxRetryAfter, wantOK: true},
		{name: "milliseconds overflow with a fraction clamps", ms: "99999999999999999999.5", want: httpx.MaxRetryAfter, wantOK: true},
		{name: "milliseconds trailing point falls through", ms: "15.", header: "3", want: 3 * time.Second, wantOK: true},
		{name: "milliseconds leading point falls through", ms: ".5", header: "3", want: 3 * time.Second, wantOK: true},
		{name: "milliseconds two points fall through", ms: "1.2.3", header: "3", want: 3 * time.Second, wantOK: true},
		{name: "milliseconds exponent falls through", ms: "1e3", header: "3", want: 3 * time.Second, wantOK: true},
		{name: "milliseconds fraction with garbage alone", ms: "1.5s"},
		{name: "milliseconds clamp", ms: "86400001", want: httpx.MaxRetryAfter, wantOK: true},
		{name: "milliseconds overflow clamps", ms: "99999999999999999999", want: httpx.MaxRetryAfter, wantOK: true},
		{name: "seconds", header: "120", want: 2 * time.Minute, wantOK: true},
		{name: "seconds with spaces", header: " 7 ", want: 7 * time.Second, wantOK: true},
		{name: "explicit zero", header: "0", want: 0, wantOK: true},
		{name: "negative clamps to zero", header: "-5", want: 0, wantOK: true},
		{name: "fractional dropped", header: "1.5"},
		{name: "fractional zero dropped", header: "0.0"},
		{name: "plus sign dropped", header: "+5"},
		{name: "lone minus dropped", header: "-"},
		{name: "garbage dropped", header: "later"},
		{name: "hex dropped", header: "0x10"},
		{name: "exactly the maximum", header: "86400", want: httpx.MaxRetryAfter, wantOK: true},
		{name: "above the maximum clamps", header: "86401", want: httpx.MaxRetryAfter, wantOK: true},
		{name: "overflow clamps", header: "99999999999999999999", want: httpx.MaxRetryAfter, wantOK: true},
		{name: "very long digits clamp", header: strings.Repeat("9", 400), want: httpx.MaxRetryAfter, wantOK: true},
		{name: "future date", header: date(90 * time.Second), want: 90 * time.Second, wantOK: true},
		{name: "past date clamps to zero", header: date(-time.Hour), want: 0, wantOK: true},
		{name: "date now", header: date(0), want: 0, wantOK: true},
		{name: "far future date clamps", header: date(48 * time.Hour), want: httpx.MaxRetryAfter, wantOK: true},
		{name: "rfc850 date", header: now.Add(time.Minute).Format(time.RFC850), want: time.Minute, wantOK: true},
		{name: "ansi c date", header: now.Add(2 * time.Minute).Format(time.ANSIC), want: 2 * time.Minute, wantOK: true},
		{name: "date-shaped garbage dropped", header: "Wed, 99 Oct 2026 12:00:00 GMT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			if tt.ms != "" {
				h.Set("retry-after-ms", tt.ms)
			}
			if tt.header != "" {
				h.Set("Retry-After", tt.header)
			}
			got, ok := httpx.RetryAfter(h, now)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRetryAfterNilHeader(t *testing.T) {
	got, ok := httpx.RetryAfter(nil, time.Now())
	assert.False(t, ok)
	assert.Zero(t, got)
}

package httpx

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryAfter parses the provider's retry hint. "retry-after-ms" takes precedence
// as a decimal count of milliseconds, such as "1500" or "1500.5", rounded up to a
// whole millisecond; then "Retry-After" as integer delta-seconds or an HTTP-date
// relative to now.
//
// An absent, empty or unparseable value is dropped and the next header is tried;
// false means no usable hint. Retry-After takes whole seconds only, so a
// fractional value there is dropped. Negative values and past dates clamp to 0,
// and hints above MaxRetryAfter, including counts too large to represent, clamp
// to MaxRetryAfter. An explicit zero, such as "0" or "0.0", returns (0, true):
// zero is distinct from absent.
func RetryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	if d, ok := parseDelta(h.Get("Retry-After-Ms"), time.Millisecond, true); ok {
		return d, true
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if d, ok := parseDelta(v, time.Second, false); ok {
		return d, true
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	return clampHint(t.Sub(now)), true
}

// parseDelta parses an optionally negative base-10 count of units. With
// fraction set it also accepts a decimal fraction, digits on both sides of the
// point, and rounds a non-zero fraction up to the next whole unit.
func parseDelta(s string, unit time.Duration, fraction bool) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	number, negative := strings.CutPrefix(s, "-")
	whole, frac, hasPoint := strings.Cut(number, ".")
	if !allDigits(whole) || (hasPoint && (!fraction || !allDigits(frac))) {
		return 0, false
	}
	if negative {
		return 0, true
	}
	n, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || n > int64(MaxRetryAfter/unit) {
		// Only overflow can fail here: whole is all digits.
		return MaxRetryAfter, true
	}
	if strings.Trim(frac, "0") != "" {
		n++ // round a partial unit up, so the hint is never shortened
	}
	return clampHint(time.Duration(n) * unit), true
}

// allDigits reports whether s is a non-empty run of ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// clampHint limits a hint to [0, MaxRetryAfter].
func clampHint(d time.Duration) time.Duration {
	return min(max(d, 0), MaxRetryAfter)
}

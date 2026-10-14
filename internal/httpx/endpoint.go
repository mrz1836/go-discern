package httpx

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/mrz1836/go-discern/internal/ident"
)

// ErrEndpoint is the sentinel every TestEndpoint error wraps.
var ErrEndpoint = errors.New("httpx: invalid test endpoint")

// maxCredentialLength bounds a credential, in bytes.
const maxCredentialLength = 4096

// TestEndpoint validates a loopback base URL for tests and returns it as
// "https://host[:port]" without a trailing slash.
//
// Only https is accepted, with a host that is an IPv4 literal in 127.0.0.0/8 or
// exactly ::1 (bracketed), an optional numeric port, and no userinfo, query,
// fragment or path other than "/". Names such as "localhost" are rejected: they
// resolve through the system resolver and could leave the machine. Errors wrap
// ErrEndpoint and never quote the input.
func TestEndpoint(raw string) (string, error) {
	if raw == "" {
		return "", endpointErr("is empty")
	}
	if strings.ContainsAny(raw, "?#@\\") {
		return "", endpointErr("must not contain userinfo, a query or a fragment")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", endpointErr("is not a valid URL")
	}
	switch {
	case u.Scheme != "https":
		return "", endpointErr("must use https")
	case u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return "", endpointErr("must not contain userinfo, a query or a fragment")
	case u.Path != "" && u.Path != "/", u.RawPath != "":
		return "", endpointErr("must not have a path")
	}
	if err = checkLoopbackHost(u.Host); err != nil {
		return "", err
	}
	return "https://" + u.Host, nil
}

// checkLoopbackHost accepts "ip[:port]" for an IPv4 loopback address in canonical
// dotted form, or "[::1][:port]".
func checkLoopbackHost(hostport string) error {
	host, port := splitHostPort(hostport)
	if !loopbackHost(host) {
		return endpointErr("host must be 127.0.0.0/8 or ::1")
	}
	if port == "" {
		return nil
	}
	digits, ok := strings.CutPrefix(port, ":")
	if !ok {
		return endpointErr("has an invalid host")
	}
	n, err := strconv.ParseUint(digits, 10, 16)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != digits {
		return endpointErr("port must be a number from 1 to 65535")
	}
	return nil
}

// splitHostPort splits "host[:port]" or "[host][:port]". The host keeps its
// brackets and the port keeps its colon, so neither part is silently dropped.
func splitHostPort(hostport string) (host, port string) {
	if end := strings.LastIndexByte(hostport, ']'); end >= 0 {
		return hostport[:end+1], hostport[end+1:]
	}
	if i := strings.LastIndexByte(hostport, ':'); i >= 0 {
		return hostport[:i], hostport[i:]
	}
	return hostport, ""
}

// loopbackHost reports whether host is exactly "[::1]" or a canonical IPv4
// address in 127.0.0.0/8.
func loopbackHost(host string) bool {
	if host == "[::1]" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Is4() && ip.IsLoopback() && ip.String() == host
}

// ValidCredential reports whether s is safe to place in a header: 1 to 4096 bytes
// of visible ASCII (0x21 to 0x7E), so no spaces, CR, LF or other controls.
func ValidCredential(s string) bool { return s != "" && ident.Visible(s, maxCredentialLength) }

// ValidID reports whether s can be kept as a provider-reported request or ray
// ID: 1 to ident.MaxProviderID bytes of visible ASCII (0x21 to 0x7E). Do drops
// any other value, so an ID that reaches an Observation or a stored record is
// always short, printable and free of spaces and controls.
func ValidID(s string) bool { return s != "" && ident.Visible(s, ident.MaxProviderID) }

func endpointErr(reason string) error {
	return fmt.Errorf("%w: %s", ErrEndpoint, reason)
}

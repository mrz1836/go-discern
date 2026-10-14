// Package ident holds the byte-level identifier rules that discern and its
// adapters share, so that each rule is written once: visible ASCII for what a
// provider reports or a header carries, and a short name alphabet for question
// IDs and model names.
//
// It imports only the standard library, so every package in this module, the
// root package included, can use it.
package ident

// MaxProviderID bounds an identifier a provider reports, such as a model name,
// request ID or ray ID, in bytes.
const MaxProviderID = 256

// Visible reports whether s is at most maxLen bytes, each visible ASCII (0x21
// to 0x7E): no spaces, control characters or non-ASCII bytes. The empty string
// is visible; callers that require a value check for it themselves.
func Visible(s string, maxLen int) bool {
	if len(s) > maxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

// Name reports whether s is 1 to maxLen bytes of [A-Za-z0-9_.-].
func Name(s string, maxLen int) bool {
	if s == "" || len(s) > maxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == '-':
		default:
			return false
		}
	}
	return true
}

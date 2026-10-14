package systemone

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mrz1836/go-discern/internal/jsonx"
)

// ErrMalformed is the sentinel every decode error wraps. Decode errors name the
// offending field and, where useful, the caller's question ID; they never quote
// response content.
var ErrMalformed = errors.New("systemone: malformed response")

// linearLookups is the largest object searched by scanning. Larger ones are
// sorted once, so that each lookup stays O(log n).
const linearLookups = 16

// field names the response member an error is about. It is rendered only when
// decoding fails, so a successful decode builds no strings for it.
type field struct {
	answer string // question ID: the path starts at answers[<answer>]
	parent string // an intermediate member, such as "usage"
	key    string // the member itself, and the key it is looked up by
}

// String renders the path, such as `answers["queue"].probabilities` or
// "usage.input_tokens".
func (f field) String() string {
	var b strings.Builder
	if f.answer != "" {
		b.WriteString("answers[")
		b.WriteString(strconv.Quote(f.answer))
		b.WriteByte(']')
	}
	for _, part := range [...]string{f.parent, f.key} {
		if part == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(part)
	}
	return b.String()
}

// member returns the field of the member key inside f.
func (f field) member(key string) field {
	if f.key == "" {
		return field{answer: f.answer, key: key}
	}
	return field{answer: f.answer, parent: f.key, key: key}
}

// malformed returns an error wrapping ErrMalformed for the field.
func malformed(f field, reason string) error {
	return fmt.Errorf("%w: %s: %s", ErrMalformed, f, reason)
}

// responseKey names the document as a whole in error messages.
const responseKey = "response"

// responseError returns an error about the document as a whole.
func responseError(reason string) error { return malformed(field{key: responseKey}, reason) }

// checkStrict rejects invalid UTF-8, invalid JSON, duplicate object keys at any
// depth (keys are compared once unescaped, so "a" and an escaped spelling of it
// collide), and any data after the top-level value. Numbers are not parsed
// here, so huge literals in fields the decoder ignores do not fail the check.
func checkStrict(data []byte) error {
	if !utf8.Valid(data) {
		return responseError("is not valid UTF-8")
	}
	if !json.Valid(data) {
		return invalidJSON(data)
	}
	if end := jsonx.DuplicateKeyEnd(data); end >= 0 {
		return responseError(fmt.Sprintf("has a duplicate key in the object ending at offset %d", end))
	}
	return nil
}

// invalidJSON explains why json.Valid rejected data: a syntax error, reported by
// offset only because decoder messages can quote response bytes, or data after a
// complete top-level value.
func invalidJSON(data []byte) error {
	var v json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&v); err != nil {
		if se, ok := errors.AsType[*json.SyntaxError](err); ok {
			return responseError(fmt.Sprintf("is not valid JSON (offset %d)", se.Offset))
		}
		return responseError("is not valid JSON")
	}
	return responseError("has data after the top-level value")
}

// unquoteKey returns the content of the JSON string s, quotes included in s. It
// aliases s, capped so that an append cannot overwrite what follows, unless s
// has escapes, which are decoded exactly as encoding/json decodes them into one
// new buffer.
func unquoteKey(s []byte) ([]byte, bool) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return nil, false
	}
	content := s[1 : len(s)-1 : len(s)-1]
	if bytes.IndexByte(content, '\\') < 0 {
		return content, true
	}
	// An escape never decodes to more bytes than it spells, so for valid UTF-8 the
	// content's length is enough.
	decoded, ok := jsonx.AppendUnquoted(make([]byte, 0, len(content)), s)
	if !ok {
		return nil, false
	}
	return decoded, true
}

// maxKnownKey bounds the escaped spelling of a field name the decoder looks
// for: every byte of an ASCII name takes at most six bytes to escape, and the
// longest name, "probabilities" or "output_tokens", has 13.
const maxKnownKey = 6 * 13

// keyIn reports whether the JSON string literal lit, a key, decodes to one of
// names, which are ASCII. A key with escapes is decoded into a buffer on the
// stack, and only when it is short enough to spell a name, so unknown keys cost
// no allocation however they are spelled.
func keyIn(lit []byte, names ...string) bool {
	if len(lit) < 2 {
		return false
	}
	content := lit[1 : len(lit)-1]
	if bytes.IndexByte(content, '\\') >= 0 {
		if len(content) > maxKnownKey {
			return false
		}
		var buf [maxKnownKey]byte
		decoded, ok := jsonx.AppendUnquoted(buf[:0], lit)
		if !ok {
			return false
		}
		content = decoded
	}
	for _, name := range names {
		if string(content) == name {
			return true
		}
	}
	return false
}

// valueEnd returns the index just past the JSON value that starts at data[i],
// or -1. data must be valid JSON from that point on.
func valueEnd(data []byte, i int) int {
	if i >= len(data) {
		return -1
	}
	switch data[i] {
	case '"':
		return jsonx.StringEnd(data, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(data); j++ {
			switch data[j] {
			case '"':
				end := jsonx.StringEnd(data, j)
				if end < 0 {
					return -1
				}
				j = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return j + 1
				}
			}
		}
		return -1
	}
	j := i
	for j < len(data) && !isDelimiter(data[j]) {
		j++
	}
	if j == i {
		return -1
	}
	return j
}

// isDelimiter reports whether c ends a number or a literal.
func isDelimiter(c byte) bool {
	switch c {
	case ',', '}', ']', ':', ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// skipSpace returns the index of the first non-whitespace byte at or after i.
func skipSpace(data []byte, i int) int {
	for i < len(data) && isSpace(data[i]) {
		i++
	}
	return i
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

package discern

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mrz1836/go-discern/internal/jsonx"
)

// invalidJSONReason is the validation reason for a Value that is not valid JSON,
// not valid UTF-8, or has an object with duplicate keys.
const invalidJSONReason = "must be valid JSON and valid UTF-8, without duplicate object keys"

type valueKind uint8

const (
	valueNull valueKind = iota
	valueText
	valueJSON
)

// jsonType is the top-level JSON type of a value. The zero value is jsonNull, so
// the zero Value is null.
type jsonType uint8

const (
	jsonNull jsonType = iota
	jsonInvalid
	jsonString
	jsonObject
	jsonArray
	jsonScalar // number or boolean
)

// Value is an immutable JSON value used for state, instructions and descriptions.
// Build one with Text, JSON or Marshal. The zero Value encodes as null, which is
// accepted only as a Choice option description.
//
// A Value is checked once, when it is built: its validity, top-level type and
// whether it is blank are cached, so validating and encoding a large state never
// rescans or copies it more than needed.
type Value struct {
	kind  valueKind
	typ   jsonType
	blank bool // text with no non-space characters, or an object or array without members
	text  string
	raw   []byte
}

// Text returns a Value holding a JSON string. The string must be valid UTF-8; that is
// checked when the request is validated, before any call.
func Text(s string) Value {
	v := Value{kind: valueText, text: s, typ: jsonString, blank: strings.TrimSpace(s) == ""}
	if !utf8.ValidString(s) {
		v.typ = jsonInvalid
	}
	return v
}

// JSON returns a Value holding raw JSON bytes, which are copied and later sent
// verbatim: they are never re-encoded, so large integers and key order survive.
// The bytes must be valid JSON and valid UTF-8, and no object may repeat a key;
// that is checked when the request is validated, before any call.
func JSON(b []byte) Value {
	raw := bytes.Clone(b)
	typ, blank := classifyRaw(raw)
	return Value{kind: valueJSON, raw: raw, typ: typ, blank: blank}
}

// Marshal encodes v once with encoding/json, without HTML escaping, and returns the
// result as a Value. The result follows the same rules as JSON; for example, a
// json.RawMessage field with duplicate keys makes the Value invalid.
func Marshal(v any) (Value, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return Value{}, invalid(ErrInvalidRequest, "value", "cannot be encoded as JSON")
	}
	raw := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	typ, blank := classifyRaw(raw)
	return Value{kind: valueJSON, raw: raw, typ: typ, blank: blank}, nil
}

// IsZero reports whether v is the zero Value.
func (v Value) IsZero() bool { return v.kind == valueNull }

// MarshalJSON returns the encoded value: a JSON string for Text, the raw bytes for
// JSON and Marshal, and null for the zero Value. HTML characters are not escaped.
// It returns an error matching ErrInvalidRequest for invalid JSON or UTF-8, or JSON
// with duplicate object keys.
func (v Value) MarshalJSON() ([]byte, error) { return v.AppendJSON(nil) }

// AppendJSON appends the encoded value to dst, like MarshalJSON, without an
// intermediate copy. It returns an error matching ErrInvalidRequest, and dst
// unchanged, for invalid JSON or UTF-8, or JSON with duplicate object keys.
func (v Value) AppendJSON(dst []byte) ([]byte, error) {
	switch {
	case v.typ == jsonInvalid:
		return dst, invalid(ErrInvalidRequest, "value", invalidJSONReason)
	case v.kind == valueText:
		return appendString(dst, v.text), nil
	case v.kind == valueJSON:
		return append(dst, v.raw...), nil
	}
	return append(dst, "null"...), nil
}

// check reports the cached top-level type and blankness.
func (v Value) check() (t jsonType, blank bool) { return v.typ, v.blank }

// classifyRaw validates raw JSON and reports its top-level type. For a string,
// blank reports whether it has no non-space characters; for an object or array,
// whether it has no members.
func classifyRaw(raw []byte) (jsonType, bool) {
	if !utf8.Valid(raw) || !json.Valid(raw) || jsonx.HasDuplicateKey(raw) {
		// Duplicate keys are ambiguous: decoders disagree on which value wins, and
		// a provider that echoes the value back would have its whole (billed)
		// response rejected by the strict response decoder.
		return jsonInvalid, false
	}
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	switch trimmed[0] {
	case '"':
		blank, ok := blankString(trimmed)
		if !ok {
			return jsonInvalid, false
		}
		return jsonString, blank
	case '{':
		return jsonObject, emptyComposite(trimmed)
	case '[':
		return jsonArray, emptyComposite(trimmed)
	case 'n':
		return jsonNull, true
	}
	return jsonScalar, false
}

// blankString reports whether a valid JSON string literal, which may be followed
// by white space, holds only white space, as strings.TrimSpace defines it. It
// reads the raw bytes, so a large string is not decoded into a copy, except when
// an escape sequence comes before the first other character; then the literal
// is decoded. ok is false only if decoding fails.
func blankString(lit []byte) (blank, ok bool) {
	for i := 1; i < len(lit); {
		switch b := lit[i]; {
		case b == '"':
			return true, true
		case b == '\\':
			// Decoding never makes valid UTF-8 longer, so the literal's length is
			// enough room.
			lit = bytes.TrimRight(lit, " \t\r\n")
			decoded, valid := jsonx.AppendUnquoted(make([]byte, 0, len(lit)), lit)
			return valid && len(bytes.TrimSpace(decoded)) == 0, valid
		case b == ' ':
			// The only white space a JSON string holds unescaped below U+0080.
			i++
		case b < utf8.RuneSelf:
			return false, true
		default:
			r, size := utf8.DecodeRune(lit[i:])
			if !unicode.IsSpace(r) {
				return false, true
			}
			i += size
		}
	}
	return false, false
}

// emptyComposite reports whether a valid JSON object or array has no members:
// after the opening bracket, the next non-space byte closes it.
func emptyComposite(raw []byte) bool {
	rest := bytes.TrimLeft(raw[1:], " \t\r\n")
	return len(rest) > 0 && (rest[0] == '}' || rest[0] == ']')
}

const hexDigits = "0123456789abcdef"

// appendString appends s as a JSON string, escaping exactly like encoding/json
// with HTML escaping disabled. s must be valid UTF-8.
func appendString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if b >= 0x20 && b != '"' && b != '\\' {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch b {
			case '\\', '"':
				dst = append(dst, '\\', b)
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[b>>4], hexDigits[b&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == '\u2028' || r == '\u2029' {
			// encoding/json escapes the JavaScript line terminators.
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[r&0xF])
			start = i + size
		}
		i += size
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

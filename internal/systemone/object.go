package systemone

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"
	"strconv"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/jsonx"
)

const (
	// maxMemberHint caps the member capacity a decode reserves up front; it
	// grows from there when a response needs more.
	maxMemberHint = 1024

	// unknownMargin is how many members beyond the expected keys an object
	// whose keys are a domain, such as the answers keyed by question ID, may
	// have before it is rejected unread. Any extra key already fails the
	// decode; the margin only bounds the memory spent before it does.
	unknownMargin = 256
)

// member is one member of a decoded object: its unescaped key and its raw value,
// which aliases the decoded document.
type member struct {
	key   []byte
	value json.RawMessage
}

// object is a JSON object's members. Lookups are exact and case-sensitive,
// unlike encoding/json struct decoding, which matches keys case-insensitively.
// An object of more than linearLookups members is sorted by key, so each lookup
// is a binary search. Keys are unique: the document passed checkStrict.
type object []member

// decoder parses the objects of one document that passed checkStrict. Every
// object's members are appended to one shared slice and the object keeps a
// capped window onto it, so a decode reserves member storage once rather than
// once per object. Windows stay valid if the slice grows, because growing
// copies into a new array and leaves the old one untouched; the slice is cut
// back only past windows that are no longer used.
type decoder struct {
	members []member
}

// newDecoder reserves member capacity for decoding data for req: no more than
// the members a well-formed answer to req keeps at once, and never more than
// data has colons, as every member is followed by one.
func newDecoder(data []byte, req *discern.Request) decoder {
	widest := 0
	for i := range req.Questions {
		widest = max(widest, len(req.Questions[i].Options), len(req.Questions[i].Levels))
	}
	// The response and usage fields, the answers, and the fields, probabilities
	// and legend of the one answer being decoded.
	hint := resultFields + usageFields + len(req.Questions) + answerFields + 2*widest
	return decoder{members: make([]member, 0, min(bytes.Count(data, []byte{':'}), hint, maxMemberHint))}
}

// fields parses raw as an object and keeps only the members named in names, so
// unknown members cost no storage however many there are. null and other types
// are rejected.
func (d *decoder) fields(raw json.RawMessage, f field, names ...string) (object, error) {
	o, reason := d.parse(raw, math.MaxInt, names)
	if reason != "" {
		return nil, malformed(f, reason)
	}
	return o, nil
}

// keyed parses raw as an object whose keys should be n names, such as question
// IDs, keeping every member. An object of more than n plus unknownMargin
// members is rejected without reading the rest.
func (d *decoder) keyed(raw json.RawMessage, f field, n int) (object, error) {
	o, reason := d.parse(raw, n+unknownMargin, nil)
	if reason != "" {
		return nil, malformed(f, reason)
	}
	return o, nil
}

// parse appends raw's members to d.members, all of them when names is empty and
// otherwise only the named ones, and returns them as an object. It returns why
// raw is rejected instead: it is not an object, or it has more than limit kept
// members.
func (d *decoder) parse(raw []byte, limit int, names []string) (object, string) {
	start := len(d.members)
	r := readObject(raw)
	for lit, value, more := r.next(); more; lit, value, more = r.next() {
		if len(names) > 0 && !keyIn(lit, names...) {
			continue
		}
		if len(d.members)-start == limit {
			d.members = d.members[:start]
			return nil, "has too many members"
		}
		key, ok := unquoteKey(lit)
		if !ok {
			r.ok = false
			break
		}
		d.members = append(d.members, member{key: key, value: value})
	}
	if !r.ok {
		d.members = d.members[:start]
		return nil, "must be an object"
	}
	end := len(d.members)
	o := object(d.members[start:end:end])
	if len(o) > linearLookups {
		slices.SortFunc(o, func(a, b member) int { return bytes.Compare(a.key, b.key) })
	}
	return o, ""
}

// requiredKeyed returns a member that must be an object keyed by n names.
func (d *decoder) requiredKeyed(o object, f field, n int) (object, error) {
	raw, err := o.required(f)
	if err != nil {
		return nil, err
	}
	return d.keyed(raw, f, n)
}

// objectReader reads the members of a JSON object in order, without storing
// them. raw must be valid JSON; on anything else it stops and clears ok.
type objectReader struct {
	raw  []byte
	i    int  // the next member's key
	ok   bool // raw has been a well-formed object so far
	done bool // the closing brace has been read
}

// readObject starts reading raw. ok is false when raw is not an object.
func readObject(raw []byte) objectReader {
	i := skipSpace(raw, 0)
	if i == len(raw) || raw[i] != '{' {
		return objectReader{}
	}
	i = skipSpace(raw, i+1)
	if i < len(raw) && raw[i] == '}' {
		return objectReader{ok: true, done: true}
	}
	return objectReader{raw: raw, i: i, ok: true}
}

// next returns the next member: its key as a JSON string literal, quotes
// included, and its raw value, both aliasing raw. It returns false after the
// last member, and also, clearing ok, when raw is not a well-formed object.
func (r *objectReader) next() (key, value []byte, more bool) {
	if !r.ok || r.done {
		return nil, nil, false
	}
	raw, i := r.raw, r.i
	if i >= len(raw) || raw[i] != '"' {
		return r.fail()
	}
	end := jsonx.StringEnd(raw, i)
	if end < 0 {
		return r.fail()
	}
	key = raw[i:end:end]
	if i = skipSpace(raw, end); i == len(raw) || raw[i] != ':' {
		return r.fail()
	}
	i = skipSpace(raw, i+1)
	vend := valueEnd(raw, i)
	if vend < 0 {
		return r.fail()
	}
	value = raw[i:vend:vend]
	if i = skipSpace(raw, vend); i == len(raw) {
		return r.fail()
	}
	switch raw[i] {
	case '}':
		r.done = true
	case ',':
		r.i = skipSpace(raw, i+1)
	default:
		return r.fail()
	}
	return key, value, true
}

// fail marks raw as not a well-formed object and ends the reading.
func (r *objectReader) fail() (key, value []byte, more bool) {
	r.ok = false
	return nil, nil, false
}

// arrayReader reads the elements of a JSON array in order, without storing
// them. raw must be valid JSON; on anything else it stops and clears ok.
type arrayReader struct {
	raw  []byte
	i    int  // the next element
	ok   bool // raw has been a well-formed array so far
	done bool // the closing bracket has been read
}

// readArray starts reading raw. ok is false when raw is not an array.
func readArray(raw []byte) arrayReader {
	i := skipSpace(raw, 0)
	if i == len(raw) || raw[i] != '[' {
		return arrayReader{}
	}
	i = skipSpace(raw, i+1)
	if i < len(raw) && raw[i] == ']' {
		return arrayReader{ok: true, done: true}
	}
	return arrayReader{raw: raw, i: i, ok: true}
}

// next returns the next element, aliasing raw. It returns false after the last
// element, and also, clearing ok, when raw is not a well-formed array.
func (r *arrayReader) next() (json.RawMessage, bool) {
	if !r.ok || r.done {
		return nil, false
	}
	raw, i := r.raw, r.i
	end := valueEnd(raw, i)
	if end < 0 {
		r.ok = false
		return nil, false
	}
	value := raw[i:end:end]
	switch i = skipSpace(raw, end); {
	case i == len(raw):
		r.ok = false
		return nil, false
	case raw[i] == ']':
		r.done = true
	case raw[i] == ',':
		r.i = skipSpace(raw, i+1)
	default:
		r.ok = false
		return nil, false
	}
	return value, true
}

// index returns the position of the member named name, or -1.
func (o object) index(name string) int {
	if len(o) <= linearLookups {
		for i := range o {
			if string(o[i].key) == name {
				return i
			}
		}
		return -1
	}
	// A lower-bound search: one comparison per step, then one equality check.
	lo, hi := 0, len(o)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if string(o[mid].key) < name {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(o) && string(o[lo].key) == name {
		return lo
	}
	return -1
}

// get returns the member named name, which may be null.
func (o object) get(name string) (json.RawMessage, bool) {
	if i := o.index(name); i >= 0 {
		return o[i].value, true
	}
	return nil, false
}

// matches reports whether o's keys are exactly the n keys key(0) to key(n-1),
// which may repeat. missing is the position of the first key o lacks, or -1.
func (o object) matches(n int, key func(int) string) (missing int, exact bool) {
	if len(o) > n {
		// Some member cannot be matched; a missing key still takes precedence.
		for i := range n {
			if o.index(key(i)) < 0 {
				return i, false
			}
		}
		return -1, false
	}
	var (
		buf  [256]bool
		seen []bool
	)
	if len(o) <= len(buf) {
		seen = buf[:len(o)]
	} else {
		seen = make([]bool, len(o))
	}
	matched := 0
	for i := range n {
		j := o.index(key(i))
		if j < 0 {
			return i, false
		}
		if !seen[j] {
			seen[j] = true
			matched++
		}
	}
	return -1, matched == len(o)
}

// lookup returns the member f.key and whether it is present with a non-null
// value.
func (o object) lookup(f field) (json.RawMessage, bool) {
	raw, ok := o.get(f.key)
	if !ok || isNull(raw) {
		return nil, false
	}
	return raw, true
}

// required returns the member f.key, which must be present and not null.
func (o object) required(f field) (json.RawMessage, error) {
	raw, ok := o.get(f.key)
	switch {
	case !ok:
		return nil, malformed(f, "is required")
	case isNull(raw):
		return nil, malformed(f, "must not be null")
	}
	return raw, nil
}

// requiredText returns the content of a member that must be a JSON string. It
// aliases the document unless the string has escapes.
func (o object) requiredText(f field) ([]byte, error) {
	raw, err := o.required(f)
	if err != nil {
		return nil, err
	}
	return asText(raw, f)
}

// optionalString returns a string member; absent and null both yield "".
func (o object) optionalString(f field) (string, error) {
	raw, ok := o.lookup(f)
	if !ok {
		return "", nil
	}
	text, err := asText(raw, f)
	if err != nil {
		return "", err
	}
	return string(text), nil
}

// requiredUnit returns a member that must be a number in [0,1].
func (o object) requiredUnit(f field) (float64, error) {
	raw, err := o.required(f)
	if err != nil {
		return 0, err
	}
	return asUnit(raw, f)
}

// optionalUnit returns a number in [0,1], or nil when absent or null.
func (o object) optionalUnit(f field) (*float64, error) {
	raw, ok := o.lookup(f)
	if !ok {
		return nil, nil //nolint:nilnil // absent and null both mean "not reported"
	}
	v, err := asUnit(raw, f)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// optionalCount returns a non-negative integer, or nil when absent or null.
func (o object) optionalCount(f field) (*int64, error) {
	raw, ok := o.lookup(f)
	if !ok {
		return nil, nil //nolint:nilnil // absent and null both mean "not reported"
	}
	n, err := asInt(raw, f, 64)
	if err != nil {
		return nil, err
	}
	if n < 0 {
		return nil, malformed(f, "must not be negative")
	}
	return &n, nil
}

// asText returns the content of the JSON string raw. It aliases raw unless the
// string has escapes, which are decoded exactly as encoding/json decodes them.
func asText(raw json.RawMessage, f field) ([]byte, error) {
	if firstByte(raw) != '"' {
		return nil, malformed(f, "must be a string")
	}
	text, ok := unquoteKey(bytes.TrimSpace(raw))
	if !ok {
		return nil, malformed(f, "must be a string")
	}
	return text, nil
}

// asFinite parses a JSON number as float64, rejecting overflow to infinity.
func asFinite(raw json.RawMessage, f field) (float64, error) {
	if !isNumber(raw) {
		return 0, malformed(f, "must be a number")
	}
	v, err := strconv.ParseFloat(string(bytes.TrimSpace(raw)), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, malformed(f, "must be a finite number")
	}
	return v, nil
}

func asUnit(raw json.RawMessage, f field) (float64, error) {
	v, err := asFinite(raw, f)
	if err != nil {
		return 0, err
	}
	if v < 0 || v > 1 {
		return 0, malformed(f, "must be in [0,1]")
	}
	return v, nil
}

// asInt parses a JSON number whose value is an integer that fits a signed
// integer of bitSize bits; see parseInt.
func asInt(raw json.RawMessage, f field, bitSize int) (int64, error) {
	n, reason := parseInt(raw, bitSize)
	if reason != "" {
		return 0, malformed(f, reason)
	}
	return n, nil
}

// parseInt parses a JSON number whose value is an integer that fits a signed
// integer of bitSize bits, or returns why it cannot. As in JSON Schema, the
// value decides, not the spelling: 145.0, 1.45e2 and 1450e-1 are the integer
// 145, while 145.5 and 1e-3 are not integers. The value is computed exactly, so
// a literal never rounds into range or onto a neighboring integer.
func parseInt(raw []byte, bitSize int) (int64, string) {
	if !isNumber(raw) {
		return 0, "must be an integer"
	}
	s := bytes.TrimSpace(raw)
	if !bytes.ContainsAny(s, ".eE") {
		n, err := strconv.ParseInt(string(s), 10, bitSize)
		if err != nil {
			return 0, "is out of range"
		}
		return n, ""
	}
	return parseDecimal(s, bitSize)
}

// parseDecimal is parseInt for a literal with a fraction or an exponent,
// -?int(.frac)?([eE][+-]?exp)?, whose value is the digits of int and frac with
// the decimal point moved exp places. The value is an integer when every digit
// after the moved point is zero; its digits before the point then go to
// strconv.ParseInt, which judges the range.
func parseDecimal(s []byte, bitSize int) (int64, string) {
	neg := s[0] == '-'
	if neg {
		s = s[1:]
	}
	intPart, rest := leadingDigits(s)
	var frac []byte
	if len(rest) > 0 && rest[0] == '.' {
		frac, rest = leadingDigits(rest[1:])
	}
	exp, rest := exponent(rest, len(s)+20)
	if len(intPart) == 0 || len(rest) > 0 {
		return 0, "must be an integer"
	}
	d := mantissa{intPart: intPart, frac: frac}
	digits := len(intPart) + len(frac)
	point := len(intPart) + exp
	for k := max(point, 0); k < digits; k++ {
		if d.digit(k) != '0' {
			return 0, "must be an integer"
		}
	}
	// The integer's digits, without leading zeros. Twenty significant digits
	// exceed every int64, so building stops there.
	var buf [21]byte
	out := buf[:0]
	if neg {
		out = append(out, '-')
	}
	sign := len(out)
	for k := 0; k < point && len(out)-sign <= 19; k++ {
		c := d.digit(k)
		if c == '0' && len(out) == sign {
			if k >= digits {
				break // only zeros remain: the value is zero
			}
			continue
		}
		out = append(out, c)
	}
	if len(out) == sign {
		return 0, "" // zero, however it is spelled, including -0.0
	}
	n, err := strconv.ParseInt(string(out), 10, bitSize)
	if err != nil {
		return 0, "is out of range"
	}
	return n, ""
}

// mantissa is the digits of a decimal literal, the integer part followed by the
// fraction.
type mantissa struct {
	intPart, frac []byte
}

// digit returns digit k, or '0' past the last: the zeros an exponent appends.
func (m mantissa) digit(k int) byte {
	switch {
	case k < len(m.intPart):
		return m.intPart[k]
	case k < len(m.intPart)+len(m.frac):
		return m.frac[k-len(m.intPart)]
	}
	return '0'
}

// exponent parses the exponent, [eE][+-]?digits, at the start of s and returns
// its value and the rest of s; without one it returns 0 and s. Digits past
// bound are not accumulated: beyond it the sign alone decides whether the value
// is too large or not an integer, so the exponent cannot overflow.
func exponent(s []byte, bound int) (int, []byte) {
	if len(s) == 0 || (s[0] != 'e' && s[0] != 'E') {
		return 0, s
	}
	rest := s[1:]
	neg := len(rest) > 0 && rest[0] == '-'
	if len(rest) > 0 && (rest[0] == '+' || rest[0] == '-') {
		rest = rest[1:]
	}
	digits, rest := leadingDigits(rest)
	if len(digits) == 0 {
		return 0, s
	}
	exp := 0
	for _, c := range digits {
		if exp <= bound {
			exp = exp*10 + int(c-'0')
		}
	}
	if neg {
		exp = -exp
	}
	return exp, rest
}

// leadingDigits splits s after its leading run of ASCII digits.
func leadingDigits(s []byte) (digits, rest []byte) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i], s[i:]
}

func firstByte(raw json.RawMessage) byte {
	if i := skipSpace(raw, 0); i < len(raw) {
		return raw[i]
	}
	return 0
}

func isNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// isNumber reports whether raw, already known to be valid JSON, is a number.
func isNumber(raw json.RawMessage) bool {
	c := firstByte(raw)
	return c == '-' || (c >= '0' && c <= '9')
}

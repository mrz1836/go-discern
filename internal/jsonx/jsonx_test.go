package jsonx_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern/internal/jsonx"
	"github.com/mrz1836/go-discern/internal/testutil"
)

// wideObject returns an object with n distinct keys k0000, k0001 and so on,
// followed by extra members.
func wideObject(n int, extra string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"k%04d":%d`, i, i)
	}
	b.WriteString(extra)
	b.WriteByte('}')
	return b.String()
}

// nested returns depth objects nested inside one another, the innermost
// holding inner.
func nested(depth int, inner string) string {
	return strings.Repeat(`{"n":`, depth) + inner + strings.Repeat(`}`, depth)
}

// duplicateCase is a valid document and the offset DuplicateKeyEnd reports.
type duplicateCase struct {
	name string
	doc  string
	end  int // -1 when no object repeats a key
}

func duplicateCases() []duplicateCase {
	wideRepeat := wideObject(40, `,"k0007":1`)
	wideEscaped := wideObject(40, testutil.Esc(`,"k~u0030007":1`))
	lateInner := wideObject(30, `,"k0029":1`)
	lateOuter := strings.TrimSuffix(wideObject(30, ""), "}") + `,"z":`
	return []duplicateCase{
		{name: "scalar", doc: `1`, end: -1},
		{name: "string", doc: `"a"`, end: -1},
		{name: "empty object", doc: `{}`, end: -1},
		{name: "empty array", doc: `[]`, end: -1},
		{name: "distinct keys", doc: `{"a":1,"b":2}`, end: -1},
		{name: "repeated key", doc: `{"a":1,"a":2}`, end: 13},
		{name: "empty keys", doc: `{"":1,"":2}`, end: 11},
		{name: "escaped spelling", doc: testutil.Esc(`{"a":1,"~u0061":2}`), end: 18},
		{name: "escaped spellings on both sides", doc: testutil.Esc(`{"~u0061b":1,"a~u0062":2}`), end: 25},
		{name: "escaped quote and backslash", doc: testutil.Esc(`{"~"":1,"~~":2,"~~~"":3}`), end: -1},
		{name: "escaped quote repeated", doc: testutil.Esc(`{"~"":1,"~u0022":2}`), end: 19},
		{name: "solidus escape", doc: testutil.Esc(`{"a/b":1,"a~/b":2}`), end: 18},
		{name: "control escapes", doc: testutil.Esc(`{"~b~f~n~r~t":1,"~u0008~u000c~u000a~u000d~u0009":2}`), end: 51},
		{name: "surrogate pair and raw rune", doc: testutil.Esc(`{"~ud83d~ude00":1,"😀":2}`), end: 27},
		{name: "lone surrogates decode to U+FFFD", doc: testutil.Esc(`{"~ud800":1,"~udfff":2}`), end: 23},
		{name: "lone surrogate and raw U+FFFD", doc: testutil.Esc(`{"~ud800":1,"�":2}`), end: 20},
		{name: "high surrogate before a non-surrogate escape", doc: testutil.Esc(`{"~ud800~u0061":1,"�a":2}`), end: 27},
		{name: "upper-case hex", doc: testutil.Esc(`{"~u00E9":1,"é":2}`), end: 19},
		{name: "prefix is not equal", doc: `{"ab":1,"a":2,"abc":3}`, end: -1},
		{name: "keys differ after an escape", doc: testutil.Esc(`{"~u0061b":1,"~u0061c":2}`), end: -1},
		{name: "escape against a raw byte", doc: testutil.Esc(`{"~u0061":1,"b":2}`), end: -1},
		{name: "same key in sibling objects", doc: `[{"a":1},{"a":1}]`, end: -1},
		{name: "same key at different depths", doc: `{"a":{"a":{"a":1}}}`, end: -1},
		{name: "repeat inside a nested object", doc: `{"a":{"b":1,"b":2},"c":3}`, end: 18},
		{name: "repeat inside an array", doc: `{"a":[1,{"b":1,"b":2}]}`, end: 21},
		{name: "inner repeat closes first", doc: `{"a":1,"x":{"b":1,"b":2},"a":2}`, end: 24},
		{name: "values are not keys", doc: `{"a":"a","b":["a","a"],"c":{"d":"a"}}`, end: -1},
		{name: "structural bytes inside strings", doc: testutil.Esc(`{"a":"}{][,:~"","{":"~~","[":1}`), end: -1},
		{name: "whitespace", doc: " {\n\t\"a\" : 1 ,\r \"a\" : 2 } ", end: 24},
		{name: "wide object on the sorted path", doc: wideObject(40, ""), end: -1},
		{name: "wide object with a repeat", doc: wideRepeat, end: len(wideRepeat)},
		{name: "wide object with an escaped repeat", doc: wideEscaped, end: len(wideEscaped)},
		{name: "many open keys", doc: wideObject(30, `,"z":`+wideObject(30, "")), end: -1},
		{name: "many open keys, inner repeat closes first", doc: lateOuter + lateInner + `,"k0000":1}`, end: len(lateOuter) + len(lateInner)},
		{name: "deep nesting", doc: nested(40, `{"a":1,"a":2}`), end: 213},
		{name: "many open keys around arrays", doc: wideObject(40, `,"z":[{"a":[1,{"a":2}]},[]]`), end: -1},
		{name: "deep arrays", doc: strings.Repeat("[", 40) + `{"a":1,"b":2}` + strings.Repeat("]", 40), end: -1},
	}
}

func TestDuplicateKeyEnd(t *testing.T) {
	for _, tt := range duplicateCases() {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte(tt.doc)
			require.True(t, json.Valid(data), "the case must be valid JSON")
			require.True(t, utf8.Valid(data), "the case must be valid UTF-8")
			assert.Equal(t, tt.end, jsonx.DuplicateKeyEnd(data))
			assert.Equal(t, tt.end >= 0, jsonx.HasDuplicateKey(data))
			_, dup := testutil.TokenWalk(data)
			assert.Equal(t, dup, jsonx.HasDuplicateKey(data), "encoding/json disagrees")
			if tt.end >= 0 {
				assert.Equal(t, byte('}'), data[tt.end-1], "the offset is just past a closing brace")
			}
		})
	}
}

func TestDuplicateKeyEndInvalidInputNeverPanics(t *testing.T) {
	// The result is unspecified for input that is not valid JSON; the scan
	// still has to finish without panicking.
	for _, s := range []string{
		``, `}`, `]`, `{`, `"`, `{"a`, `{"a":1}}`, `{"a":1]`, `[}`, `{"a" "a"}`, testutil.Esc(`{"~x":1,"~x":2}`),
		testutil.Esc(`{"~u12":1,"~u12":2}`), testutil.Esc(`{"a~`), `{"a":1,"a`, wideObject(40, `,"k0007`), wideObject(40, `}}}`),
		"{\"\xff\":1,\"\xfe\":2}",
	} {
		t.Run(s, func(t *testing.T) {
			require.NotPanics(t, func() { _ = jsonx.DuplicateKeyEnd([]byte(s)) })
		})
	}
}

func TestDuplicateKeyEndAllocations(t *testing.T) {
	testutil.SkipUnderRace(t)
	scan := func(doc string) func() {
		data := []byte(doc)
		return func() { _ = jsonx.DuplicateKeyEnd(data) }
	}
	testutil.CheckCeilings(t, []testutil.Ceiling{
		{Name: "small document", Run: scan(`{"model":"jev-1","answers":{"urgent":{"type":"noul","noul":0.25}},"usage":{"input_tokens":1}}`)},
		{Name: "escaped keys", Run: scan(testutil.Esc(`{"~u0061":1,"b~n":{"~ud83d~ude00":[1,2]}}`))},
		{Name: "sixteen keys, sorted path", Run: scan(wideObject(17, ""))},
		{Name: "thirty-two open keys", Run: scan(wideObject(32, ""))},
		{Name: "more open keys than the stack buffer", Max: 1, Run: scan(wideObject(255, ""))},
		{Name: "deeper than the frame buffer", Max: 1, Run: scan(nested(17, `1`))},
	})
}

func TestDuplicateKeyEndMemoryGrowsWithKeysOnly(t *testing.T) {
	testutil.SkipUnderRace(t)
	// The most keys a document of this size can hold open at once: one short
	// key per five bytes, all in one object.
	const size = 4 << 20
	var b strings.Builder
	b.WriteByte('{')
	keys := 0
	for b.Len() < size {
		if keys > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"%x":0`, keys)
		keys++
	}
	b.WriteByte('}')
	many := []byte(b.String())
	require.Equal(t, -1, jsonx.DuplicateKeyEnd(many))

	perOp := testutil.BytesPerOp(func() { _ = jsonx.DuplicateKeyEnd(many) })
	t.Logf("%d bytes allocated for %d open keys in a %d-byte document", perOp, keys, len(many))
	// One int per key, allocated once: never a regrown copy, and well under
	// the document's own size.
	assert.LessOrEqual(t, perOp, int64(8*keys)+int64(8*keys)/8)

	// Escaped keys are compared in place, so they cost nothing more.
	escaped := []byte(strings.ReplaceAll(string(many), `":0`, testutil.Esc(`~u0030":0`)))
	require.Equal(t, -1, jsonx.DuplicateKeyEnd(escaped))
	perOp = testutil.BytesPerOp(func() { _ = jsonx.DuplicateKeyEnd(escaped) })
	t.Logf("%d bytes allocated for %d escaped keys in a %d-byte document", perOp, keys, len(escaped))
	assert.LessOrEqual(t, perOp, int64(8*keys)+int64(8*keys)/8)
}

// unquoteOracle decodes s with encoding/json.
func unquoteOracle(s []byte) (string, bool) {
	var v string
	if err := json.Unmarshal(s, &v); err != nil {
		return "", false
	}
	return v, true
}

func unquoteCases() []string {
	return []string{
		`""`, `"a"`, `"plain ASCII text"`, `"é and 😀 raw"`,
		testutil.Esc(`"~" ~~ ~/ ~b ~f ~n ~r ~t"`), testutil.Esc(`"~u0041~u00e9~u00E9~u20ac"`), testutil.Esc(`"~ud83d~ude00"`),
		testutil.Esc(`"~ud800"`), testutil.Esc(`"~udfff"`), testutil.Esc(`"~ud800x"`), testutil.Esc(`"~ud800~u0041"`), testutil.Esc(`"~ud800~ud800"`), testutil.Esc(`"~udc00~ud800"`),
		testutil.Esc(`"~ud83d~ude00~ud83d"`), testutil.Esc(`"a~u0000b"`), "\"\x7f\"", "\"\xff\"", "\"a\xc3\"", "\"\xed\xa0\x80\"",
		// Not valid string literals.
		``, `"`, `a`, `"a`, `a"`, ` "a"`, `"a" `, `"a"b"`, "\"a\nb\"", "\"\x00\"",
		testutil.Esc(`"~"`), testutil.Esc(`"~x"`), testutil.Esc(`"~'"`), testutil.Esc(`"~u12"`), testutil.Esc(`"~u12g4"`), testutil.Esc(`"~uZZZZ"`), testutil.Esc(`"~u"`), testutil.Esc(`"a~`),
	}
}

func TestAppendUnquotedMatchesEncodingJSON(t *testing.T) {
	for _, s := range unquoteCases() {
		t.Run(s, func(t *testing.T) {
			assertUnquoteMatches(t, []byte(s))
		})
	}
}

// assertUnquoteMatches checks AppendUnquoted against encoding/json for s, when
// s is spelled as a bare string literal, and that it appends to dst without
// touching what dst already holds.
func assertUnquoteMatches(t *testing.T, s []byte) {
	t.Helper()
	got, ok := jsonx.AppendUnquoted(nil, s)
	literal := len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"'
	if want, wantOK := unquoteOracle(s); literal || !wantOK {
		require.Equal(t, wantOK, ok, "AppendUnquoted and encoding/json disagree on validity")
		if ok {
			require.Equal(t, want, string(got))
		}
	}
	if !literal {
		require.False(t, ok, "only a bare string literal unquotes")
	}
	prefix := []byte("prefix:")
	appended, ok2 := jsonx.AppendUnquoted(prefix, s)
	require.Equal(t, ok, ok2)
	if !ok2 {
		require.Equal(t, "prefix:", string(appended))
		return
	}
	require.Equal(t, "prefix:"+string(got), string(appended))
}

func TestAppendUnquotedAllocatesOnlyToGrow(t *testing.T) {
	testutil.SkipUnderRace(t)
	s := []byte(testutil.Esc(`"refund ~u00e9 ~ud83d~ude00 please"`))
	buf := make([]byte, 0, 64)
	allocs := testing.AllocsPerRun(100, func() { _, _ = jsonx.AppendUnquoted(buf[:0], s) })
	assert.Zero(t, allocs)
}

func FuzzHasDuplicateKey(f *testing.F) {
	for _, tt := range duplicateCases() {
		f.Add([]byte(tt.doc))
	}
	f.Add([]byte(`{"a":1,"a"`))
	f.Add([]byte("{\"\xff\":1,\"\xfe\":2}"))
	f.Fuzz(func(t *testing.T, data []byte) {
		got := jsonx.HasDuplicateKey(data) // never panics, whatever the input
		if !json.Valid(data) || !utf8.Valid(data) {
			return
		}
		_, dup := testutil.TokenWalk(data)
		require.Equal(t, dup, got)
		end := jsonx.DuplicateKeyEnd(data)
		require.Equal(t, got, end >= 0)
		if end >= 0 {
			require.Equal(t, byte('}'), data[end-1])
		}
	})
}

func FuzzAppendUnquoted(f *testing.F) {
	for _, s := range unquoteCases() {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, s []byte) {
		assertUnquoteMatches(t, s)
	})
}

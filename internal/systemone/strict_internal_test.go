package systemone

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/testutil"
)

// maxOracleInput bounds the inputs compared with testutil.TokenWalk. encoding/json
// rejects nesting deeper than 10,000 levels, which the token walk never
// checked; a shorter input cannot nest that deep.
const maxOracleInput = 10000

// elements collects the elements of the array raw, as an arrayReader reads
// them, and reports whether raw is a well-formed array.
func elements(raw []byte) ([]json.RawMessage, bool) {
	r := readArray(raw)
	var out []json.RawMessage
	for v, more := r.next(); more; v, more = r.next() {
		out = append(out, v)
	}
	return out, r.ok
}

// parseAll parses raw as an object, keeping every member.
func parseAll(t *testing.T, d *decoder, raw []byte) object {
	t.Helper()
	o, reason := d.parse(raw, math.MaxInt, nil)
	require.Empty(t, reason)
	return o
}

// anyRequest is a request with no questions, for decoders built in tests.
func anyRequest() *discern.Request { return &discern.Request{} }

// assertSameStructure checks that the decoder parses raw, a value from a
// document checkStrict accepted, exactly as encoding/json does, recursively.
func assertSameStructure(t *testing.T, d *decoder, raw json.RawMessage, depth int) {
	t.Helper()
	if depth > 64 {
		return
	}
	switch firstByte(raw) {
	case '{':
		o := parseAll(t, d, raw)
		var want map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &want))
		require.Len(t, o, len(want))
		for k, v := range want {
			got, ok := o.get(k)
			require.True(t, ok, "key %q", k)
			require.Equal(t, string(v), string(got))
			assertSameStructure(t, d, got, depth+1)
		}
	case '[':
		got, ok := elements(raw)
		require.True(t, ok)
		var want []json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &want))
		require.Len(t, got, len(want))
		for i := range want {
			require.Equal(t, string(want[i]), string(got[i]))
			assertSameStructure(t, d, got[i], depth+1)
		}
	case '"':
		got, err := asText(raw, field{})
		require.NoError(t, err)
		var want string
		require.NoError(t, json.Unmarshal(raw, &want))
		require.Equal(t, want, string(got))
	}
}

// wideObject returns an object with n distinct keys, enough to take the sorted
// lookup path when n exceeds linearLookups, followed by extra members.
func wideObject(n int, extra string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"k%02d":%d`, i, i)
	}
	b.WriteString(extra)
	b.WriteByte('}')
	return b.String()
}

// strictSeeds are documents the oracle and checkStrict must judge alike.
func strictSeeds() []string {
	return []string{
		`{}`, `[]`, `null`, `0`, `"x"`, ` {"a":1} `, `{"a":1}x`, `{"a":1} {}`, `{"a":1,}`, `{"a"}`, `[1,]`,
		`{"a":1,"a":2}`, `{"a":1,"\u0061":2}`, `{"a":{"b":1,"b":1}}`, `[{"a":1},{"a":1}]`, `[{"a":1,"a":1}]`,
		`{"a":"\"}","b":"\\","c":"\\\"","d":"}{]["}`, `{"\"":1,"\\\"":2}`, `{"\ud800":1,"\udbff":2}`,
		`{"e":1e400,"n":-0.0e+5,"t":true,"f":false,"z":null}`, `{"a":[1,{"b":[2,{"c":3}]}]}`,
		`{"a":"` + strings.Repeat(`\\`, 9) + `"}`, `{"a":"x"` + strings.Repeat(" ", 3) + `}`, "{\n\t\"a\" :\r [ ] }",
		wideObject(20, ""), wideObject(20, `,"k07":1`), wideObject(20, `,"k\u0030\u0037":1`), wideObject(40, `,"z":{"k":1,"k":2}`),
		"\xff", `{"a":"` + "\xff" + `"}`, ``, ` `, `{`, `"`, `[[[]]`,
	}
}

func TestCheckStrictMatchesTokenWalk(t *testing.T) {
	for _, s := range strictSeeds() {
		t.Run(s, func(t *testing.T) {
			data := []byte(s)
			err := checkStrict(data)
			valid, dup := testutil.TokenWalk(data)
			assert.Equal(t, valid && !dup, err == nil, "checkStrict error: %v", err)
			if err == nil {
				d := newDecoder(data, anyRequest())
				assertSameStructure(t, &d, data, 0)
			} else {
				require.ErrorIs(t, err, ErrMalformed)
			}
		})
	}
}

func TestCheckStrictRejectsDeepNesting(t *testing.T) {
	deep := `{"answers":{},"extra":` + strings.Repeat("[", 10001) + strings.Repeat("]", 10001) + `}`
	require.ErrorIs(t, checkStrict([]byte(deep)), ErrMalformed)
	shallow := `{"answers":{},"extra":` + strings.Repeat("[", 9000) + strings.Repeat("]", 9000) + `}`
	require.NoError(t, checkStrict([]byte(shallow)))
}

func TestCheckStrictErrorsExplainTheFailure(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "invalid UTF-8", body: "\"\xff\"", want: "response: is not valid UTF-8"},
		{name: "syntax error", body: `{"a":}`, want: "response: is not valid JSON (offset 6)"},
		{name: "empty", body: ``, want: "response: is not valid JSON"},
		{name: "trailing data", body: `{"a":1} x`, want: "response: has data after the top-level value"},
		{name: "duplicate key", body: `{"a":{"b":1,"b":2}}`, want: "response: has a duplicate key in the object ending at offset 18"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkStrict([]byte(tt.body))
			require.ErrorIs(t, err, ErrMalformed)
			assert.Equal(t, "systemone: malformed response: "+tt.want, err.Error())
		})
	}
}

func TestFieldString(t *testing.T) {
	tests := []struct {
		f    field
		want string
	}{
		{f: field{key: "model"}, want: "model"},
		{f: field{key: "usage"}.member("input_tokens"), want: "usage.input_tokens"},
		{f: field{answer: "queue"}, want: `answers["queue"]`},
		{f: field{answer: "queue"}.member("probabilities"), want: `answers["queue"].probabilities`},
		{f: field{answer: `say "hi"`}.member("type"), want: `answers["say \"hi\""].type`},
		{f: field{key: "errors[2]"}.member("code"), want: "errors[2].code"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.f.String())
		})
	}
}

func TestObjectLookupsBothPaths(t *testing.T) {
	// 300 members also exceed the stack buffer that matches uses for its marks.
	for _, n := range []int{linearLookups, linearLookups + 1, 255, 300} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			data := []byte(wideObject(n, ""))
			require.NoError(t, checkStrict(data))
			d := newDecoder(data, anyRequest())
			o := parseAll(t, &d, data)
			require.Len(t, o, n)
			for i := range n {
				raw, ok := o.get(fmt.Sprintf("k%02d", i))
				require.True(t, ok)
				assert.Equal(t, fmt.Sprint(i), string(raw))
			}
			_, ok := o.get("K00")
			assert.False(t, ok)
			missing, exact := o.matches(n, func(i int) string { return fmt.Sprintf("k%02d", i) })
			assert.Equal(t, -1, missing)
			assert.True(t, exact)
		})
	}
}

func TestObjectMatchesRepeatedExpectedKeys(t *testing.T) {
	data := []byte(`{"a":1,"b":2}`)
	d := newDecoder(data, anyRequest())
	o := parseAll(t, &d, data)
	tests := []struct {
		name        string
		keys        []string
		wantMissing int
		wantExact   bool
	}{
		{name: "exact", keys: []string{"b", "a"}, wantMissing: -1, wantExact: true},
		{name: "repeated key leaves a member unmatched", keys: []string{"a", "a"}, wantMissing: -1},
		{name: "missing key", keys: []string{"a", "c"}, wantMissing: 1},
		{name: "fewer keys than members", keys: []string{"a"}, wantMissing: -1},
		{name: "missing key takes precedence", keys: []string{"c"}, wantMissing: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			missing, exact := o.matches(len(tt.keys), func(i int) string { return tt.keys[i] })
			assert.Equal(t, tt.wantMissing, missing)
			assert.Equal(t, tt.wantExact, exact)
		})
	}
}

func TestParsersRejectWhatTheyDoNotRecognize(t *testing.T) {
	// The parsers only ever see documents checkStrict accepted; on anything
	// else they must fail rather than panic or misread.
	for _, s := range []string{``, `{`, `{"a"`, `{"a":`, `{"a":1`, `{"a":1;`, `{"a":1 x}`, `{"\u12":1}`, `{1:2}`, `{"a":"`, `[`, `[1`, `[1;`, `[1 x]`, `["`, `"`, `x`} {
		t.Run(s, func(t *testing.T) {
			var d decoder
			if strings.HasPrefix(s, "[") {
				_, ok := elements([]byte(s))
				assert.False(t, ok)
				return
			}
			_, err := d.keyed([]byte(s), field{key: "x"}, 0)
			require.ErrorIs(t, err, ErrMalformed)
			assert.Empty(t, d.members)
		})
	}
	assert.Equal(t, -1, valueEnd([]byte(`[1,"`), 0))
	assert.Equal(t, -1, valueEnd([]byte(`[1,2`), 0))
	assert.Equal(t, -1, valueEnd([]byte(`,`), 0))
	assert.Equal(t, -1, valueEnd(nil, 0))
}

func FuzzStrictMatchesTokenWalk(f *testing.F) {
	for _, s := range strictSeeds() {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxOracleInput {
			t.Skip("longer inputs may nest deeper than encoding/json allows")
		}
		err := checkStrict(data)
		valid, dup := testutil.TokenWalk(data)
		require.Equal(t, valid && !dup, err == nil, "checkStrict error: %v", err)
		if err != nil {
			require.ErrorIs(t, err, ErrMalformed)
			return
		}
		d := newDecoder(data, anyRequest())
		assertSameStructure(t, &d, data, 0)
	})
}

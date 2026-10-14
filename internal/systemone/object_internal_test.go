package systemone

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern/internal/testutil"
)

// The reasons parseInt gives.
const (
	notInteger = "must be an integer"
	outOfRange = "is out of range"
)

func TestParseIntExactValues(t *testing.T) {
	tests := []struct {
		raw     string
		bitSize int
		want    int64
		reason  string
	}{
		{raw: `145`, bitSize: 64, want: 145},
		{raw: ` 145 `, bitSize: 64, want: 145},
		{raw: `145.0`, bitSize: 64, want: 145},
		{raw: `145.000`, bitSize: 64, want: 145},
		{raw: `1.45e2`, bitSize: 64, want: 145},
		{raw: `1.450E+2`, bitSize: 64, want: 145},
		{raw: `1450e-1`, bitSize: 64, want: 145},
		{raw: `14500e-2`, bitSize: 64, want: 145},
		{raw: `1e3`, bitSize: 64, want: 1000},
		{raw: `0.0145e4`, bitSize: 64, want: 145},
		{raw: `-7.0`, bitSize: 64, want: -7},
		{raw: `0`, bitSize: 64, want: 0},
		{raw: `-0`, bitSize: 64, want: 0},
		{raw: `-0.0e7`, bitSize: 64, want: 0},
		{raw: `0e999999999999999999999`, bitSize: 64, want: 0},
		{raw: `0.000e-5`, bitSize: 64, want: 0},
		{raw: `9223372036854775807`, bitSize: 64, want: math.MaxInt64},
		{raw: `9223372036854775807.000`, bitSize: 64, want: math.MaxInt64},
		{raw: `9.223372036854775807e18`, bitSize: 64, want: math.MaxInt64},
		{raw: `-9.223372036854775808e18`, bitSize: 64, want: math.MinInt64},
		{raw: `1e18`, bitSize: 64, want: 1_000_000_000_000_000_000},
		{raw: `2147483647.0`, bitSize: 32, want: math.MaxInt32},
		{raw: `-2.147483648e9`, bitSize: 32, want: math.MinInt32},
		{raw: `9007199254740993.0`, bitSize: 64, want: 9007199254740993},

		{raw: `145.5`, bitSize: 64, reason: notInteger},
		{raw: `1e-3`, bitSize: 64, reason: notInteger},
		{raw: `1451e-1`, bitSize: 64, reason: notInteger},
		{raw: `1e-999999999999999999999`, bitSize: 64, reason: notInteger},
		{raw: `123456789012345678901234567890e-11`, bitSize: 64, reason: notInteger},
		{raw: `9223372036854775808`, bitSize: 64, reason: outOfRange},
		{raw: `9223372036854775808.0`, bitSize: 64, reason: outOfRange},
		{raw: `9.223372036854775808e18`, bitSize: 64, reason: outOfRange},
		{raw: `-9.223372036854775809e18`, bitSize: 64, reason: outOfRange},
		{raw: `1e19`, bitSize: 64, reason: outOfRange},
		{raw: `1e999999999999999999999`, bitSize: 64, reason: outOfRange},
		{raw: `2147483648.0`, bitSize: 32, reason: outOfRange},
		{raw: `"145"`, bitSize: 64, reason: notInteger},
		{raw: `null`, bitSize: 64, reason: notInteger},
		{raw: ``, bitSize: 64, reason: notInteger},
		// Not JSON numbers; the decoder never sees them, but they must not pass.
		{raw: `-.5`, bitSize: 64, reason: notInteger},
		{raw: `1.5x`, bitSize: 64, reason: notInteger},
		{raw: `1e`, bitSize: 64, reason: notInteger},
		{raw: `1.0e+`, bitSize: 64, reason: notInteger},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, reason := parseInt([]byte(tt.raw), tt.bitSize)
			assert.Equal(t, tt.reason, reason)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestKeyIn(t *testing.T) {
	tests := []struct {
		lit  string
		want bool
	}{
		{lit: `"code"`, want: true},
		{lit: `"message"`, want: true},
		{lit: testutil.Esc(`"c~u006fde"`), want: true},
		{lit: testutil.Esc(`"~u0063~u006f~u0064~u0065"`), want: true},
		{lit: testutil.Esc(`"~u006d~u0065~u0073~u0073~u0061~u0067~u0065"`), want: true},
		{lit: `"Code"`},
		{lit: `"codes"`},
		{lit: `""`},
		{lit: testutil.Esc(`"c~u006fde~u0020"`)},
		{lit: testutil.Esc(`"~u0063` + strings.Repeat("~u0063", 20) + `"`)},
		{lit: testutil.Esc(`"c~xde"`)},
		{lit: `"`},
		{lit: ``},
	}
	for _, tt := range tests {
		t.Run(tt.lit, func(t *testing.T) {
			assert.Equal(t, tt.want, keyIn([]byte(tt.lit), "code", "message"))
		})
	}
}

func TestReadersRejectMalformedStructure(t *testing.T) {
	// The readers only ever see documents that are valid JSON; on anything else
	// they must stop and report it rather than panic or misread.
	for _, s := range []string{`{"ab`, `{"a"`, `{"a":`, `{"a":1`, `{"a":1;`, `{"a":1 x}`, `{1:2}`, `{"a":"`, `{"a":1,}`, `{"a" 1}`} {
		t.Run("object "+s, func(t *testing.T) {
			r := readObject([]byte(s))
			require.True(t, r.ok)
			for {
				if _, _, more := r.next(); !more {
					break
				}
			}
			assert.False(t, r.ok)
		})
	}
	for _, s := range []string{`[1`, `[1;`, `[1 x]`, `["`, `[1,`, `[,]`} {
		t.Run("array "+s, func(t *testing.T) {
			r := readArray([]byte(s))
			require.True(t, r.ok)
			for {
				if _, more := r.next(); !more {
					break
				}
			}
			assert.False(t, r.ok)
		})
	}
	for _, s := range []string{``, ` `, `1`, `"x"`, `[]`} {
		assert.False(t, readObject([]byte(s)).ok, s)
	}
	for _, s := range []string{``, `1`, `{}`} {
		assert.False(t, readArray([]byte(s)).ok, s)
	}
}

func TestReadersReadEveryMember(t *testing.T) {
	r := readObject([]byte(` { "a" : 1 , "b":[1,{"c":2}],"d":{} } `))
	var keys, values []string
	for k, v, more := r.next(); more; k, v, more = r.next() {
		keys, values = append(keys, string(k)), append(values, string(v))
	}
	require.True(t, r.ok)
	assert.Equal(t, []string{`"a"`, `"b"`, `"d"`}, keys)
	assert.Equal(t, []string{`1`, `[1,{"c":2}]`, `{}`}, values)
	_, _, more := r.next()
	assert.False(t, more, "a finished reader stays finished")

	a := readArray([]byte(` [ 1 , "x", {"a":[]} ] `))
	var elems []string
	for v, ok := a.next(); ok; v, ok = a.next() {
		elems = append(elems, string(v))
	}
	require.True(t, a.ok)
	assert.Equal(t, []string{`1`, `"x"`, `{"a":[]}`}, elems)
	_, more = a.next()
	assert.False(t, more)
}

func TestUnquoteKeyRejectsInvalidEscapes(t *testing.T) {
	for _, s := range []string{testutil.Esc(`"~x"`), testutil.Esc(`"~u12"`), `"`, `x`} {
		_, ok := unquoteKey([]byte(s))
		assert.False(t, ok, s)
	}
	got, ok := unquoteKey([]byte(testutil.Esc(`"a~u00e9~n"`)))
	require.True(t, ok)
	assert.Equal(t, "aé\n", string(got))
	_, err := asText([]byte(testutil.Esc(`"~x"`)), field{key: "model"})
	require.ErrorIs(t, err, ErrMalformed)
}

func TestDecoderCapsKeyedObjects(t *testing.T) {
	var d decoder
	// n plus the margin members are read; one more is rejected unread.
	_, err := d.keyed([]byte(wideObject(4+unknownMargin, "")), field{key: "answers"}, 4)
	require.NoError(t, err)
	d.members = d.members[:0]
	_, err = d.keyed([]byte(wideObject(5+unknownMargin, "")), field{key: "answers"}, 4)
	require.ErrorIs(t, err, ErrMalformed)
	assert.Equal(t, "systemone: malformed response: answers: has too many members", err.Error())
	assert.Empty(t, d.members)
}

func TestDecoderFieldsKeepOnlyNamedMembers(t *testing.T) {
	var d decoder
	o, err := d.fields([]byte(wideObject(300, testutil.Esc(`,"model":"m","us~u0061ge":{}`))), field{key: responseKey}, "model", "usage", "answers")
	require.NoError(t, err)
	require.Len(t, o, 2)
	raw, ok := o.get("usage")
	require.True(t, ok)
	assert.Equal(t, `{}`, string(raw))
	assert.Len(t, d.members, 2)
}

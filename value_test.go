package discern_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/discerntest"
	"github.com/mrz1836/go-discern/internal/testutil"
)

func TestValueMarshalJSONScenarios(t *testing.T) {
	tests := []struct {
		name string
		v    discern.Value
		want string
	}{
		{"zero is null", discern.Value{}, `null`},
		{"text without HTML escaping", discern.Text(`refund <now> & "fast"`), `"refund <now> & \"fast\""`},
		{"text with newline", discern.Text("a\nb"), `"a\nb"`},
		{"raw JSON verbatim", discern.JSON([]byte(` {"id": 12345678901234567890, "b":[1, 2]} `)), ` {"id": 12345678901234567890, "b":[1, 2]} `},
		{"raw JSON keeps key order", discern.JSON([]byte(`{"z":1,"a":2}`)), `{"z":1,"a":2}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.v.MarshalJSON()
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestValueIsImmutable(t *testing.T) {
	raw := []byte(`{"ticket":1}`)
	v := discern.JSON(raw)
	raw[2] = 'X'
	got, err := v.MarshalJSON()
	require.NoError(t, err)
	assert.JSONEq(t, `{"ticket":1}`, string(got))

	got[2] = 'Y'
	again, err := v.MarshalJSON()
	require.NoError(t, err)
	assert.JSONEq(t, `{"ticket":1}`, string(again))
}

func TestValueIsZero(t *testing.T) {
	assert.True(t, discern.Value{}.IsZero())
	assert.False(t, discern.Text("").IsZero())
	assert.False(t, discern.JSON([]byte(`null`)).IsZero())
}

func TestMarshalScenarios(t *testing.T) {
	type ticket struct {
		ID      uint64 `json:"id"`
		Subject string `json:"subject"`
	}
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"large integer and HTML characters", ticket{ID: 12345678901234567890, Subject: "a <b> & c"}, `{"id":12345678901234567890,"subject":"a <b> & c"}`},
		{"array", []string{"a", "b"}, `["a","b"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := discern.Marshal(tt.in)
			require.NoError(t, err)
			got, err := v.MarshalJSON()
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got)) // exact bytes: no HTML escaping, no float64 round trip
		})
	}

	_, err := discern.Marshal(make(chan int))
	require.ErrorIs(t, err, discern.ErrInvalidRequest)
}

func TestStructuredStateIsAccepted(t *testing.T) {
	jev, _ := staticPair()
	ev := testutil.Evaluator(t, discern.WithAdapter(jev))
	tests := []struct {
		name  string
		state discern.Value
	}{
		{"object", discern.JSON([]byte(`{"ticket":{"id":12345678901234567890,"text":"charged twice"}}`))},
		{"array", discern.JSON([]byte(`["charged twice","refund please"]`))},
		{"JSON string", discern.JSON([]byte(`"charged twice"`))},
		{"object between spaces", discern.JSON([]byte(" \n{\"ticket\":\"charged twice\"} \t"))},
		{"text with surrounding spaces", discern.Text("  charged twice\n")},
		{"marshaled struct", mustMarshal(t, struct {
			ID uint64 `json:"id"`
		}{ID: 12345678901234567890})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := ticketRequest(discern.JevOnly)
			req.State = tt.state
			_, err := ev.Evaluate(t.Context(), req)
			require.NoError(t, err)
		})
	}
	assert.Equal(t, len(tests), jev.Calls())
}

func mustMarshal(t *testing.T, v any) discern.Value {
	t.Helper()
	out, err := discern.Marshal(v)
	require.NoError(t, err)
	return out
}

func TestValueIsNeverEncodedTwice(t *testing.T) {
	type envelope struct {
		State discern.Value `json:"state"`
	}
	tests := []struct {
		name string
		v    discern.Value
		want string
	}{
		{"raw JSON nests as JSON", discern.JSON([]byte(`{"id":12345678901234567890}`)), `{"state":{"id":12345678901234567890}}`},
		{"text nests as one JSON string", discern.Text(`say "hi" <b>`), `{"state":"say \"hi\" <b>"}`},
		{"marshaled value nests as JSON", mustMarshal(t, []int{1, 2}), `{"state":[1,2]}`},
		{"a Value marshaled again is unchanged", mustMarshal(t, discern.Text("charged & refunded")), `{"state":"charged & refunded"}`},
		{"zero value nests as null", discern.Value{}, `{"state":null}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := discern.Marshal(envelope{State: tt.v})
			require.NoError(t, err)
			got, err := v.MarshalJSON()
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestJSONStringBlankness(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		blank bool
	}{
		{"empty", `""`, true},
		{"spaces", `"   "`, true},
		{"leading space before JSON string", ` "  "`, true},
		{"escaped whitespace", `"\t\n\r \u000b\f"`, true},
		{"escaped whitespace between spaces", " \"\\n\" \t\r\n", true},
		{"escaped letter between spaces", " \"\\u0041\" \n", false},
		{"unicode spaces", "\"\u00a0\u2003\u3000\"", true},
		{"escaped unicode spaces", `"\u00a0\u2028\u2029\u0085"`, true},
		{"letter", `"a"`, false},
		{"letter after spaces", `"   a"`, false},
		{"escaped letter", `"\u0041"`, false},
		{"escaped quote", `"\""`, false},
		{"escaped slash", `"\/"`, false},
		{"letter after unicode space", "\"\u3000x\"", false},
		{"non-space symbol", "\"\u00b7\"", false},
		{"zero-width space is not white space", "\"\u200b\"", false},
		{"emoji", "\"🙂\"", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var decoded string
			require.NoError(t, json.Unmarshal([]byte(tt.raw), &decoded))
			require.Equal(t, tt.blank, strings.TrimSpace(decoded) == "", "fixture agrees with strings.TrimSpace")
			req := discern.Request{
				Providers: discern.JevOnly, State: discern.Text("ticket"),
				Questions: []discern.Question{discern.Noul(qOutage, discern.JSON([]byte(tt.raw)))},
			}
			jev := discerntest.Static(discern.Jev, discern.Observation{Answers: []discern.Answer{{
				QuestionID: qOutage, Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: 0.5},
			}}})
			ev := testutil.Evaluator(t, discern.WithAdapter(jev))
			_, err := ev.Evaluate(t.Context(), req)
			// The same literal as the state, with non-blank instructions.
			stateReq := req
			stateReq.State = discern.JSON([]byte(tt.raw))
			stateReq.Questions = []discern.Question{discern.Noul(qOutage, discern.Text("Is there an outage?"))}
			_, stateErr := ev.Evaluate(t.Context(), stateReq)
			if tt.blank {
				require.ErrorIs(t, err, discern.ErrInvalidRequest)
				assert.Contains(t, err.Error(), "questions[0].instructions: must not be empty")
				require.ErrorIs(t, stateErr, discern.ErrInvalidRequest)
				assert.Contains(t, stateErr.Error(), "state: must not be empty")
				assert.Equal(t, 0, jev.Calls())
				return
			}
			require.NoError(t, err)
			require.NoError(t, stateErr)
			assert.Equal(t, 2, jev.Calls())
		})
	}
}

// stdlibString encodes s like encoding/json with HTML escaping disabled.
func stdlibString(t testing.TB, s string) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	require.NoError(t, enc.Encode(s))
	return string(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
}

func TestValueTextEncodingMatchesEncodingJSON(t *testing.T) {
	tests := []string{
		"", "plain ticket", `quote " and backslash \`, "tab\tnewline\ncarriage\rbackspace\bformfeed\f",
		"\x00\x01\x1f\x7f", "line\u2028separator\u2029paragraph", "<html> & 'quotes'", "multi-byte: café, naïve, Ωμέγα, 🙂",
	}
	for _, s := range tests {
		got, err := discern.Text(s).MarshalJSON()
		require.NoError(t, err)
		assert.Equal(t, stdlibString(t, s), string(got), "input %q", s)
	}
}

func TestValueAppendJSON(t *testing.T) {
	prefix := []byte(`{"state":`)
	state := discern.JSON([]byte(`{"id":12345678901234567890}`))
	want, err := state.MarshalJSON()
	require.NoError(t, err)
	got, err := state.AppendJSON(append([]byte(nil), prefix...))
	require.NoError(t, err)
	assert.Equal(t, append(append([]byte(nil), prefix...), want...), got)

	got, err = discern.Text("a<b").AppendJSON([]byte("x="))
	require.NoError(t, err)
	assert.Equal(t, `x="a<b"`, string(got))

	got, err = discern.Value{}.AppendJSON(nil)
	require.NoError(t, err)
	assert.Equal(t, "null", string(got))

	invalid := []struct {
		name string
		v    discern.Value
	}{
		{"invalid JSON", discern.JSON([]byte(`{"a":`))},
		{"invalid UTF-8 JSON", discern.JSON([]byte("\"\xff\""))},
		{"invalid UTF-8 text", discern.Text("\xff")},
		{"empty JSON", discern.JSON(nil)},
		{"whitespace JSON", discern.JSON([]byte(" \n"))},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			out, err := tt.v.AppendJSON(prefix)
			require.ErrorIs(t, err, discern.ErrInvalidRequest)
			assert.Equal(t, prefix, out)
			_, err = tt.v.MarshalJSON()
			require.ErrorIs(t, err, discern.ErrInvalidRequest)
		})
	}
}

func TestValueAppendJSONDoesNotAllocateWithCapacity(t *testing.T) {
	testutil.SkipUnderRace(t)
	raw := discern.JSON(append(append([]byte("["), bytes.Repeat([]byte(`[1,2,3],`), 1000)...), `[]]`...))
	text := discern.Text(string(bytes.Repeat([]byte("charged twice "), 500)))
	escaped := discern.Text(strings.Repeat("line \"one\"\n\ttab\u2028", 300))
	buf := make([]byte, 0, 16<<10)
	tests := []struct {
		name string
		v    discern.Value
	}{{"raw JSON", raw}, {"text", text}, {"text with escapes", escaped}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := tt.v.AppendJSON(buf[:0])
			require.NoError(t, err)
			require.NotEmpty(t, out)
			require.Less(t, len(out), cap(buf))
			allocs := testing.AllocsPerRun(100, func() {
				_, _ = tt.v.AppendJSON(buf[:0])
			})
			assert.Zero(t, allocs)
		})
	}
}

func TestJSONValueOfLargeStringDoesNotDecodeIt(t *testing.T) {
	testutil.SkipUnderRace(t)
	raw := []byte(`"` + strings.Repeat("I was charged twice for invoice 1042. ", 1<<15) + `"`)
	_, allocated := testutil.AllocStats(10, func() { _ = discern.JSON(raw) })
	t.Logf("%.0f bytes allocated to build a Value from a %d-byte JSON string", allocated, len(raw))
	assert.Less(t, allocated, 1.25*float64(len(raw)), "the bytes are copied once and never decoded into a second copy")
}

func FuzzValueTextEncoding(f *testing.F) {
	for _, s := range []string{"", "ticket", "\u2028", "\x00\"\\", "Ωμέγα🙂", "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := discern.Text(s).MarshalJSON()
		if !utf8.ValidString(s) {
			require.ErrorIs(t, err, discern.ErrInvalidRequest)
			return
		}
		require.NoError(t, err)
		require.Equal(t, stdlibString(t, s), string(got))
		var back string
		require.NoError(t, json.Unmarshal(got, &back))
		require.Equal(t, s, back)
	})
}

func FuzzJSONStringBlankness(f *testing.F) {
	for _, s := range []string{"", " ", "\t", "\u00a0", "\u3000x", "x", " \u2028 ", "\u200b", "🙂", "\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			return
		}
		// Both the shortest encoding and one with every rune escaped must classify
		// like strings.TrimSpace on the decoded string.
		plain, err := json.Marshal(s)
		require.NoError(t, err)
		var escaped strings.Builder
		escaped.WriteByte('"')
		for _, r := range s {
			if r > 0xFFFF {
				escaped.WriteRune(r)
				continue
			}
			fmt.Fprintf(&escaped, "\\u%04x", r)
		}
		escaped.WriteByte('"')
		want := strings.TrimSpace(s) == ""
		for _, raw := range [][]byte{plain, []byte(escaped.String())} {
			req := discern.Request{
				Providers: discern.JevOnly, State: discern.Text("ticket"),
				Questions: []discern.Question{discern.Noul(qOutage, discern.JSON(raw))},
			}
			jev := discerntest.Static(discern.Jev, discern.Observation{Answers: []discern.Answer{{
				QuestionID: qOutage, Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: 0.5},
			}}})
			ev, err := discern.New(discern.WithAdapter(jev))
			require.NoError(t, err)
			_, err = ev.Evaluate(t.Context(), req)
			if want {
				require.ErrorIs(t, err, discern.ErrInvalidRequest, "%s", raw)
			} else {
				require.NoError(t, err, "%s", raw)
			}
		}
	})
}

func TestJSONValuesWithDuplicateKeysAreRejected(t *testing.T) {
	dup := discern.JSON([]byte(`{"ticket":{"id":1,"id":2}}`))
	escaped := discern.JSON([]byte(`{"a":1,"\u0061":2}`)) // the same key once unescaped
	raw, err := discern.Marshal(struct {
		Extra json.RawMessage `json:"extra"`
	}{Extra: json.RawMessage(`{"k":1,"k":2}`)})
	require.NoError(t, err)
	tests := []struct {
		name string
		edit func(r *discern.Request)
	}{
		{"nested in state", func(r *discern.Request) { r.State = dup }},
		{"escaped in state", func(r *discern.Request) { r.State = escaped }},
		{"marshaled raw message in state", func(r *discern.Request) { r.State = raw }},
		{"in instructions", func(r *discern.Request) { r.Questions[1].Instructions = dup }},
		{"in an option description", func(r *discern.Request) { r.Questions[0].Options[0].Description = dup }},
		{"in a level description", func(r *discern.Request) { r.Questions[2].Levels[0].Description = dup }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jev, clef := staticPair()
			ev := testutil.Evaluator(t, discern.WithAdapter(jev), discern.WithAdapter(clef))
			req := ticketRequest(discern.JevAndClef)
			tt.edit(&req)
			res, evalErr := ev.Evaluate(t.Context(), req)
			require.ErrorIs(t, evalErr, discern.ErrInvalidRequest)
			assert.Contains(t, evalErr.Error(), "duplicate object keys")
			assert.Nil(t, res)
			assert.Zero(t, jev.Calls()+clef.Calls())
		})
	}
	_, err = dup.MarshalJSON()
	require.ErrorIs(t, err, discern.ErrInvalidRequest)
	// Equal keys in different objects are fine, and the bytes pass through verbatim.
	distinct := []byte(`{"a":{"id":1},"b":{"id":2}}`)
	got, err := discern.JSON(distinct).MarshalJSON()
	require.NoError(t, err)
	assert.Equal(t, distinct, got)
}

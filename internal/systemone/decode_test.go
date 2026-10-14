package systemone_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/systemone"
	"github.com/mrz1836/go-discern/internal/testutil"
)

// validBody answers ticketRequest. Probability keys are deliberately in a
// different order from the options and levels.
const validBody = `{"model":"jev-1","answers":{` +
	`"queue":{"type":"choice","choice":"billing","probabilities":{"account":0.05,"billing":0.9,"technical":0.05},"confidence":0.8},` +
	`"urgent":{"type":"noul","noul":0.25},` +
	`"tone":{"type":"score","score":1.2,"legend":{"0":"Calm","1":"Annoyed","2":"Angry"},"probabilities":{"2":0.3,"0":0.1,"1":0.6},"confidence":0.4}` +
	`},"usage":{"input_tokens":120,"output_tokens":3}}`

const (
	secretSentinel = "SECRET-SENTINEL"

	// noulField is the urgent answer's value in validBody.
	noulField = `"noul":0.25`
)

// validObservation is the decoding of validBody.
func validObservation() *discern.Observation {
	return &discern.Observation{
		Model: "jev-1",
		Usage: &discern.Usage{InputTokens: new(int64(120)), OutputTokens: new(int64(3))},
		Answers: []discern.Answer{
			{
				QuestionID: "queue",
				Kind:       discern.KindChoice,
				Choice: &discern.ChoiceAnswer{
					Option:     "billing",
					Confidence: new(0.8),
					Probabilities: []discern.OptionProbability{
						{Option: "technical", Probability: 0.05},
						{Option: "billing", Probability: 0.9},
						{Option: "account", Probability: 0.05},
					},
				},
			},
			{QuestionID: "urgent", Kind: discern.KindNoul, Noul: &discern.NoulAnswer{Value: 0.25}},
			{
				QuestionID: "tone",
				Kind:       discern.KindScore,
				Score: &discern.ScoreAnswer{
					Value:      1.2,
					Confidence: new(0.4),
					Probabilities: []discern.LevelProbability{
						{Level: "calm", Probability: 0.1},
						{Level: "annoyed", Probability: 0.6},
						{Level: "angry", Probability: 0.3},
					},
				},
			},
		},
	}
}

// mutate returns validBody with the first occurrence of old replaced by repl.
func mutate(t *testing.T, old, repl string) string {
	t.Helper()
	require.Contains(t, validBody, old)
	return strings.Replace(validBody, old, repl, 1)
}

func TestDecodeResultValidBody(t *testing.T) {
	obs, err := systemone.DecodeResult([]byte(validBody), ticketRequest())
	require.NoError(t, err)
	assert.Equal(t, validObservation(), obs)
}

func TestDecodeResultMissingNullAndZero(t *testing.T) {
	tests := []struct {
		name  string
		old   string
		repl  string
		check func(t *testing.T, obs *discern.Observation)
	}{
		{
			name: "missing choice confidence is nil",
			old:  `,"confidence":0.8`, repl: ``,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Nil(t, obs.Answers[0].Choice.Confidence)
			},
		},
		{
			name: "null choice confidence is nil",
			old:  `"confidence":0.8`, repl: `"confidence":null`,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Nil(t, obs.Answers[0].Choice.Confidence)
			},
		},
		{
			name: "zero choice confidence is a pointer to zero",
			old:  `"confidence":0.8`, repl: `"confidence":0`,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Equal(t, new(0.0), obs.Answers[0].Choice.Confidence)
			},
		},
		{
			name: "missing score confidence is nil",
			old:  `,"confidence":0.4`, repl: ``,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Nil(t, obs.Answers[2].Score.Confidence)
			},
		},
		{
			name: "zero noul is zero",
			old:  noulField, repl: `"noul":0`,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Equal(t, &discern.NoulAnswer{Value: 0}, obs.Answers[1].Noul)
			},
		},
		{
			name: "missing model is empty",
			old:  `"model":"jev-1",`, repl: ``,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Empty(t, obs.Model)
			},
		},
		{
			name: "null model is empty",
			old:  `"model":"jev-1"`, repl: `"model":null`,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Empty(t, obs.Model)
			},
		},
		{
			name: "missing usage is nil",
			old:  `,"usage":{"input_tokens":120,"output_tokens":3}`, repl: ``,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Nil(t, obs.Usage)
			},
		},
		{
			name: "null usage is nil",
			old:  `"usage":{"input_tokens":120,"output_tokens":3}`, repl: `"usage":null`,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Nil(t, obs.Usage)
			},
		},
		{
			name: "null and missing token counts are nil",
			old:  `{"input_tokens":120,"output_tokens":3}`, repl: `{"input_tokens":null}`,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Equal(t, &discern.Usage{}, obs.Usage)
			},
		},
		{
			name: "zero token counts are pointers to zero",
			old:  `{"input_tokens":120,"output_tokens":3}`, repl: `{"input_tokens":0,"output_tokens":-0}`,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Equal(t, &discern.Usage{InputTokens: new(int64(0)), OutputTokens: new(int64(0))}, obs.Usage)
			},
		},
		{
			name: "null legend is ignored",
			old:  `"legend":{"0":"Calm","1":"Annoyed","2":"Angry"}`, repl: `"legend":null`,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Equal(t, validObservation().Answers[2], obs.Answers[2])
			},
		},
		{
			name: "missing legend is ignored",
			old:  `"legend":{"0":"Calm","1":"Annoyed","2":"Angry"},`, repl: ``,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Equal(t, validObservation().Answers[2], obs.Answers[2])
			},
		},
		{
			name: "legend with object and array values",
			old:  `{"0":"Calm","1":"Annoyed","2":"Angry"}`, repl: `{"0":{"label":"Calm","rank":0},"1":["Annoyed"],"2":null}`,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Equal(t, validObservation().Answers[2], obs.Answers[2])
			},
		},
		{
			name: "huge numbers in unknown fields",
			old:  `{"model":"jev-1",`, repl: `{"extra":1e400,"big":` + strings.Repeat("9", 500) + `,"model":"jev-1",`,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Equal(t, validObservation(), obs)
			},
		},
		{
			name: "unknown answer and usage fields",
			old:  noulField, repl: `"noul":0.25,"reasoning":{"steps":[1e999,-1e999]},"Noul":"x"`,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Equal(t, validObservation(), obs)
			},
		},
		{
			name: "trailing whitespace",
			old:  `"output_tokens":3}}`, repl: "\"output_tokens\":3}}\n\t ",
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Equal(t, validObservation(), obs)
			},
		},
		{
			name: "probability bounds are inclusive",
			old:  `{"account":0.05,"billing":0.9,"technical":0.05}`, repl: `{"account":0,"billing":1,"technical":0.0e0}`,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.Equal(t, []discern.OptionProbability{
					{Option: "technical", Probability: 0},
					{Option: "billing", Probability: 1},
					{Option: "account", Probability: 0},
				}, obs.Answers[0].Choice.Probabilities)
			},
		},
		{
			name: "score outside the level range is left to the discern package",
			old:  `"score":1.2`, repl: `"score":-3.5e1`,
			check: func(t *testing.T, obs *discern.Observation) {
				t.Helper()
				assert.InDelta(t, -35.0, obs.Answers[2].Score.Value, 1e-12)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs, err := systemone.DecodeResult([]byte(mutate(t, tt.old, tt.repl)), ticketRequest())
			require.NoError(t, err)
			require.NotNil(t, obs)
			require.Len(t, obs.Answers, 3)
			tt.check(t, obs)
		})
	}
}

func TestDecodeResultRejectsMalformedBodies(t *testing.T) {
	tests := []struct {
		name string
		old  string
		repl string
	}{
		// Required fields: missing, null, wrong type.
		{name: "missing noul", old: `"type":"noul","noul":0.25`, repl: `"type":"noul"`},
		{name: "null noul", old: noulField, repl: `"noul":null`},
		{name: "string noul", old: noulField, repl: `"noul":"0.25"`},
		{name: "boolean noul", old: noulField, repl: `"noul":true`},
		{name: "missing answers", old: `"answers":`, repl: `"replies":`},
		{name: "null answers", old: validBody, repl: `{"answers":null}`},
		{name: "array answers", old: validBody, repl: `{"answers":[]}`},
		{name: "answer not an object", old: `{"type":"noul","noul":0.25}`, repl: `0.25`},
		{name: "null answer", old: `{"type":"noul","noul":0.25}`, repl: `null`},
		{name: "missing type", old: `"type":"noul",`, repl: ``},
		{name: "null type", old: `"type":"noul"`, repl: `"type":null`},
		{name: "numeric type", old: `"type":"noul"`, repl: `"type":1`},
		{name: "unknown type", old: `"type":"noul"`, repl: `"type":"rank"`},
		{name: "missing choice", old: `"choice":"billing",`, repl: ``},
		{name: "null choice", old: `"choice":"billing"`, repl: `"choice":null`},
		{name: "missing choice probabilities", old: `"probabilities":{"account":0.05,"billing":0.9,"technical":0.05},`, repl: ``},
		{name: "null choice probabilities", old: `{"account":0.05,"billing":0.9,"technical":0.05}`, repl: `null`},
		{name: "array choice probabilities", old: `{"account":0.05,"billing":0.9,"technical":0.05}`, repl: `[0.05,0.9,0.05]`},
		{name: "missing score", old: `"score":1.2,`, repl: ``},
		{name: "null score", old: `"score":1.2`, repl: `"score":null`},
		{name: "string score", old: `"score":1.2`, repl: `"score":"1.2"`},
		{name: "missing score probabilities", old: `"probabilities":{"2":0.3,"0":0.1,"1":0.6},`, repl: ``},
		{name: "string confidence", old: `"confidence":0.8`, repl: `"confidence":"high"`},
		{name: "top-level array", old: validBody, repl: `[` + validBody + `]`},
		{name: "top-level null", old: validBody, repl: `null`},
		{name: "top-level string", old: validBody, repl: `"answers"`},
		{name: "top-level number", old: validBody, repl: `1`},
		{name: "empty input", old: validBody, repl: ``},
		{name: "whitespace input", old: validBody, repl: " \n"},
		{name: "truncated", old: validBody, repl: validBody[:len(validBody)/2]},
		{name: "invalid literal", old: noulField, repl: `"noul":0.25.1`},
		{name: "leading zero", old: noulField, repl: `"noul":00.25`},
		{name: "trailing comma", old: `"noul":0.25}`, repl: `"noul":0.25,}`},
		{name: "invalid UTF-8", old: `"model":"jev-1"`, repl: "\"model\":\"jev-\xff\""},
		{name: "model not a string", old: `"model":"jev-1"`, repl: `"model":1`},

		// Duplicate keys at any depth, including escaped spellings.
		{name: "duplicate top-level key", old: `{"model":"jev-1",`, repl: `{"model":"jev-1","model":"jev-1",`},
		{name: "duplicate answer", old: `"urgent":{"type":"noul","noul":0.25},`, repl: `"urgent":{"type":"noul","noul":0.25},"urgent":{"type":"noul","noul":0.25},`},
		{name: "duplicate probability", old: `"billing":0.9,`, repl: `"billing":0.9,"billing":0.9,`},
		{name: "duplicate probability via escape", old: `"billing":0.9,`, repl: `"billing":0.9,"bill\u0069ng":0.9,`},
		{name: "duplicate legend key", old: `"1":"Annoyed",`, repl: `"1":"Annoyed","1":"Annoyed",`},
		{name: "duplicate key in unknown nested field", old: `{"model":"jev-1",`, repl: `{"meta":{"a":[{"x":1,"x":2}]},"model":"jev-1",`},
		{name: "duplicate usage key", old: `"output_tokens":3`, repl: `"output_tokens":3,"output_tokens":3`},

		// Answer key domain.
		{name: "extra answer", old: `"urgent":`, repl: `"spam":{"type":"noul","noul":0.5},"urgent":`},
		{name: "missing answer", old: `"urgent":{"type":"noul","noul":0.25},`, repl: ``},
		{name: "answer key differs in case", old: `"urgent":`, repl: `"Urgent":`},

		// Exact, case-sensitive field names.
		{name: "case-variant choice", old: `"choice":"billing"`, repl: `"Choice":"billing"`},
		{name: "case-variant answers", old: `"answers":`, repl: `"Answers":`},
		{name: "case-variant type", old: `"type":"noul"`, repl: `"Type":"noul"`},
		{name: "case-variant noul", old: noulField, repl: `"NOUL":0.25`},
		{name: "case-variant probabilities", old: `"probabilities":{"2"`, repl: `"Probabilities":{"2"`},
		{name: "case-variant type value", old: `"type":"noul"`, repl: `"type":"Noul"`},

		// Trailing data.
		{name: "trailing object", old: validBody, repl: validBody + ` {}`},
		{name: "trailing garbage", old: validBody, repl: validBody + `x`},
		{name: "trailing value", old: validBody, repl: validBody + "\n1"},

		// Kind mismatch.
		{name: "noul answer for choice question", old: `"type":"choice"`, repl: `"type":"noul"`},
		{name: "score answer for noul question", old: `"type":"noul","noul":0.25`, repl: `"type":"score","noul":0.25`},
		{name: "choice answer for score question", old: `"type":"score"`, repl: `"type":"choice"`},

		// Ranges and finiteness.
		{name: "noul above one", old: noulField, repl: `"noul":1.5`},
		{name: "noul below zero", old: noulField, repl: `"noul":-0.1`},
		{name: "noul overflow", old: noulField, repl: `"noul":1e400`},
		{name: "probability above one", old: `"billing":0.9`, repl: `"billing":1.5`},
		{name: "probability below zero", old: `"billing":0.9`, repl: `"billing":-0.1`},
		{name: "probability overflow", old: `"billing":0.9`, repl: `"billing":1e400`},
		{name: "probability string", old: `"billing":0.9`, repl: `"billing":"0.9"`},
		{name: "probability null", old: `"billing":0.9`, repl: `"billing":null`},
		{name: "score probability above one", old: `"1":0.6`, repl: `"1":1.6`},
		{name: "score overflow", old: `"score":1.2`, repl: `"score":-1e400`},
		{name: "confidence above one", old: `"confidence":0.8`, repl: `"confidence":1.01`},
		{name: "confidence below zero", old: `"confidence":0.4`, repl: `"confidence":-0.01`},

		// Choice domain.
		{name: "choice not an option", old: `"choice":"billing"`, repl: `"choice":"sales"`},
		{name: "choice differs in case", old: `"choice":"billing"`, repl: `"choice":"Billing"`},
		{name: "choice probabilities missing an option", old: `"account":0.05,`, repl: ``},
		{name: "choice probabilities with extra option", old: `"account":0.05,`, repl: `"account":0.05,"sales":0,`},
		{name: "choice probabilities with renamed option", old: `"account":0.05,`, repl: `"Account":0.05,`},

		// Score domain.
		{name: "score probability key out of range", old: `"2":0.3,`, repl: `"3":0.3,`},
		{name: "score probability key padded", old: `"2":0.3,`, repl: `"02":0.3,`},
		{name: "score probability keyed by level ID", old: `"2":0.3,`, repl: `"angry":0.3,`},
		{name: "score probability missing a level", old: `"2":0.3,`, repl: ``},
		{name: "score probability extra level", old: `"2":0.3,`, repl: `"2":0.3,"3":0,`},
		{name: "legend wrong key", old: `"2":"Angry"`, repl: `"3":"Angry"`},
		{name: "legend missing key", old: `,"2":"Angry"`, repl: ``},
		{name: "legend extra key", old: `"2":"Angry"`, repl: `"2":"Angry","3":"Livid"`},
		{name: "legend array", old: `{"0":"Calm","1":"Annoyed","2":"Angry"}`, repl: `["Calm","Annoyed","Angry"]`},
		{name: "legend string", old: `{"0":"Calm","1":"Annoyed","2":"Angry"}`, repl: `"Calm"`},

		// Usage.
		{name: "usage negative", old: `"input_tokens":120`, repl: `"input_tokens":-1`},
		{name: "usage fractional", old: `"input_tokens":120`, repl: `"input_tokens":120.5`},
		{name: "usage fractional by exponent", old: `"input_tokens":120`, repl: `"input_tokens":12e-1`},
		{name: "usage negative fraction", old: `"input_tokens":120`, repl: `"input_tokens":-1.0`},
		{name: "usage overflow", old: `"output_tokens":3`, repl: `"output_tokens":9223372036854775808`},
		{name: "usage overflow spelled with a fraction", old: `"output_tokens":3`, repl: `"output_tokens":9223372036854775808.0`},
		{name: "usage overflow by exponent", old: `"output_tokens":3`, repl: `"output_tokens":1e19`},
		{name: "usage string", old: `"output_tokens":3`, repl: `"output_tokens":"3"`},
		{name: "usage not an object", old: `{"input_tokens":120,"output_tokens":3}`, repl: `[120,3]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs, err := systemone.DecodeResult([]byte(mutate(t, tt.old, tt.repl)), ticketRequest())
			require.ErrorIs(t, err, systemone.ErrMalformed)
			assert.Nil(t, obs)
		})
	}
}

func TestDecodeResultMissingAnswerNamesQuestion(t *testing.T) {
	_, err := systemone.DecodeResult([]byte(mutate(t, `"urgent":{"type":"noul","noul":0.25},`, ``)), ticketRequest())
	require.ErrorIs(t, err, systemone.ErrMalformed)
	assert.Contains(t, err.Error(), `answers["urgent"]`)
}

func TestDecodeResultNilRequest(t *testing.T) {
	obs, err := systemone.DecodeResult([]byte(validBody), nil)
	require.ErrorIs(t, err, systemone.ErrMalformed)
	assert.Nil(t, obs)
}

func TestDecodeResultErrorsNeverQuoteResponseContent(t *testing.T) {
	tests := []struct {
		name string
		old  string
		repl string
	}{
		{name: "unexpected answer key", old: `"urgent":`, repl: `"SECRET-SENTINEL-KEY":{"type":"noul","noul":0.5},"urgent":`},
		{name: "legend value with bad legend keys", old: `{"0":"Calm","1":"Annoyed","2":"Angry"}`, repl: `{"0":"SECRET-SENTINEL","1":"x","9":"y"}`},
		{name: "legend key", old: `"2":"Angry"`, repl: `"SECRET-SENTINEL":"Angry"`},
		{name: "unknown field with bad noul", old: noulField, repl: `"note":"SECRET-SENTINEL","noul":7`},
		{name: "choice value", old: `"choice":"billing"`, repl: `"choice":"SECRET-SENTINEL"`},
		{name: "type value", old: `"type":"noul"`, repl: `"type":"SECRET-SENTINEL"`},
		{name: "probability key", old: `"account":0.05`, repl: `"SECRET-SENTINEL":0.05`},
		{name: "duplicate key", old: `{"model":"jev-1",`, repl: `{"SECRET-SENTINEL":1,"SECRET-SENTINEL":2,"model":"jev-1",`},
		{name: "model wrong type", old: `"model":"jev-1"`, repl: `"model":["SECRET-SENTINEL"]`},
		{name: "number text", old: noulField, repl: `"noul":"SECRET-SENTINEL"`},
		{name: "syntax error after sentinel", old: `"model":"jev-1"`, repl: `"model":"SECRET-SENTINEL"SECRET-SENTINEL`},
		{name: "trailing sentinel", old: validBody, repl: validBody + `"SECRET-SENTINEL"`},
		{name: "huge probability", old: `"billing":0.9`, repl: `"billing":1e400`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := systemone.DecodeResult([]byte(mutate(t, tt.old, tt.repl)), ticketRequest())
			require.ErrorIs(t, err, systemone.ErrMalformed)
			assert.NotContains(t, err.Error(), secretSentinel)
			assert.NotContains(t, err.Error(), "1e400")
		})
	}
}

func TestDecodeResultEmptyQuestionSet(t *testing.T) {
	req := &discern.Request{Providers: discern.JevOnly, State: discern.Text("ticket")}
	obs, err := systemone.DecodeResult([]byte(`{"answers":{}}`), req)
	require.NoError(t, err)
	assert.Empty(t, obs.Answers)

	_, err = systemone.DecodeResult([]byte(`{"answers":{"urgent":{"type":"noul","noul":0.5}}}`), req)
	require.ErrorIs(t, err, systemone.ErrMalformed)
}

func TestDecodeResultClefStyleResult(t *testing.T) {
	q := discern.Choice("queue", discern.Text("Route it."),
		discern.Opt("account", discern.Text("a")),
		discern.Opt("billing", discern.Text("b")),
	)
	req := &discern.Request{Providers: discern.ClefOnly, State: discern.Text("ticket"), Questions: []discern.Question{q}}
	body := `{"model":"clef","answers":{"queue":{"type":"choice","choice":"account",` +
		`"probabilities":{"billing":0.25,"account":0.75}}}}`
	obs, err := systemone.DecodeResult([]byte(body), req)
	require.NoError(t, err)
	assert.Equal(t, &discern.Observation{
		Model: "clef",
		Answers: []discern.Answer{{
			QuestionID: "queue",
			Kind:       discern.KindChoice,
			Choice: &discern.ChoiceAnswer{
				Option: "account",
				Probabilities: []discern.OptionProbability{
					{Option: "account", Probability: 0.75},
					{Option: "billing", Probability: 0.25},
				},
			},
		}},
	}, obs)
}

func FuzzSystemOneDecode(f *testing.F) {
	f.Add([]byte(validBody))
	f.Add([]byte(`{"answers":{}}`))
	f.Add([]byte(`{"answers":{"urgent":{"type":"noul","noul":0.5}}}`))
	f.Add([]byte(validBody + ` {}`))
	f.Add([]byte(`{"model":"jev-1","model":"jev-1"}`))
	f.Add([]byte(`{"answers":{"queue":{"type":"choice","choice":"billing","probabilities":{"billing":1e400}}}}`))
	f.Add([]byte(`[{"answers":null}]`))
	f.Add([]byte(`{"extra":` + strings.Repeat("9", 400) + `}`))
	f.Add([]byte(strings.Repeat("[", 20000)))
	f.Add([]byte("\xff"))
	req := ticketRequest()
	f.Fuzz(func(t *testing.T, data []byte) {
		require.NotPanics(t, func() {
			obs, err := systemone.DecodeResult(data, req)
			if err != nil {
				require.ErrorIs(t, err, systemone.ErrMalformed)
				require.Nil(t, obs)
				return
			}
			require.Len(t, obs.Answers, len(req.Questions))
			for i, a := range obs.Answers {
				q := req.Questions[i]
				require.Equal(t, q.ID, a.QuestionID)
				require.Equal(t, q.Kind, a.Kind)
				switch q.Kind {
				case discern.KindChoice:
					require.Len(t, a.Choice.Probabilities, len(q.Options))
				case discern.KindScore:
					require.Len(t, a.Score.Probabilities, len(q.Levels))
				case discern.KindNoul:
					require.NotNil(t, a.Noul)
				}
			}
		})
	})
}

func TestDecodeResultManyOptions(t *testing.T) {
	req := choiceRequest(255)
	// Reverse the wire order of the probabilities: lookups must not depend on it.
	var b strings.Builder
	b.WriteString(`{"answers":{"queue":{"type":"choice","choice":"o254","probabilities":{`)
	for i := 254; i >= 0; i-- {
		p := 0
		if i == 254 {
			p = 1
		}
		fmt.Fprintf(&b, `"o%03d":%d`, i, p)
		if i > 0 {
			b.WriteByte(',')
		}
	}
	b.WriteString(`}}}}`)
	obs, err := systemone.DecodeResult([]byte(b.String()), req)
	require.NoError(t, err)
	require.Len(t, obs.Answers, 1)
	choice := obs.Answers[0].Choice
	require.NotNil(t, choice)
	assert.Equal(t, "o254", choice.Option)
	require.Len(t, choice.Probabilities, 255)
	for i, p := range choice.Probabilities {
		assert.Equal(t, req.Questions[0].Options[i].ID, p.Option)
	}
	assert.InDelta(t, 1.0, choice.Probabilities[254].Probability, 0)
	assert.Zero(t, choice.Probabilities[0].Probability)
}

func TestDecodeResultManyQuestions(t *testing.T) {
	const n = 40
	req := &discern.Request{Providers: discern.JevOnly, State: discern.Text("ticket")}
	answers := make([]string, n)
	for i := range n {
		id := fmt.Sprintf("q%02d", i)
		req.Questions = append(req.Questions, discern.Noul(id, discern.Text("Does the ticket mention item "+id+"?")))
		answers[n-1-i] = fmt.Sprintf(`%q:{"type":"noul","noul":0.%02d}`, id, i)
	}
	body := `{"answers":{` + strings.Join(answers, ",") + `}}`

	obs, err := systemone.DecodeResult([]byte(body), req)
	require.NoError(t, err)
	require.Len(t, obs.Answers, n)
	for i, a := range obs.Answers {
		assert.Equal(t, req.Questions[i].ID, a.QuestionID)
		assert.InDelta(t, float64(i)/100, a.Noul.Value, 1e-12)
	}

	t.Run("missing answer names the question", func(t *testing.T) {
		missing := strings.Replace(body, `"q17":{"type":"noul","noul":0.17},`, ``, 1)
		require.NotEqual(t, body, missing)
		_, err := systemone.DecodeResult([]byte(missing), req)
		require.ErrorIs(t, err, systemone.ErrMalformed)
		assert.Contains(t, err.Error(), `answers["q17"]: is missing`)
	})
	t.Run("extra answer", func(t *testing.T) {
		extra := strings.Replace(body, `{"answers":{`, `{"answers":{"spam":{"type":"noul","noul":0.5},`, 1)
		_, err := systemone.DecodeResult([]byte(extra), req)
		require.ErrorIs(t, err, systemone.ErrMalformed)
		assert.Contains(t, err.Error(), "answers: has keys that are not requested question IDs")
	})
	t.Run("duplicate answer spelled with an escape", func(t *testing.T) {
		dup := strings.Replace(body, `{"answers":{`, `{"answers":{"q\u0030\u0030":{"type":"noul","noul":0.5},`, 1)
		_, err := systemone.DecodeResult([]byte(dup), req)
		require.ErrorIs(t, err, systemone.ErrMalformed)
		assert.Contains(t, err.Error(), "duplicate key")
	})
}

func TestDecodeResultUnknownQuestionKind(t *testing.T) {
	// Requests reach the decoder validated, so every kind is known; an unknown
	// one still fails rather than decoding as some other kind.
	req := &discern.Request{Questions: []discern.Question{{ID: "urgent", Kind: "rank", Instructions: discern.Text("Rank it.")}}}
	for _, typ := range []string{"rank", "score"} {
		_, err := systemone.DecodeResult([]byte(`{"answers":{"urgent":{"type":"`+typ+`","score":1,"probabilities":{}}}}`), req)
		require.ErrorIs(t, err, systemone.ErrMalformed, typ)
	}
}

func TestDecodeResultRepeatedQuestionIDs(t *testing.T) {
	// Requests reach the decoder validated, so IDs are unique; the key check
	// still stays exact if one is repeated.
	q := discern.Noul("urgent", discern.Text("Is the customer blocked?"))
	req := &discern.Request{Providers: discern.JevOnly, State: discern.Text("ticket"), Questions: []discern.Question{q, q}}
	answer := `{"type":"noul","noul":0.25}`

	_, err := systemone.DecodeResult([]byte(`{"answers":{"urgent":`+answer+`,"spam":`+answer+`}}`), req)
	require.ErrorIs(t, err, systemone.ErrMalformed)

	obs, err := systemone.DecodeResult([]byte(`{"answers":{"urgent":`+answer+`}}`), req)
	require.NoError(t, err)
	assert.Len(t, obs.Answers, 2)
}

func TestDecodeResultEscapedStrings(t *testing.T) {
	// Escaped spellings of the model, a type, a choice and a key decode exactly as
	// encoding/json decodes them.
	body := mutate(t, `"model":"jev-1"`, `"model":"jev-1-\"beta\""`)
	for _, r := range [][2]string{
		{`"type":"noul"`, `"type":"n\u006ful"`},
		{`"choice":"billing"`, `"choice":"bill\u0069ng"`},
		{`"account":0.05`, `"\u0061ccount":0.05`},
	} {
		require.Contains(t, body, r[0])
		body = strings.Replace(body, r[0], r[1], 1)
	}
	obs, err := systemone.DecodeResult([]byte(body), ticketRequest())
	require.NoError(t, err)
	want := validObservation()
	want.Model = `jev-1-"beta"`
	assert.Equal(t, want, obs)
}

func TestDecodeResultReportedModel(t *testing.T) {
	tests := []struct {
		name  string
		model string // the JSON value of "model"
		want  string
		ok    bool
	}{
		{name: "versioned", model: `"jev-1.13.0"`, want: "jev-1.13.0", ok: true},
		{name: "visible punctuation", model: `"@cf/cloudflare/clef-flash+build.7~x"`, want: "@cf/cloudflare/clef-flash+build.7~x", ok: true},
		{name: "longest", model: `"` + strings.Repeat("m", 256) + `"`, want: strings.Repeat("m", 256), ok: true},
		{name: "escaped visible ASCII", model: testutil.Esc(`"jev~u002d1"`), want: "jev-1", ok: true},
		{name: "empty means not reported", model: `""`, ok: true},
		{name: "too long", model: `"` + strings.Repeat("m", 257) + `"`},
		{name: "space", model: `"jev 1"`},
		{name: "tab", model: testutil.Esc(`"jev~t1"`)},
		{name: "line feed", model: testutil.Esc(`"jev-1~n"`)},
		{name: "control character", model: testutil.Esc(`"jev~u00011"`)},
		{name: "delete", model: testutil.Esc(`"jev~u007f"`)},
		{name: "non-ASCII", model: `"jév-1"`},
		{name: "escaped non-ASCII", model: testutil.Esc(`"j~u00e9v"`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs, err := systemone.DecodeResult([]byte(mutate(t, `"model":"jev-1"`, `"model":`+tt.model)), ticketRequest())
			if !tt.ok {
				require.ErrorIs(t, err, systemone.ErrMalformed)
				assert.Equal(t, "systemone: malformed response: model: must be at most 256 bytes of visible ASCII", err.Error())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, obs.Model)
		})
	}
}

func TestDecodeResultTokenCountsByValue(t *testing.T) {
	tests := []struct {
		count string
		want  int64
	}{
		{count: `145`, want: 145},
		{count: `145.0`, want: 145},
		{count: `1.45e2`, want: 145},
		{count: `1450E-1`, want: 145},
		{count: `1e3`, want: 1000},
		{count: `0.0`, want: 0},
		{count: `-0.0`, want: 0},
		{count: `9.223372036854775807e18`, want: 9223372036854775807},
	}
	for _, tt := range tests {
		t.Run(tt.count, func(t *testing.T) {
			body := mutate(t, `"input_tokens":120,"output_tokens":3`, `"input_tokens":`+tt.count+`,"output_tokens":`+tt.count)
			obs, err := systemone.DecodeResult([]byte(body), ticketRequest())
			require.NoError(t, err)
			assert.Equal(t, &discern.Usage{InputTokens: new(tt.want), OutputTokens: new(tt.want)}, obs.Usage)
		})
	}
}

// wideMembers returns n members k0, k1 and so on, each followed by a comma.
func wideMembers(n int, value string) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, `"k%d":%s,`, i, value)
	}
	return b.String()
}

func TestDecodeResultBoundsKeyedObjects(t *testing.T) {
	// Objects keyed by question, option or level take the expected keys plus
	// 256 more before the rest is rejected unread; objects with named fields
	// skip unknown members, however many there are.
	tests := []struct {
		name   string
		old    string
		repl   string
		reason string // empty when the body decodes
	}{
		{name: "answers at the margin", old: `"answers":{`, repl: `"answers":{` + wideMembers(256, `{}`), reason: `answers: has keys that are not requested question IDs`},
		{name: "answers past the margin", old: `"answers":{`, repl: `"answers":{` + wideMembers(257, `{}`), reason: `answers: has too many members`},
		{name: "choice probabilities past the margin", old: `"probabilities":{"account"`, repl: `"probabilities":{` + wideMembers(257, `0`) + `"account"`, reason: `answers["queue"].probabilities: has too many members`},
		{name: "score probabilities past the margin", old: `"probabilities":{"2"`, repl: `"probabilities":{` + wideMembers(257, `0`) + `"2"`, reason: `answers["tone"].probabilities: has too many members`},
		{name: "legend past the margin", old: `"legend":{`, repl: `"legend":{` + wideMembers(257, `0`), reason: `answers["tone"].legend: has too many members`},
		{name: "many unknown top-level members", old: `{"model":`, repl: `{` + wideMembers(5000, `0`) + `"model":`},
		{name: "many unknown answer members", old: noulField, repl: wideMembers(5000, `[1]`) + noulField},
		{name: "many unknown usage members", old: `"input_tokens":120`, repl: wideMembers(5000, `"x"`) + `"input_tokens":120`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs, err := systemone.DecodeResult([]byte(mutate(t, tt.old, tt.repl)), ticketRequest())
			if tt.reason == "" {
				require.NoError(t, err)
				assert.Equal(t, validObservation(), obs)
				return
			}
			require.ErrorIs(t, err, systemone.ErrMalformed)
			assert.Equal(t, "systemone: malformed response: "+tt.reason, err.Error())
		})
	}
}

// minimalResult spells the shortest result DecodeResult accepts for req.
func minimalResult(req *discern.Request) string {
	var b strings.Builder
	b.WriteString(`{"answers":{`)
	for i, q := range req.Questions {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"%s":`, q.ID)
		switch q.Kind {
		case discern.KindNoul:
			b.WriteString(`{"type":"noul","noul":0}`)
		case discern.KindChoice:
			shortest := q.Options[0].ID
			entries := make([]string, len(q.Options))
			for j, o := range q.Options {
				if len(o.ID) < len(shortest) {
					shortest = o.ID
				}
				entries[j] = `"` + o.ID + `":0`
			}
			fmt.Fprintf(&b, `{"type":"choice","choice":"%s","probabilities":{%s}}`, shortest, strings.Join(entries, ","))
		case discern.KindScore:
			entries := make([]string, len(q.Levels))
			for j := range q.Levels {
				entries[j] = fmt.Sprintf(`"%d":0`, j)
			}
			fmt.Fprintf(&b, `{"type":"score","score":0,"probabilities":{%s}}`, strings.Join(entries, ","))
		}
	}
	b.WriteString(`}}`)
	return b.String()
}

func TestMinResultSizeIsTheShortestAcceptedResult(t *testing.T) {
	levels := make([]discern.ScoreLevel, 10)
	for i := range levels {
		levels[i] = discern.Level(fmt.Sprintf("level-%d", i), discern.Text("x"))
	}
	tests := []struct {
		name string
		req  *discern.Request
	}{
		{name: "no questions", req: &discern.Request{}},
		{name: "ticket", req: ticketRequest()},
		{name: "255 options", req: choiceRequest(255)},
		{name: "ten levels", req: &discern.Request{Questions: []discern.Question{discern.Score("tone", discern.Text("How?"), levels...)}}},
		{name: "options of different lengths", req: &discern.Request{Questions: []discern.Question{
			discern.Choice("q", discern.Text("Which?"), discern.Opt("a-much-longer-option", discern.Text("x")), discern.Opt("b", discern.Text("y"))),
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := minimalResult(tt.req)
			_, err := systemone.DecodeResult([]byte(body), tt.req)
			require.NoError(t, err, "the minimal result must decode")
			assert.Equal(t, int64(len(body)), systemone.MinResultSize(tt.req))
		})
	}
	t.Run("IDs that need escaping are undercounted", func(t *testing.T) {
		req := &discern.Request{Questions: []discern.Question{
			discern.Choice("q", discern.Text("Which?"), discern.Opt(`say "yes"`, discern.Text("x")), discern.Opt(`a\b`, discern.Text("y"))),
		}}
		body := `{"answers":{"q":{"type":"choice","choice":"a\\b","probabilities":{"say \"yes\"":0,"a\\b":0}}}}`
		_, err := systemone.DecodeResult([]byte(body), req)
		require.NoError(t, err)
		assert.Less(t, systemone.MinResultSize(req), int64(len(body)))
	})
	t.Run("nil request", func(t *testing.T) {
		assert.Zero(t, systemone.MinResultSize(nil))
	})
	t.Run("questions without options or levels", func(t *testing.T) {
		// The encoder rejects these; the bound still never goes below the
		// fixed parts of each answer.
		req := &discern.Request{Questions: []discern.Question{{ID: "c", Kind: discern.KindChoice}, {ID: "s", Kind: discern.KindScore}}}
		want := len(`{"answers":{"c":{"type":"choice","choice":"","probabilities":{}},"s":{"type":"score","score":0,"probabilities":{}}}}`)
		assert.Equal(t, int64(want), systemone.MinResultSize(req))
	})
}

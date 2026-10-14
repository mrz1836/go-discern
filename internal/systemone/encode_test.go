package systemone_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/systemone"
	"github.com/mrz1836/go-discern/internal/testutil"
)

const testModel = "jev-1"

// ticketRequest returns a three-question support-ticket request: a choice with
// options in non-alphabetical order, a noul with both ends described, and a
// three-level score.
func ticketRequest() *discern.Request {
	queue := discern.Choice("queue", discern.Text("Route the ticket to one queue."),
		discern.Opt("technical", discern.Text("Bugs, outages and error messages.")),
		discern.Opt("billing", discern.Text("Invoices, refunds and charges.")),
		discern.Opt("account", discern.Value{}),
	)
	urgent := discern.Noul("urgent", discern.Text("Is the customer blocked right now?"))
	urgent.Yes = discern.Text("The customer cannot work.")
	urgent.No = discern.Text("The customer can wait.")
	tone := discern.Score("tone", discern.Text("How frustrated is the customer?"),
		discern.Level("calm", discern.Text("Calm")),
		discern.Level("annoyed", discern.Text("Annoyed")),
		discern.Level("angry", discern.Text("Angry")),
	)
	return &discern.Request{
		Providers: discern.JevOnly,
		State:     discern.Text("Customer: I was charged twice for plan <pro> & need a refund."),
		Questions: []discern.Question{queue, urgent, tone},
	}
}

// assertWire compares wire bytes exactly. Key order, whitespace and escaping are
// part of the contract, so a semantic JSON comparison would hide regressions.
func assertWire(t *testing.T, want string, got []byte) {
	t.Helper()
	assert.Equal(t, want, string(got))
	assert.True(t, json.Valid(got))
}

func TestEncodeRequestExactWireBytes(t *testing.T) {
	const want = `{"model":"jev-1",` +
		`"state":"Customer: I was charged twice for plan <pro> & need a refund.",` +
		`"questions":{` +
		`"queue":{"type":"choice","instructions":"Route the ticket to one queue.",` +
		`"criteria":{"technical":"Bugs, outages and error messages.","billing":"Invoices, refunds and charges.","account":null}},` +
		`"urgent":{"type":"noul","instructions":"Is the customer blocked right now?",` +
		`"criteria":{"true":"The customer cannot work.","false":"The customer can wait."}},` +
		`"tone":{"type":"score","instructions":"How frustrated is the customer?",` +
		`"criteria":["Calm","Annoyed","Angry"]}}}`

	got, err := systemone.EncodeRequest(testModel, ticketRequest())
	require.NoError(t, err)
	assertWire(t, want, got)

	var compact bytes.Buffer
	require.NoError(t, json.Compact(&compact, got))
	assert.Equal(t, string(got), compact.String())
}

func TestEncodeRequestPreservesOptionOrder(t *testing.T) {
	tests := []struct {
		name    string
		options []string
		want    string
	}{
		{
			name:    "technical billing account",
			options: []string{"technical", "billing", "account"},
			want:    `"criteria":{"technical":"t","billing":"t","account":"t"}`,
		},
		{
			name:    "account technical billing",
			options: []string{"account", "technical", "billing"},
			want:    `"criteria":{"account":"t","technical":"t","billing":"t"}`,
		},
		{
			name:    "reverse alphabetical",
			options: []string{"technical", "billing", "account", "abuse"},
			want:    `"criteria":{"technical":"t","billing":"t","account":"t","abuse":"t"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := make([]discern.ChoiceOption, len(tt.options))
			for i, id := range tt.options {
				opts[i] = discern.Opt(id, discern.Text("t"))
			}
			req := &discern.Request{
				Providers: discern.JevOnly,
				State:     discern.Text("ticket"),
				Questions: []discern.Question{discern.Choice("queue", discern.Text("Route it."), opts...)},
			}
			got, err := systemone.EncodeRequest(testModel, req)
			require.NoError(t, err)
			assert.Contains(t, string(got), tt.want)
		})
	}
}

func TestEncodeRequestPreservesQuestionOrder(t *testing.T) {
	req := &discern.Request{
		Providers: discern.JevOnly,
		State:     discern.Text("ticket"),
		Questions: []discern.Question{
			discern.Noul("zeta", discern.Text("z")),
			discern.Noul("alpha", discern.Text("a")),
			discern.Noul("mid", discern.Text("m")),
		},
	}
	got, err := systemone.EncodeRequest(testModel, req)
	require.NoError(t, err)
	assertWire(t,
		`{"model":"jev-1","state":"ticket","questions":{`+
			`"zeta":{"type":"noul","instructions":"z"},`+
			`"alpha":{"type":"noul","instructions":"a"},`+
			`"mid":{"type":"noul","instructions":"m"}}}`,
		got)
}

func TestEncodeRequestNoulCriteria(t *testing.T) {
	tests := []struct {
		name string
		yes  discern.Value
		no   discern.Value
		want string
	}{
		{
			name: "both ends",
			yes:  discern.Text("blocked"),
			no:   discern.Text("can wait"),
			want: `{"type":"noul","instructions":"Blocked?","criteria":{"true":"blocked","false":"can wait"}}`,
		},
		{
			name: "yes only",
			yes:  discern.Text("blocked"),
			want: `{"type":"noul","instructions":"Blocked?","criteria":{"true":"blocked"}}`,
		},
		{
			name: "no only",
			no:   discern.JSON([]byte(`{"meaning":"can wait"}`)),
			want: `{"type":"noul","instructions":"Blocked?","criteria":{"false":{"meaning":"can wait"}}}`,
		},
		{
			name: "neither",
			want: `{"type":"noul","instructions":"Blocked?"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := discern.Noul("urgent", discern.Text("Blocked?"))
			q.Yes, q.No = tt.yes, tt.no
			req := &discern.Request{Providers: discern.JevOnly, State: discern.Text("ticket"), Questions: []discern.Question{q}}
			got, err := systemone.EncodeRequest(testModel, req)
			require.NoError(t, err)
			assertWire(t, `{"model":"jev-1","state":"ticket","questions":{"urgent":`+tt.want+`}}`, got)
		})
	}
}

func TestEncodeRequestStateEncoding(t *testing.T) {
	tests := []struct {
		name  string
		state discern.Value
		want  string
	}{
		{
			name:  "large integer preserved byte for byte",
			state: discern.JSON([]byte(`{"ticket":{"id":12345678901234567890,"queue":"billing"}}`)),
			want:  `"state":{"ticket":{"id":12345678901234567890,"queue":"billing"}},`,
		},
		{
			name:  "raw whitespace kept verbatim",
			state: discern.JSON([]byte("{ \"ticket\": [1, 2.50, 1e3] }")),
			want:  "\"state\":{ \"ticket\": [1, 2.50, 1e3] },",
		},
		{
			name:  "text html characters not escaped",
			state: discern.Text(`refund <now> & "soon"`),
			want:  `"state":"refund <now> & \"soon\"",`,
		},
		{
			name:  "json html characters not escaped",
			state: discern.JSON([]byte(`{"note":"a<b>&c"}`)),
			want:  `"state":{"note":"a<b>&c"},`,
		},
		{
			name:  "json array state",
			state: discern.JSON([]byte(`["billing","account"]`)),
			want:  `"state":["billing","account"],`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &discern.Request{
				Providers: discern.JevOnly,
				State:     tt.state,
				Questions: []discern.Question{discern.Noul("urgent", discern.Text("Blocked?"))},
			}
			got, err := systemone.EncodeRequest(testModel, req)
			require.NoError(t, err)
			assert.Contains(t, string(got), tt.want)
			assert.NotContains(t, string(got), `\u003c`)
			assert.NotContains(t, string(got), `\u0026`)
			assert.True(t, json.Valid(got))
		})
	}
}

func TestEncodeRequestJSONStateNotDoubleEncoded(t *testing.T) {
	req := &discern.Request{
		Providers: discern.JevOnly,
		State:     discern.JSON([]byte(`{"subject":"Refund","id":12345678901234567890}`)),
		Questions: []discern.Question{discern.Noul("urgent", discern.Text("Blocked?"))},
	}
	got, err := systemone.EncodeRequest(testModel, req)
	require.NoError(t, err)

	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(got, &body))
	assertWire(t, `{"subject":"Refund","id":12345678901234567890}`, body["state"])
	assert.Equal(t, byte('{'), body["state"][0])
}

func TestEncodeRequestValuesVerbatim(t *testing.T) {
	marshaled, err := discern.Marshal(map[string]any{"rule": "a<b"})
	require.NoError(t, err)
	q := discern.Choice("queue", discern.JSON([]byte(`["Route", "it"]`)),
		discern.Opt("technical", discern.JSON([]byte(`{"examples":["error 500"]}`))),
		discern.Opt("billing", marshaled),
	)
	s := discern.Score("tone", discern.JSON([]byte(`{"ask":"tone"}`)),
		discern.Level("calm", discern.JSON([]byte(`{"label":"calm"}`))),
		discern.Level("angry", discern.JSON([]byte(`["angry"]`))),
	)
	req := &discern.Request{Providers: discern.ClefOnly, State: discern.Text("ticket"), Questions: []discern.Question{q, s}}
	got, err := systemone.EncodeRequest("clef", req)
	require.NoError(t, err)
	assertWire(t,
		`{"model":"clef","state":"ticket","questions":{`+
			`"queue":{"type":"choice","instructions":["Route", "it"],"criteria":{"technical":{"examples":["error 500"]},"billing":{"rule":"a<b"}}},`+
			`"tone":{"type":"score","instructions":{"ask":"tone"},"criteria":[{"label":"calm"},["angry"]]}}}`,
		got)
}

func TestEncodeRequestStringKeysNotEscaped(t *testing.T) {
	q := discern.Choice("queue", discern.Text("Route it."),
		discern.Opt("tier<1>&up", discern.Text("t")),
		discern.Opt(`say "hi"`, discern.Text("t")),
	)
	req := &discern.Request{Providers: discern.JevOnly, State: discern.Text("ticket"), Questions: []discern.Question{q}}
	got, err := systemone.EncodeRequest("model<&>", req)
	require.NoError(t, err)
	s := string(got)
	assert.True(t, strings.HasPrefix(s, `{"model":"model<&>",`))
	assert.Contains(t, s, `"criteria":{"tier<1>&up":"t","say \"hi\"":"t"}`)
	assert.True(t, json.Valid(got))
}

func TestEncodeRequestRejectsInvalidRequests(t *testing.T) {
	valid := func() *discern.Request {
		return &discern.Request{
			Providers: discern.JevOnly,
			State:     discern.Text("ticket"),
			Questions: []discern.Question{discern.Choice("queue", discern.Text("Route it."),
				discern.Opt("billing", discern.Text("b")), discern.Opt("account", discern.Text("a")))},
		}
	}
	tests := []struct {
		name   string
		mutate func(r *discern.Request) *discern.Request
	}{
		{name: "nil request", mutate: func(*discern.Request) *discern.Request { return nil }},
		{name: "invalid state JSON", mutate: func(r *discern.Request) *discern.Request {
			r.State = discern.JSON([]byte(`{"ticket":`))
			return r
		}},
		{name: "invalid UTF-8 in raw state", mutate: func(r *discern.Request) *discern.Request {
			r.State = discern.JSON([]byte("\"\xff\""))
			return r
		}},
		{name: "unknown kind", mutate: func(r *discern.Request) *discern.Request {
			r.Questions[0].Kind = "rank"
			return r
		}},
		{name: "duplicate question ID", mutate: func(r *discern.Request) *discern.Request {
			r.Questions = append(r.Questions, r.Questions[0])
			return r
		}},
		{name: "empty question ID", mutate: func(r *discern.Request) *discern.Request {
			r.Questions[0].ID = ""
			return r
		}},
		{name: "duplicate option ID", mutate: func(r *discern.Request) *discern.Request {
			r.Questions[0].Options[1].ID = "billing"
			return r
		}},
		{name: "no options", mutate: func(r *discern.Request) *discern.Request {
			r.Questions[0].Options = nil
			return r
		}},
		{name: "invalid option description", mutate: func(r *discern.Request) *discern.Request {
			r.Questions[0].Options[0].Description = discern.JSON([]byte(`{bad}`))
			return r
		}},
		{name: "invalid instructions", mutate: func(r *discern.Request) *discern.Request {
			r.Questions[0].Instructions = discern.JSON([]byte(`[`))
			return r
		}},
		{name: "score without levels", mutate: func(r *discern.Request) *discern.Request {
			r.Questions[0] = discern.Score("tone", discern.Text("Tone?"))
			return r
		}},
		{name: "invalid level description", mutate: func(r *discern.Request) *discern.Request {
			r.Questions[0] = discern.Score("tone", discern.Text("Tone?"),
				discern.Level("calm", discern.Text("Calm")), discern.Level("angry", discern.JSON([]byte(`nope`))))
			return r
		}},
		{name: "invalid noul yes", mutate: func(r *discern.Request) *discern.Request {
			q := discern.Noul("urgent", discern.Text("Blocked?"))
			q.Yes = discern.JSON([]byte(`]`))
			r.Questions[0] = q
			return r
		}},
		{name: "invalid noul no", mutate: func(r *discern.Request) *discern.Request {
			q := discern.Noul("urgent", discern.Text("Blocked?"))
			q.Yes = discern.Text("blocked")
			q.No = discern.JSON([]byte(`{`))
			r.Questions[0] = q
			return r
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := systemone.EncodeRequest(testModel, tt.mutate(valid()))
			require.ErrorIs(t, err, discern.ErrInvalidRequest)
			assert.Nil(t, got)
			ve := testutil.ValidationError(t, err)
			assert.NotEmpty(t, ve.Field)
		})
	}
}

func TestEncodeRequestRejectsInvalidModel(t *testing.T) {
	got, err := systemone.EncodeRequest("jev-\xff", ticketRequest())
	assert.Nil(t, got)
	require.ErrorIs(t, err, discern.ErrInvalidRequest)
	ve := testutil.ValidationError(t, err)
	assert.Equal(t, "model", ve.Field)
}

func TestEncodeRequestReportsTheFirstFieldInBodyOrder(t *testing.T) {
	req := ticketRequest()
	req.State = discern.JSON([]byte(`{"ticket":`))
	req.Questions[0].Kind = "rank"
	_, err := systemone.EncodeRequest(testModel, req)
	ve := testutil.ValidationError(t, err)
	assert.Equal(t, "state", ve.Field)
}

func TestEncodeRequestManyOptions(t *testing.T) {
	req := choiceRequest(255)
	got, err := systemone.EncodeRequest(testModel, req)
	require.NoError(t, err)
	assert.True(t, json.Valid(got))
	assert.Contains(t, string(got), `"criteria":{"o000":"Route to queue 0","o001":"Route to queue 1",`)
	assert.True(t, strings.HasSuffix(string(got), `"o254":"Route to queue 254"}}}}`))

	t.Run("duplicate option ID", func(t *testing.T) {
		dup := choiceRequest(255)
		dup.Questions[0].Options[200].ID = "o017"
		_, err := systemone.EncodeRequest(testModel, dup)
		ve := testutil.ValidationError(t, err)
		assert.Equal(t, "questions[0].options[200].id", ve.Field)
		assert.Equal(t, "duplicate ID", ve.Reason)
	})
}

func TestEncodeRequestStateLargerThanTheReservedRoom(t *testing.T) {
	// A state too large for the body's initial capacity moves into place
	// behind the reserved room for the questions.
	state := largeState(64 << 10)
	req := ticketRequest()
	req.State = discern.JSON(state)
	got, err := systemone.EncodeRequest(testModel, req)
	require.NoError(t, err)
	small, err := systemone.EncodeRequest(testModel, ticketRequest())
	require.NoError(t, err)
	const smallState = `"Customer: I was charged twice for plan <pro> & need a refund."`
	want := strings.Replace(string(small), smallState, string(state), 1)
	assertWire(t, want, got)
}

func TestEncodeRequestLongEscapedModel(t *testing.T) {
	// Every control character escapes to six bytes, so the head outgrows the
	// room reserved for it; the body must still come out whole.
	model := strings.Repeat("\x01", 600)
	got, err := systemone.EncodeRequest(model, ticketRequest())
	require.NoError(t, err)
	var body struct {
		Model string `json:"model"`
	}
	require.NoError(t, json.Unmarshal(got, &body))
	assert.Equal(t, model, body.Model)
	assert.True(t, strings.HasSuffix(string(got), `"criteria":["Calm","Annoyed","Angry"]}}}`))
}

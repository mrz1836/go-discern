// Package systemone encodes System One requests and strictly decodes System One
// responses for the Jev and Clef adapters.
//
// The wire format is shared by both providers. Requests are written by hand so
// that the caller's question and option order survives on the wire (models can
// lean toward earlier options, and Go maps sort their keys) and so that raw JSON
// values are sent byte for byte. Responses are decoded strictly: duplicate keys,
// trailing data and answers that do not match the request are rejected rather
// than guessed at. Lookups are exact and case-sensitive, so a case-variant key
// such as "Answers" is not read as "answers": like any unknown field it is
// ignored, which fails the decode only when the field it resembles is required
// and missing.
package systemone

import (
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/mrz1836/go-discern"
)

// Encoding size hints, in bytes. They only reduce regrowth; any size works.
const (
	// headRoom covers `{"model":`, `,"state":` and a short model name.
	headRoom = 64

	// stateRoom is spare capacity for a small state, so that it is appended
	// without allocating.
	stateRoom = 512

	// questionRoom and memberRoom are the capacity reserved per question and per
	// option or level, and maxQuestionRoom caps the total reserved for the
	// questions up front.
	questionRoom    = 160
	memberRoom      = 48
	maxQuestionRoom = 64 << 10
)

// invalidValue is the reason for a Value that is not valid JSON and UTF-8.
const invalidValue = "must be valid JSON and valid UTF-8"

// EncodeRequest returns the exact System One request body:
//
//	{"model":<model>,"state":<state>,"questions":{<id>:{"type":<kind>,"instructions":<v>,"criteria":<c>}, ...}}
//
// Questions appear in request order, Choice criteria keys in option order and
// Score criteria in level order. Values are written verbatim from
// discern.Value.AppendJSON, and strings are encoded without HTML escaping. A Noul
// question has no criteria key unless Yes or No is set, and then only the set ends
// appear, "true" before "false".
//
// The request is expected to be validated already; EncodeRequest still rejects a
// nil request, a model or ID that is not valid UTF-8, an unknown kind, duplicate
// IDs and values that are not valid JSON, with an error matching
// discern.ErrInvalidRequest.
func EncodeRequest(model string, req *discern.Request) ([]byte, error) {
	if req == nil {
		return nil, invalidRequest("request", "is nil")
	}
	// The questions are encoded first, so that their size is known when the
	// body is allocated; see appendState.
	tail, qerr := appendQuestions(make([]byte, 0, questionsSizeHint(req.Questions)), req.Questions)
	body := make([]byte, 0, headRoom+len(model)+len(tail)+stateRoom)
	body = append(body, `{"model":`...)
	body, ok := appendText(body, model)
	if !ok {
		return nil, invalidRequest("model", "must be valid UTF-8")
	}
	body = append(body, `,"state":`...)
	// A state error takes precedence over a question error, as both are
	// reported in body order.
	body, err := appendState(body, tail, req.State)
	if err != nil {
		return nil, err
	}
	if qerr != nil {
		return nil, qerr
	}
	return body, nil
}

// appendState appends the state and then tail to body. When the state does not
// fit in body's spare capacity, appending it allocates the final body once,
// sized exactly for what is already written plus the state; reserving tail's
// room in front of the state first, then moving the state into place, keeps that
// one allocation large enough. A large state is thus allocated for and copied
// into the body once, never into a second, regrown body.
func appendState(body, tail []byte, state discern.Value) ([]byte, error) {
	body = slices.Grow(body, len(tail)) // a no-op unless a long, escaped model outgrew the estimate
	n := len(body)
	out, err := state.AppendJSON(body[:n+len(tail)])
	if err != nil {
		return nil, invalidRequest("state", invalidValue)
	}
	copy(out[n:], out[n+len(tail):])
	copy(out[len(out)-len(tail):], tail)
	return out, nil
}

// questionsSizeHint estimates the encoded size of the questions, so that their
// buffer rarely regrows.
func questionsSizeHint(qs []discern.Question) int {
	n := questionRoom
	for i := range qs {
		n += questionRoom + memberRoom*(len(qs[i].Options)+len(qs[i].Levels))
	}
	return min(n, maxQuestionRoom)
}

// appendQuestions appends `,"questions":{...}}`. Field paths for errors are
// formatted only when a question fails.
func appendQuestions(dst []byte, qs []discern.Question) ([]byte, error) {
	dst = append(dst, `,"questions":{`...)
	seen := make(map[string]struct{}, len(qs))
	for i := range qs {
		q := &qs[i]
		if reason := checkKey(q.ID, seen); reason != "" {
			return nil, invalidRequest(fmt.Sprintf("questions[%d].id", i), reason)
		}
		if i > 0 {
			dst = append(dst, ',')
		}
		var err error
		if dst, err = appendQuestion(dst, q, i); err != nil {
			return nil, err
		}
	}
	return append(dst, `}}`...), nil
}

func appendQuestion(dst []byte, q *discern.Question, i int) ([]byte, error) {
	switch q.Kind {
	case discern.KindChoice, discern.KindNoul, discern.KindScore:
	default:
		return nil, invalidRequest(fmt.Sprintf("questions[%d].kind", i), "must be choice, noul or score")
	}
	dst, _ = appendText(dst, q.ID) // checkKey already required valid UTF-8
	// The kind is one of three plain ASCII names, so it needs no escaping.
	dst = append(dst, `:{"type":"`...)
	dst = append(dst, q.Kind...)
	dst = append(dst, `","instructions":`...)
	dst, ok := appendValue(dst, q.Instructions)
	if !ok {
		return nil, invalidRequest(fmt.Sprintf("questions[%d].instructions", i), invalidValue)
	}
	var err error
	switch q.Kind {
	case discern.KindChoice:
		dst, err = appendChoiceCriteria(dst, q, i)
	case discern.KindNoul:
		dst, err = appendNoulCriteria(dst, q, i)
	case discern.KindScore:
		dst, err = appendScoreCriteria(dst, q, i)
	}
	if err != nil {
		return nil, err
	}
	return append(dst, '}'), nil
}

// appendChoiceCriteria writes an object keyed by option ID in option order. A
// zero description is written as null.
func appendChoiceCriteria(dst []byte, q *discern.Question, i int) ([]byte, error) {
	if len(q.Options) == 0 {
		return nil, invalidRequest(fmt.Sprintf("questions[%d].options", i), "must not be empty")
	}
	dst = append(dst, `,"criteria":{`...)
	seen := make(map[string]struct{}, len(q.Options))
	for j := range q.Options {
		o := &q.Options[j]
		if reason := checkKey(o.ID, seen); reason != "" {
			return nil, invalidRequest(fmt.Sprintf("questions[%d].options[%d].id", i, j), reason)
		}
		if j > 0 {
			dst = append(dst, ',')
		}
		dst, _ = appendText(dst, o.ID) // checkKey already required valid UTF-8
		dst = append(dst, ':')
		var ok bool
		if dst, ok = appendValue(dst, o.Description); !ok {
			return nil, invalidRequest(fmt.Sprintf("questions[%d].options[%d].description", i, j), invalidValue)
		}
	}
	return append(dst, '}'), nil
}

// appendNoulCriteria writes {"true":<yes>,"false":<no>} with only the set ends,
// or nothing at all when neither is set.
func appendNoulCriteria(dst []byte, q *discern.Question, i int) ([]byte, error) {
	if q.Yes.IsZero() && q.No.IsZero() {
		return dst, nil
	}
	dst = append(dst, `,"criteria":{`...)
	var ok bool
	if !q.Yes.IsZero() {
		dst = append(dst, `"true":`...)
		if dst, ok = appendValue(dst, q.Yes); !ok {
			return nil, invalidRequest(fmt.Sprintf("questions[%d].yes", i), invalidValue)
		}
	}
	if !q.No.IsZero() {
		if !q.Yes.IsZero() {
			dst = append(dst, ',')
		}
		dst = append(dst, `"false":`...)
		if dst, ok = appendValue(dst, q.No); !ok {
			return nil, invalidRequest(fmt.Sprintf("questions[%d].no", i), invalidValue)
		}
	}
	return append(dst, '}'), nil
}

// appendScoreCriteria writes the level descriptions as an array, lowest first.
// Level IDs stay local: the provider sees only positions.
func appendScoreCriteria(dst []byte, q *discern.Question, i int) ([]byte, error) {
	if len(q.Levels) == 0 {
		return nil, invalidRequest(fmt.Sprintf("questions[%d].levels", i), "must not be empty")
	}
	dst = append(dst, `,"criteria":[`...)
	for j := range q.Levels {
		if j > 0 {
			dst = append(dst, ',')
		}
		var ok bool
		if dst, ok = appendValue(dst, q.Levels[j].Description); !ok {
			return nil, invalidRequest(fmt.Sprintf("questions[%d].levels[%d].description", i, j), invalidValue)
		}
	}
	return append(dst, ']'), nil
}

// appendText appends s as a JSON string, escaped exactly as encoding/json
// escapes it with HTML escaping off. It reports false, leaving dst unchanged,
// when s is not valid UTF-8.
func appendText(dst []byte, s string) ([]byte, bool) {
	return appendValue(dst, discern.Text(s))
}

// appendValue appends v's encoding verbatim. A Value checks its JSON and UTF-8
// validity once when it is built, so the bytes are not rescanned here; an
// invalid Value reports false and leaves dst unchanged.
func appendValue(dst []byte, v discern.Value) ([]byte, bool) {
	out, err := v.AppendJSON(dst)
	return out, err == nil
}

// checkKey returns why id cannot be a unique JSON object key, or "" when it can,
// and records it in seen.
func checkKey(id string, seen map[string]struct{}) string {
	if id == "" || !utf8.ValidString(id) {
		return "must be a non-empty valid UTF-8 string"
	}
	if _, dup := seen[id]; dup {
		return "duplicate ID"
	}
	seen[id] = struct{}{}
	return ""
}

func invalidRequest(field, reason string) error {
	return &discern.ValidationError{Err: discern.ErrInvalidRequest, Field: field, Reason: reason}
}

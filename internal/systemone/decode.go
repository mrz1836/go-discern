package systemone

import (
	"encoding/json"
	"strconv"

	"github.com/mrz1836/go-discern"
	"github.com/mrz1836/go-discern/internal/ident"
)

// Members kept from objects whose members are named: the most the decoder
// reads from a result, from its usage, and from any one answer (a Score
// answer's type, score, probabilities, legend and confidence).
const (
	resultFields = 3
	usageFields  = 2
	answerFields = 5
)

// The shortest spelling of each part of a result, for MinResultSize: no
// whitespace, no escapes and one-digit numbers.
const (
	minResult    = len(`{"answers":{}}`)
	minAnswerKey = len(`"":`)
	minNoul      = len(`{"type":"noul","noul":0}`)
	minChoice    = len(`{"type":"choice","choice":"","probabilities":{}}`)
	minScore     = len(`{"type":"score","score":0,"probabilities":{}}`)
	minEntry     = len(`"":0`)
)

// MinResultSize returns a lower bound, in bytes, on the size of any result that
// DecodeResult accepts for req: every member it requires spelled as briefly as
// JSON allows, without whitespace or escapes, with one-digit numbers, and with
// the shortest option ID as the choice. An ID that needs escaping is counted as
// if it did not, so the bound never exceeds the true minimum. A Clef envelope,
// which wraps the result, is larger still. A nil req has a bound of 0.
//
// Adapters compare it with their response size limit before any call: a
// request whose every possible answer exceeds the limit would be billed and
// then fail as too large.
func MinResultSize(req *discern.Request) int64 {
	if req == nil {
		return 0
	}
	n := int64(minResult)
	for i := range req.Questions {
		q := &req.Questions[i]
		if i > 0 {
			n++ // the comma before the answer
		}
		n += int64(minAnswerKey + len(q.ID))
		switch q.Kind {
		case discern.KindNoul:
			n += int64(minNoul)
		case discern.KindChoice:
			n += int64(minChoice) + minEntries(len(q.Options), func(j int) int { return len(q.Options[j].ID) })
			if len(q.Options) > 0 {
				shortest := len(q.Options[0].ID)
				for j := range q.Options {
					shortest = min(shortest, len(q.Options[j].ID))
				}
				n += int64(shortest)
			}
		case discern.KindScore:
			n += int64(minScore) + minEntries(len(q.Levels), func(j int) int { return len(strconv.Itoa(j)) })
		}
	}
	return n
}

// minEntries returns the shortest spelling of count probability entries, the
// j-th keyed by keyLen(j) bytes: `"<key>":0`, separated by commas.
func minEntries(count int, keyLen func(j int) int) int64 {
	if count == 0 {
		return 0
	}
	n := int64(count - 1)
	for j := range count {
		n += int64(minEntry + keyLen(j))
	}
	return n
}

// DecodeResult strictly decodes a System One response object (Jev's whole body or
// Clef's envelope "result") into an Observation. Answers are in request question
// order, Choice probabilities in option order and Score probabilities in level
// order. Model and Usage are set when the response reports them; RequestID and
// RayID are left for the adapter.
//
// The decoder checks structure, presence, JSON types, key domains, finiteness and
// [0,1] ranges. A reported model must be at most 256 bytes of visible ASCII
// (0x21 to 0x7E); an empty, null or absent model means none was reported. Token
// counts must be non-negative integers by value, so 145.0 and 1.45e2 count as
// 145. Probability sums, argmax and score consistency are checked by the discern
// package, which validates every observation. Unknown fields are ignored without
// being stored, and an object keyed by question, option or level that has more
// than 256 members beyond the expected keys is rejected unread, so the memory a
// decode uses stays proportional to the request, not to the response. Every
// error wraps ErrMalformed.
func DecodeResult(data []byte, req *discern.Request) (*discern.Observation, error) {
	if req == nil {
		return nil, malformed(field{key: "request"}, "is nil")
	}
	if err := checkStrict(data); err != nil {
		return nil, err
	}
	return decodeResult(data, req)
}

// decodeResult decodes data, which must have passed checkStrict, for req, which
// must not be nil.
func decodeResult(data []byte, req *discern.Request) (*discern.Observation, error) {
	d := newDecoder(data, req)
	top, err := d.fields(data, field{key: responseKey}, "model", "usage", "answers")
	if err != nil {
		return nil, err
	}
	obs := &discern.Observation{}
	if obs.Model, err = reportedModel(top); err != nil {
		return nil, err
	}
	if obs.Usage, err = d.usage(top); err != nil {
		return nil, err
	}
	answers, err := d.requiredKeyed(top, field{key: "answers"}, len(req.Questions))
	if err != nil {
		return nil, err
	}
	if err = checkAnswerKeys(answers, req.Questions); err != nil {
		return nil, err
	}
	obs.Answers = make([]discern.Answer, len(req.Questions))
	// An answer's members are not needed once it is decoded, so each answer
	// reuses the storage of the one before.
	mark := len(d.members)
	for i := range req.Questions {
		q := &req.Questions[i]
		raw, _ := answers.get(q.ID)
		if obs.Answers[i], err = d.answer(q, raw); err != nil {
			return nil, err
		}
		d.members = d.members[:mark]
	}
	return obs, nil
}

// reportedModel returns the model the response reports, or "" when it reports
// none.
func reportedModel(top object) (string, error) {
	f := field{key: "model"}
	model, err := top.optionalString(f)
	if err != nil {
		return "", err
	}
	if !ident.Visible(model, ident.MaxProviderID) {
		return "", malformed(f, "must be at most 256 bytes of visible ASCII")
	}
	return model, nil
}

// usage returns the reported token counts, or nil when usage is absent or null.
// Each count may itself be absent or null.
func (d *decoder) usage(top object) (*discern.Usage, error) {
	f := field{key: "usage"}
	raw, ok := top.lookup(f)
	if !ok {
		return nil, nil //nolint:nilnil // absent and null both mean "not reported"
	}
	u, err := d.fields(raw, f, "input_tokens", "output_tokens")
	if err != nil {
		return nil, err
	}
	usage := &discern.Usage{}
	if usage.InputTokens, err = u.optionalCount(f.member("input_tokens")); err != nil {
		return nil, err
	}
	if usage.OutputTokens, err = u.optionalCount(f.member("output_tokens")); err != nil {
		return nil, err
	}
	return usage, nil
}

// checkAnswerKeys requires the answer keys to be exactly the requested question
// IDs. The message never names an unexpected key: it comes from the response.
func checkAnswerKeys(answers object, qs []discern.Question) error {
	missing, exact := answers.matches(len(qs), func(i int) string { return qs[i].ID })
	switch {
	case missing >= 0:
		return malformed(field{answer: qs[missing].ID}, "is missing")
	case !exact:
		return malformed(field{key: "answers"}, "has keys that are not requested question IDs")
	}
	return nil
}

func (d *decoder) answer(q *discern.Question, raw json.RawMessage) (discern.Answer, error) {
	at := field{answer: q.ID}
	a := discern.Answer{QuestionID: q.ID, Kind: q.Kind}
	var (
		obj object
		err error
	)
	switch q.Kind {
	case discern.KindChoice:
		obj, err = d.fields(raw, at, "type", "choice", "probabilities", "confidence")
	case discern.KindNoul:
		obj, err = d.fields(raw, at, "type", "noul")
	case discern.KindScore:
		obj, err = d.fields(raw, at, "type", "score", "probabilities", "legend", "confidence")
	default:
		// An unknown kind, which a validated request never has: its type
		// cannot match, so nothing else is read.
		obj, err = d.fields(raw, at, "type")
	}
	if err != nil {
		return a, err
	}
	tf := at.member("type")
	typ, err := obj.requiredText(tf)
	if err != nil {
		return a, err
	}
	switch {
	case string(typ) != string(discern.KindChoice) && string(typ) != string(discern.KindNoul) &&
		string(typ) != string(discern.KindScore):
		return a, malformed(tf, "must be choice, noul or score")
	case string(typ) != string(q.Kind):
		return a, malformed(tf, "does not match the question kind")
	}
	switch q.Kind {
	case discern.KindChoice:
		a.Choice, err = d.choice(q, obj, at)
	case discern.KindNoul:
		a.Noul, err = decodeNoul(obj, at)
	case discern.KindScore:
		a.Score, err = d.score(q, obj, at)
	}
	return a, err
}

func decodeNoul(obj object, at field) (*discern.NoulAnswer, error) {
	v, err := obj.requiredUnit(at.member("noul"))
	if err != nil {
		return nil, err
	}
	return &discern.NoulAnswer{Value: v}, nil
}

func (d *decoder) choice(q *discern.Question, obj object, at field) (*discern.ChoiceAnswer, error) {
	cf := at.member("choice")
	text, err := obj.requiredText(cf)
	if err != nil {
		return nil, err
	}
	// The answer reuses the request's option ID, which the response spells
	// identically, instead of allocating the response's copy.
	option := -1
	for i := range q.Options {
		if string(text) == q.Options[i].ID {
			option = i
			break
		}
	}
	if option < 0 {
		return nil, malformed(cf, "is not one of the options")
	}
	pf := at.member("probabilities")
	probs, err := d.requiredKeyed(obj, pf, len(q.Options))
	if err != nil {
		return nil, err
	}
	if _, exact := probs.matches(len(q.Options), func(i int) string { return q.Options[i].ID }); !exact {
		return nil, malformed(pf, "keys do not match the options")
	}
	c := &discern.ChoiceAnswer{Option: q.Options[option].ID, Probabilities: make([]discern.OptionProbability, len(q.Options))}
	for i := range q.Options {
		id := q.Options[i].ID
		raw, _ := probs.get(id)
		v, uerr := asUnit(raw, pf)
		if uerr != nil {
			return nil, uerr
		}
		c.Probabilities[i] = discern.OptionProbability{Option: id, Probability: v}
	}
	if c.Confidence, err = obj.optionalUnit(at.member("confidence")); err != nil {
		return nil, err
	}
	return c, nil
}

func (d *decoder) score(q *discern.Question, obj object, at field) (*discern.ScoreAnswer, error) {
	sf := at.member("score")
	raw, err := obj.required(sf)
	if err != nil {
		return nil, err
	}
	score, err := asFinite(raw, sf)
	if err != nil {
		return nil, err
	}
	// Level positions are the wire keys "0" through n-1. strconv.Itoa returns
	// them without allocating, as n is at most discern.MaxScoreLevels.
	n := len(q.Levels)
	pf := at.member("probabilities")
	probs, err := d.requiredKeyed(obj, pf, n)
	if err != nil {
		return nil, err
	}
	if _, exact := probs.matches(n, strconv.Itoa); !exact {
		return nil, malformed(pf, "keys do not match the level positions")
	}
	s := &discern.ScoreAnswer{Value: score, Probabilities: make([]discern.LevelProbability, n)}
	for i := range n {
		praw, _ := probs.get(strconv.Itoa(i))
		v, uerr := asUnit(praw, pf)
		if uerr != nil {
			return nil, uerr
		}
		s.Probabilities[i] = discern.LevelProbability{Level: q.Levels[i].ID, Probability: v}
	}
	lf := at.member("legend")
	if lraw, ok := obj.lookup(lf); ok {
		legend, lerr := d.keyed(lraw, lf, n)
		if lerr != nil {
			return nil, lerr
		}
		if _, exact := legend.matches(n, strconv.Itoa); !exact {
			return nil, malformed(lf, "keys do not match the level positions")
		}
	}
	if s.Confidence, err = obj.optionalUnit(at.member("confidence")); err != nil {
		return nil, err
	}
	return s, nil
}

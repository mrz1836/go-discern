package discern

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// Probability rounding is undocumented by both providers, so the precision of
// each distribution is inferred from its numbers: d is the largest count of
// fractional digits in the shortest decimal form of any value, clamped to
// [minPrecisionDigits, maxPrecisionDigits], and eps = 10^-d is one grid unit,
// which covers both rounding and truncation. The constants are provisional.
const (
	minPrecisionDigits = 2
	maxPrecisionDigits = 6

	// floatSlack absorbs binary floating-point error in sums and differences.
	floatSlack = 1e-9
)

// errAnswers is the cause of an observation whose answers fail validation, and
// errProviderID that of one whose model, request ID or ray ID is not a provider
// identifier.
var (
	errAnswers    = errors.New("answers do not match the request")
	errProviderID = errors.New("invalid provider identifier")
)

// normalizeObservation validates obs against the questions and returns a deep
// copy with answers in question order and probabilities in option or level
// order. Adapters and fakes may answer in any order; the arranged answers must
// then pass checkObservation, the rule set that stored records also meet.
func normalizeObservation(qs []QuestionInfo, obs *Observation) (*Observation, error) {
	if obs == nil {
		return nil, answerErr("no observation")
	}
	if err := checkProviderIDs(obs); err != nil {
		return nil, err
	}
	answers, err := arrangeAnswers(qs, obs.Answers)
	if err != nil {
		return nil, err
	}
	if err := checkAnswers(qs, obs.Usage, answers); err != nil {
		return nil, err
	}
	return &Observation{
		Model: obs.Model, RequestID: obs.RequestID, RayID: obs.RayID,
		Usage: obs.Usage.clone(), Answers: cloneAnswers(answers),
	}, nil
}

// checkObservation validates a stored observation against the questions without
// copying it. Its answers must already be in question order and its probabilities
// in option or level order, as normalizeObservation returns them.
func checkObservation(qs []QuestionInfo, obs *Observation) error {
	if obs == nil {
		return answerErr("no observation")
	}
	if err := checkProviderIDs(obs); err != nil {
		return err
	}
	return checkAnswers(qs, obs.Usage, obs.Answers)
}

// checkProviderIDs checks that the model, request ID and ray ID the provider
// reported are provider identifiers (see validProviderID).
func checkProviderIDs(obs *Observation) error {
	if field := providerIDsProblem(obs); field != "" {
		return fmt.Errorf("%w: %s %s", errProviderID, field, providerIDReason)
	}
	return nil
}

// providerIDsProblem returns the JSON name of the first of obs's model, request
// ID and ray ID that is not a provider identifier, or "".
func providerIDsProblem(obs *Observation) string {
	switch {
	case !validProviderID(obs.Model):
		return "model"
	case !validProviderID(obs.RequestID):
		return "request_id"
	case !validProviderID(obs.RayID):
		return "ray_id"
	}
	return ""
}

func checkAnswers(qs []QuestionInfo, usage *Usage, answers []Answer) error {
	if err := validateUsage(usage); err != nil {
		return err
	}
	if len(answers) != len(qs) {
		return answerErr(fmt.Sprintf("got %d answers for %d questions", len(answers), len(qs)))
	}
	for i := range qs {
		if err := checkAnswer(&qs[i], &answers[i]); err != nil {
			return fmt.Errorf("questions[%d]: %w", i, err)
		}
	}
	return nil
}

// arrangeAnswers returns the answers in question order with Choice and Score
// probabilities in option and level order. Answers that are already arranged, as
// the jev and clef adapters return them, are returned as they are; otherwise the
// result is a new slice whose rearranged typed answers are fresh shallow copies,
// so the caller's observation is never modified. Repeated or missing answers and
// probabilities are errors.
func arrangeAnswers(qs []QuestionInfo, answers []Answer) ([]Answer, error) {
	ordered := len(answers) == len(qs)
	for i := 0; ordered && i < len(qs); i++ {
		ordered = answers[i].QuestionID == qs[i].ID && probabilitiesArranged(&qs[i], &answers[i])
	}
	if ordered {
		return answers, nil
	}
	index := make(map[string]int, len(answers))
	for i := range answers {
		if _, dup := index[answers[i].QuestionID]; dup {
			return nil, answerErr("duplicate answer")
		}
		index[answers[i].QuestionID] = i
	}
	if len(index) != len(qs) {
		return nil, answerErr(fmt.Sprintf("got %d answers for %d questions", len(index), len(qs)))
	}
	out := make([]Answer, len(qs))
	for i := range qs {
		q := &qs[i]
		j, ok := index[q.ID]
		if !ok {
			return nil, answerErr(fmt.Sprintf("questions[%d] has no answer", i))
		}
		a := answers[j]
		if !probabilitiesArranged(q, &a) {
			var err error
			if a, err = arrangeProbabilities(q, a); err != nil {
				return nil, fmt.Errorf("questions[%d]: %w", i, err)
			}
		}
		out[i] = a
	}
	return out, nil
}

// arrangeProbabilities returns a with a shallow copy of its typed answer whose
// probabilities are in q's option or level order.
func arrangeProbabilities(q *QuestionInfo, a Answer) (Answer, error) {
	var err error
	switch {
	case q.Kind == KindChoice && a.Choice != nil:
		c := *a.Choice
		c.Probabilities, err = arrange(q.Options, c.Probabilities, OptionProbability.key, "option")
		a.Choice = &c
	case q.Kind == KindScore && a.Score != nil:
		s := *a.Score
		s.Probabilities, err = arrange(q.Levels, s.Probabilities, LevelProbability.key, "level")
		a.Score = &s
	}
	return a, err
}

// probabilitiesArranged reports whether a's probabilities for q, if any, are in
// option or level order. A typed answer that does not fit q is left to
// checkAnswer.
func probabilitiesArranged(q *QuestionInfo, a *Answer) bool {
	switch {
	case q.Kind == KindChoice && a.Choice != nil:
		return keysInOrder(q.Options, a.Choice.Probabilities, OptionProbability.key)
	case q.Kind == KindScore && a.Score != nil:
		return keysInOrder(q.Levels, a.Score.Probabilities, LevelProbability.key)
	}
	return true
}

func (p OptionProbability) key() string { return p.Option }

func (p LevelProbability) key() string { return p.Level }

// keysInOrder reports whether items carry exactly keys, in order.
func keysInOrder[T any](keys []string, items []T, key func(T) string) bool {
	if len(items) != len(keys) {
		return false
	}
	for i := range items {
		if key(items[i]) != keys[i] {
			return false
		}
	}
	return true
}

// arrange returns a copy of items in key order. Every key must appear exactly
// once; member names the keys ("option" or "level") in errors.
func arrange[T any](keys []string, items []T, key func(T) string, member string) ([]T, error) {
	index := make(map[string]int, len(items))
	for i := range items {
		if _, dup := index[key(items[i])]; dup {
			return nil, answerErr("duplicate " + member + " probability")
		}
		index[key(items[i])] = i
	}
	out := make([]T, len(keys))
	for i, k := range keys {
		j, ok := index[k]
		if !ok {
			return nil, answerErr("probabilities do not cover exactly the " + member + "s")
		}
		out[i] = items[j]
	}
	if len(index) != len(keys) {
		return nil, answerErr("probabilities do not cover exactly the " + member + "s")
	}
	return out, nil
}

func validateUsage(u *Usage) error {
	if u == nil {
		return nil
	}
	if (u.InputTokens != nil && *u.InputTokens < 0) || (u.OutputTokens != nil && *u.OutputTokens < 0) {
		return answerErr("negative token count")
	}
	if u.Neurons != nil && (!finite(*u.Neurons) || *u.Neurons < 0) {
		return answerErr("invalid neurons")
	}
	return nil
}

// checkAnswer checks that a answers q with exactly the typed answer q's kind
// needs, with probabilities in request order and values that are consistent.
func checkAnswer(q *QuestionInfo, a *Answer) error {
	if a.QuestionID != q.ID {
		return answerErr("answer is not in question order")
	}
	if a.Kind != q.Kind {
		return answerErr("kind mismatch")
	}
	set := 0
	for _, p := range []bool{a.Choice != nil, a.Noul != nil, a.Score != nil} {
		if p {
			set++
		}
	}
	if set != 1 {
		return answerErr("exactly one typed answer is required")
	}
	switch q.Kind {
	case KindChoice:
		if a.Choice == nil {
			return answerErr("kind mismatch")
		}
		return checkChoice(q, a.Choice)
	case KindNoul:
		if a.Noul == nil {
			return answerErr("kind mismatch")
		}
		if !unitInterval(a.Noul.Value) {
			return answerErr("noul must be a finite number in [0,1]")
		}
		return nil
	case KindScore:
		if a.Score == nil {
			return answerErr("kind mismatch")
		}
		return checkScore(q, a.Score)
	}
	return answerErr("unknown kind")
}

// distribution summarizes probabilities read in request order.
type distribution struct {
	n        int
	nonzero  int
	digits   int
	sum      float64
	weighted float64 // Σ i·pᵢ over the positions i
	max      float64
}

// add appends one probability, which must be a finite number in [0,1].
func (d *distribution) add(p float64) error {
	if !unitInterval(p) {
		return answerErr("probability must be a finite number in [0,1]")
	}
	d.sum += p
	d.weighted += float64(d.n) * p
	d.n++
	if p != 0 {
		d.nonzero++
	}
	d.max = max(d.max, p)
	d.digits = max(d.digits, fractionDigits(p))
	return nil
}

// unit is one grid unit for the most precise probability.
func (d *distribution) unit() float64 { return gridUnit(d.digits) }

// checkSum allows each value one grid unit of rounding or truncation error:
// truncation can lower every value, rounding up can raise only nonzero values.
func (d *distribution) checkSum() error {
	eps, diff := d.unit(), d.sum-1
	if diff < -float64(d.n)*eps-floatSlack || diff > float64(d.nonzero)*eps+floatSlack {
		return answerErr("probabilities do not sum to 1 within rounding")
	}
	return nil
}

func checkChoice(q *QuestionInfo, c *ChoiceAnswer) error {
	if len(c.Probabilities) != len(q.Options) {
		return answerErr("probabilities do not cover exactly the options")
	}
	var d distribution
	chosen := -1
	for j, p := range c.Probabilities {
		if p.Option != q.Options[j] {
			return answerErr("probabilities are not in option order")
		}
		if err := d.add(p.Probability); err != nil {
			return err
		}
		if p.Option == c.Option {
			chosen = j
		}
	}
	if chosen < 0 {
		return answerErr("choice is not one of the options")
	}
	if err := checkConfidence(c.Confidence); err != nil {
		return err
	}
	if err := d.checkSum(); err != nil {
		return err
	}
	if c.Probabilities[chosen].Probability < d.max-floatSlack {
		return answerErr("choice is not the highest-probability option")
	}
	return nil
}

func checkScore(q *QuestionInfo, s *ScoreAnswer) error {
	if len(s.Probabilities) != len(q.Levels) {
		return answerErr("probabilities do not cover exactly the levels")
	}
	var d distribution
	for j, p := range s.Probabilities {
		if p.Level != q.Levels[j] {
			return answerErr("probabilities are not in level order")
		}
		if err := d.add(p.Probability); err != nil {
			return err
		}
	}
	if err := checkConfidence(s.Confidence); err != nil {
		return err
	}
	if !finite(s.Value) {
		return answerErr("score must be finite")
	}
	n := d.n
	epsP, epsS := d.unit(), gridUnit(fractionDigits(s.Value))
	if s.Value < -epsS || s.Value > float64(n-1)+epsS {
		return answerErr("score is out of range")
	}
	if err := d.checkSum(); err != nil {
		return err
	}
	if math.Abs(s.Value-d.weighted) > epsS+epsP*float64(n*(n-1)/2)+floatSlack {
		return answerErr("score does not match its probabilities")
	}
	return nil
}

func checkConfidence(c *float64) error {
	if c != nil && !unitInterval(*c) {
		return answerErr("confidence must be a finite number in [0,1]")
	}
	return nil
}

// gridUnit returns 10^-d for d fractional digits clamped to the inferred
// precision range.
func gridUnit(digits int) float64 {
	return math.Pow10(-min(max(digits, minPrecisionDigits), maxPrecisionDigits))
}

// fractionDigits counts the fractional digits of v's shortest decimal form, which
// equals the provider's literal unless the literal had trailing zeros. A value
// that a float32 holds exactly may be a float32 widened to float64, such as
// 0.33000001311302185 for 0.33, so its shortest float32 form is counted too and
// the smaller count wins. It formats into a stack buffer in exponent form, so it
// never allocates. NaN and the infinities have no exponent and count as zero
// digits.
func fractionDigits(v float64) int {
	var buf [32]byte
	d := exponentFormDigits(strconv.AppendFloat(buf[:0], v, 'e', -1, 64))
	if d > 0 && float64(float32(v)) == v {
		d = min(d, exponentFormDigits(strconv.AppendFloat(buf[:0], v, 'e', -1, 32)))
	}
	return d
}

// exponentFormDigits counts the fractional digits of a number in strconv's 'e'
// format, such as "9.1e-01" (two digits).
func exponentFormDigits(s []byte) int {
	mantissa, exponent, ok := bytes.Cut(s, []byte("e"))
	if !ok || len(exponent) < 2 {
		return 0
	}
	frac := 0
	if _, f, ok := bytes.Cut(mantissa, []byte(".")); ok {
		frac = len(f)
	}
	exp := 0
	for _, c := range exponent[1:] {
		exp = exp*10 + int(c-'0')
	}
	if exponent[0] == '-' {
		exp = -exp
	}
	return max(0, frac-exp)
}

func answerErr(reason string) error { return fmt.Errorf("%w: %s", errAnswers, reason) }

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func unitInterval(v float64) bool { return finite(v) && v >= 0 && v <= 1 }

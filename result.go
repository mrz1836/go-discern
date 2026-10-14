package discern

import (
	"math"
	"slices"
	"time"
)

// SchemaVersion identifies the JSON layout of a Result. Changes within v1 are
// additive only.
const SchemaVersion = "discern.result/v1"

// modulePath is this module's import path, recorded in every Result.
const modulePath = "github.com/mrz1836/go-discern"

// ResultStatus summarizes the provider outcomes of one evaluation.
type ResultStatus string

// Result statuses.
const (
	// Complete means every selected provider succeeded.
	Complete ResultStatus = "complete"

	// Partial means one of two selected providers failed.
	Partial ResultStatus = "partial"

	// Failed means every selected provider failed.
	Failed ResultStatus = "failed"
)

// OutcomeStatus is the status of one provider call.
type OutcomeStatus string

// Outcome statuses.
const (
	// OutcomeSucceeded means the call returned a valid observation.
	OutcomeSucceeded OutcomeStatus = "succeeded"

	// OutcomeFailed means the call failed; Outcome.Failure says how.
	OutcomeFailed OutcomeStatus = "failed"
)

// Result is the typed, storable record of one evaluation: every provider outcome,
// the effective policy, and the recommendations derived from them. Marshal it with
// encoding/json and restore it with ParseResult.
//
// A Result holds caller identifiers (question, option and level IDs, the
// correlation ID and evidence references) and provider outputs, but never the
// state, instructions, descriptions, credentials or provider bodies. It is not
// proof that an answer is true, not tamper-evident, and not a guarantee that a
// repeated evaluation would return the same values.
type Result struct {
	SchemaVersion string   `json:"schema_version"`
	Library       Library  `json:"library"`
	CorrelationID string   `json:"correlation_id"`
	EvidenceRefs  []string `json:"evidence_refs"`

	// Redacted lists the fields that Redact removed.
	Redacted []string `json:"redacted"`

	Providers ProviderSet  `json:"providers"`
	Status    ResultStatus `json:"status"`

	// StartedAt is the client wall-clock time, in UTC, when the calls started.
	StartedAt time.Time `json:"started_at"`

	// Elapsed is the client-observed time for all calls, not provider compute time.
	Elapsed time.Duration `json:"elapsed_ns"`

	Questions       []QuestionInfo   `json:"questions"`
	Outcomes        []Outcome        `json:"outcomes"`
	Policy          PolicySnapshot   `json:"policy"`
	Recommendations []Recommendation `json:"recommendations"`
	Overall         Overall          `json:"overall"`
}

// Library identifies the module that produced a Result.
type Library struct {
	Module  string `json:"module"`
	Version string `json:"version"`
}

// QuestionInfo records a question's identity and its option or level IDs, in
// request order. It never holds instructions or descriptions.
type QuestionInfo struct {
	ID      string   `json:"id"`
	Kind    Kind     `json:"kind"`
	Options []string `json:"options,omitempty"`
	Levels  []string `json:"levels,omitempty"`
}

// Outcome is one provider call. Exactly one of Observation and Failure is set.
type Outcome struct {
	Provider       Provider      `json:"provider"`
	RequestedModel string        `json:"requested_model"`
	Status         OutcomeStatus `json:"status"`
	Elapsed        time.Duration `json:"elapsed_ns"`
	Observation    *Observation  `json:"observation,omitempty"`
	Failure        *Failure      `json:"failure,omitempty"`
}

// Observation is a validated provider response. Answers are in request order.
// Optional provider fields are pointers or empty strings when absent.
//
// Model, RequestID and RayID are provider identifiers: each is empty or 1 to 256
// bytes of visible ASCII (0x21 to 0x7E). An observation that breaks the rule fails
// validation as FailureMalformed, so provider-echoed text never reaches a record
// or a recommendation summary.
type Observation struct {
	// Model is the model the provider reported, or empty if it reported none.
	Model string `json:"model,omitempty"`

	// RequestID is the provider's request identifier, when supplied.
	RequestID string `json:"request_id,omitempty"`

	// RayID is Cloudflare's cf-ray identifier, when supplied.
	RayID string `json:"ray_id,omitempty"`

	Usage   *Usage   `json:"usage,omitempty"`
	Answers []Answer `json:"answers"`
}

// Usage is provider-reported metering. Each field is nil when not reported; token
// counts from different providers are not one bill.
type Usage struct {
	InputTokens  *int64 `json:"input_tokens,omitempty"`
	OutputTokens *int64 `json:"output_tokens,omitempty"`

	// Neurons is Cloudflare's cf-ai-neurons billing figure, when supplied.
	Neurons *float64 `json:"neurons,omitempty"`
}

// Answer is one typed answer. Exactly one of Choice, Noul and Score is set,
// matching Kind.
type Answer struct {
	QuestionID string        `json:"question_id"`
	Kind       Kind          `json:"kind"`
	Choice     *ChoiceAnswer `json:"choice,omitempty"`
	Noul       *NoulAnswer   `json:"noul,omitempty"`
	Score      *ScoreAnswer  `json:"score,omitempty"`
}

// ChoiceAnswer is a Choice result: the chosen option, the probability of every
// option in request order, and the provider's confidence when reported.
//
// Confidence is a concentration statistic computed from the probabilities, not
// the probability that the answer is correct.
type ChoiceAnswer struct {
	Option        string              `json:"option"`
	Confidence    *float64            `json:"confidence,omitempty"`
	Probabilities []OptionProbability `json:"probabilities"`
}

// OptionProbability is the probability of one Choice option.
type OptionProbability struct {
	Option      string  `json:"option"`
	Probability float64 `json:"probability"`
}

// NoulAnswer is a Noul result. Value is the probability that the answer is yes, in
// [0,1]; it is not a boolean and carries no separate confidence.
type NoulAnswer struct {
	Value float64 `json:"value"`
}

// ScoreAnswer is a Score result. Value is the probability-weighted level position
// in [0, levels-1] and may be fractional. Probabilities are in level order.
type ScoreAnswer struct {
	Value         float64            `json:"value"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities []LevelProbability `json:"probabilities"`
}

// LevelProbability is the probability of one Score level.
type LevelProbability struct {
	Level       string  `json:"level"`
	Probability float64 `json:"probability"`
}

// Nearest returns the ID of the level nearest to Value; halves round up. It
// returns "" if the answer has no levels.
func (s ScoreAnswer) Nearest() string {
	n := len(s.Probabilities)
	if n == 0 {
		return ""
	}
	// math.Round rounds halves away from zero, which is up for the levels'
	// non-negative range, and unlike Floor(v+0.5) it never rounds a value just
	// below a half (such as 0.49999999999999994) up through the addition.
	// Clamp in floating point: converting an out-of-range float to int is
	// implementation-defined, so huge or infinite values would land on an
	// arbitrary level on some platforms. NaN selects the lowest level.
	i := 0
	switch v := math.Round(s.Value); {
	case v >= float64(n-1):
		i = n - 1
	case v > 0:
		i = int(v)
	}
	return s.Probabilities[i].Level
}

// Failure is the storage-safe form of a provider error. RequestID and RayID are
// provider identifiers, like an Observation's: a value that is not empty or 1 to
// 256 bytes of visible ASCII is dropped before it is recorded.
type Failure struct {
	Kind       FailureKind `json:"kind"`
	StatusCode int         `json:"status_code,omitempty"`
	RequestID  string      `json:"request_id,omitempty"`
	RayID      string      `json:"ray_id,omitempty"`

	// RetryAfter is the provider's retry hint, when supplied. It is a hint, not
	// proof that repeating the call is safe.
	RetryAfter *time.Duration `json:"retry_after_ns,omitempty"`

	// Codes are provider error codes, such as Cloudflare errors[].code.
	Codes []int `json:"codes,omitempty"`
}

// Overall is the per-request verdict over the questions the policy covers.
type Overall struct {
	// Status is NoRule when no question is covered; otherwise the most severe
	// covered status, so it is Accepted only if every covered question is.
	Status RecommendationStatus `json:"status"`

	// Uncovered lists the questions with status NoRule, in request order.
	Uncovered []string `json:"uncovered"`
}

// BatchItem is one EvaluateBatch result, with the same contract as Evaluate:
// Result is nil exactly when no call was attempted for the item.
type BatchItem struct {
	Result *Result
	Err    error
}

// Redaction selects the fields Redact removes.
type Redaction struct {
	CorrelationID bool
	EvidenceRefs  bool
	RequestIDs    bool
	Usage         bool
}

// Redaction names recorded in Result.Redacted.
const (
	redactedCorrelationID = "correlation_id"
	redactedEvidenceRefs  = "evidence_refs"
	redactedRequestIDs    = "request_ids"
	redactedUsage         = "usage"
)

// Outcome returns a copy of the provider's outcome, or false if the provider was
// not selected.
func (r *Result) Outcome(p Provider) (Outcome, bool) {
	if o := r.outcomeFor(p); o != nil {
		return o.clone(), true
	}
	return Outcome{}, false
}

// Observation returns a copy of the provider's observation, or false if the
// provider was not selected or failed.
func (r *Result) Observation(p Provider) (Observation, bool) {
	o := r.outcomeFor(p)
	if o == nil || o.Observation == nil {
		return Observation{}, false
	}
	return o.Observation.Clone(), true
}

// Recommendation returns a copy of the question's recommendation.
func (r *Result) Recommendation(questionID string) (Recommendation, bool) {
	if rec := r.recommendationFor(questionID); rec != nil {
		return rec.clone(), true
	}
	return Recommendation{}, false
}

// SelectedAnswers returns copies of the answers an accepted recommendation selected,
// in fixed provider order. It returns nil unless the question's status is Accepted.
func (r *Result) SelectedAnswers(questionID string) []Answer {
	rec := r.recommendationFor(questionID)
	if rec == nil || rec.Status != Accepted || rec.Selected == nil {
		return nil
	}
	var out []Answer
	for _, p := range rec.Selected.Providers {
		if a := r.answerOf(p, questionID); a != nil {
			out = append(out, a.clone())
		}
	}
	return out
}

// Redact returns a deep copy with the selected fields removed and listed in
// Redacted. Question, option and level IDs cannot be redacted; keep them free of
// sensitive data.
func (r *Result) Redact(opts Redaction) *Result {
	c := r.Clone()
	mark := func(name string) {
		if slices.Contains(c.Redacted, name) {
			return
		}
		c.Redacted = append(c.Redacted, name)
	}
	if opts.CorrelationID {
		c.CorrelationID = ""
		mark(redactedCorrelationID)
	}
	if opts.EvidenceRefs {
		c.EvidenceRefs = []string{}
		mark(redactedEvidenceRefs)
	}
	if opts.RequestIDs {
		c.clearRequestIDs()
		mark(redactedRequestIDs)
	}
	if opts.Usage {
		for i := range c.Outcomes {
			if obs := c.Outcomes[i].Observation; obs != nil {
				obs.Usage = nil
			}
		}
		mark(redactedUsage)
	}
	return c
}

// Answer returns a copy of the answer to the question.
func (o *Observation) Answer(questionID string) (Answer, bool) {
	if a := o.answerFor(-1, questionID); a != nil {
		return a.clone(), true
	}
	return Answer{}, false
}

// Choice returns a copy of the Choice answer to the question.
func (o *Observation) Choice(questionID string) (ChoiceAnswer, bool) {
	a, ok := o.Answer(questionID)
	if !ok || a.Choice == nil {
		return ChoiceAnswer{}, false
	}
	return *a.Choice, true
}

// Noul returns the Noul answer to the question.
func (o *Observation) Noul(questionID string) (NoulAnswer, bool) {
	a := o.answerFor(-1, questionID)
	if a == nil || a.Noul == nil {
		return NoulAnswer{}, false
	}
	return *a.Noul, true
}

// Score returns a copy of the Score answer to the question.
func (o *Observation) Score(questionID string) (ScoreAnswer, bool) {
	a, ok := o.Answer(questionID)
	if !ok || a.Score == nil {
		return ScoreAnswer{}, false
	}
	return *a.Score, true
}

// Clone returns a deep copy of the result.
func (r *Result) Clone() *Result {
	c := r.cloneObservations()
	c.Policy = r.Policy.clone()
	c.Recommendations = cloneRecommendations(r.Recommendations)
	c.Overall = Overall{Status: r.Overall.Status, Uncovered: slices.Clone(r.Overall.Uncovered)}
	return c
}

// cloneObservations returns a deep copy of everything except the policy
// snapshot, recommendations and overall verdict, which it leaves zero for the
// caller to set.
func (r *Result) cloneObservations() *Result {
	c := *r
	c.EvidenceRefs = slices.Clone(r.EvidenceRefs)
	c.Redacted = slices.Clone(r.Redacted)
	c.Questions = cloneQuestionInfos(r.Questions)
	if r.Outcomes != nil {
		c.Outcomes = make([]Outcome, len(r.Outcomes))
		for i := range r.Outcomes {
			c.Outcomes[i] = r.Outcomes[i].clone()
		}
	}
	c.Policy, c.Recommendations, c.Overall = PolicySnapshot{}, nil, Overall{}
	return &c
}

func (r *Result) clearRequestIDs() {
	for i := range r.Outcomes {
		if obs := r.Outcomes[i].Observation; obs != nil {
			obs.RequestID, obs.RayID = "", ""
		}
		if f := r.Outcomes[i].Failure; f != nil {
			f.RequestID, f.RayID = "", ""
		}
	}
}

// cloneQuestionInfos deep-copies question identities. All option and level IDs
// share one backing array.
func cloneQuestionInfos(qs []QuestionInfo) []QuestionInfo {
	if qs == nil {
		return nil
	}
	total := 0
	for i := range qs {
		total += len(qs[i].Options) + len(qs[i].Levels)
	}
	pool := make([]string, 0, total)
	out := make([]QuestionInfo, len(qs))
	for i, q := range qs {
		q.Options = appendCapped(&pool, q.Options)
		q.Levels = appendCapped(&pool, q.Levels)
		out[i] = q
	}
	return out
}

func (o Outcome) clone() Outcome {
	if o.Observation != nil {
		obs := o.Observation.Clone()
		o.Observation = &obs
	}
	if o.Failure != nil {
		f := *o.Failure
		if f.RetryAfter != nil {
			f.RetryAfter = new(*f.RetryAfter)
		}
		f.Codes = slices.Clone(f.Codes)
		o.Failure = &f
	}
	return o
}

// Clone returns a deep copy of the observation.
func (o *Observation) Clone() Observation {
	c := *o
	c.Usage = o.Usage.clone()
	c.Answers = cloneAnswers(o.Answers)
	return c
}

// outcomeFor returns the provider's outcome in place, or nil.
func (r *Result) outcomeFor(p Provider) *Outcome {
	for i := range r.Outcomes {
		if r.Outcomes[i].Provider == p {
			return &r.Outcomes[i]
		}
	}
	return nil
}

// recommendationFor returns the question's recommendation in place, or nil.
func (r *Result) recommendationFor(questionID string) *Recommendation {
	for i := range r.Recommendations {
		if r.Recommendations[i].QuestionID == questionID {
			return &r.Recommendations[i]
		}
	}
	return nil
}

// answerOf returns the provider's answer to the question in place, or nil when the
// provider was not selected, failed or did not answer.
func (r *Result) answerOf(p Provider, questionID string) *Answer {
	o := r.outcomeFor(p)
	if o == nil || o.Observation == nil {
		return nil
	}
	return o.Observation.answerFor(-1, questionID)
}

// answerFor returns the answer to the question in place, or nil. Validated
// observations hold the answer to question i at index i, so a caller that knows
// the question's position passes it to skip the scan; -1 means unknown.
func (o *Observation) answerFor(i int, questionID string) *Answer {
	if i >= 0 && i < len(o.Answers) && o.Answers[i].QuestionID == questionID {
		return &o.Answers[i]
	}
	for j := range o.Answers {
		if o.Answers[j].QuestionID == questionID {
			return &o.Answers[j]
		}
	}
	return nil
}

// usageCopy holds a copied Usage together with the values its pointers refer to,
// so that a copy costs one allocation.
type usageCopy struct {
	usage         Usage
	input, output int64
	neurons       float64
}

func (u *Usage) clone() *Usage {
	if u == nil {
		return nil
	}
	c := &usageCopy{}
	if u.InputTokens != nil {
		c.input = *u.InputTokens
		c.usage.InputTokens = &c.input
	}
	if u.OutputTokens != nil {
		c.output = *u.OutputTokens
		c.usage.OutputTokens = &c.output
	}
	if u.Neurons != nil {
		c.neurons = *u.Neurons
		c.usage.Neurons = &c.neurons
	}
	return &c.usage
}

func (a Answer) clone() Answer {
	z := newAnswerArena([]Answer{a})
	return z.copy(a)
}

// cloneAnswers deep-copies answers, keeping nil and empty distinct.
func cloneAnswers(answers []Answer) []Answer {
	if answers == nil {
		return nil
	}
	z := newAnswerArena(answers)
	out := make([]Answer, len(answers))
	for i := range answers {
		out[i] = z.copy(answers[i])
	}
	return out
}

// answerArena deep-copies answers into shared backing arrays, one per part type,
// so copying many answers costs a fixed number of allocations rather than
// several per answer. A confidence is stored next to its typed answer. Every
// copied slice is capped to its own length, so appending to one never
// overwrites another.
type answerArena struct {
	choices []choiceCopy
	nouls   []NoulAnswer
	scores  []scoreCopy
	options []OptionProbability
	levels  []LevelProbability
}

// choiceCopy and scoreCopy hold a copied typed answer and the confidence its
// pointer refers to.
type (
	choiceCopy struct {
		answer     ChoiceAnswer
		confidence float64
	}
	scoreCopy struct {
		answer     ScoreAnswer
		confidence float64
	}
)

// newAnswerArena returns an arena with room for copies of answers.
func newAnswerArena(answers []Answer) answerArena {
	var choices, nouls, scores, options, levels int
	for i := range answers {
		a := &answers[i]
		if a.Choice != nil {
			choices++
			options += len(a.Choice.Probabilities)
		}
		if a.Noul != nil {
			nouls++
		}
		if a.Score != nil {
			scores++
			levels += len(a.Score.Probabilities)
		}
	}
	return answerArena{
		choices: make([]choiceCopy, 0, choices),
		nouls:   make([]NoulAnswer, 0, nouls),
		scores:  make([]scoreCopy, 0, scores),
		options: make([]OptionProbability, 0, options),
		levels:  make([]LevelProbability, 0, levels),
	}
}

// copy returns a deep copy of a whose parts live in the arena.
func (z *answerArena) copy(a Answer) Answer {
	if a.Choice != nil {
		z.choices = append(z.choices, choiceCopy{answer: *a.Choice})
		c := &z.choices[len(z.choices)-1]
		if conf := c.answer.Confidence; conf != nil {
			c.confidence = *conf
			c.answer.Confidence = &c.confidence
		}
		c.answer.Probabilities = appendCapped(&z.options, c.answer.Probabilities)
		a.Choice = &c.answer
	}
	if a.Noul != nil {
		z.nouls = append(z.nouls, *a.Noul)
		a.Noul = &z.nouls[len(z.nouls)-1]
	}
	if a.Score != nil {
		z.scores = append(z.scores, scoreCopy{answer: *a.Score})
		s := &z.scores[len(z.scores)-1]
		if conf := s.answer.Confidence; conf != nil {
			s.confidence = *conf
			s.answer.Confidence = &s.confidence
		}
		s.answer.Probabilities = appendCapped(&z.levels, s.answer.Probabilities)
		a.Score = &s.answer
	}
	return a
}

// appendCapped appends s to *pool and returns the appended elements as a slice
// capped to its own length. It keeps nil and empty distinct. When *pool has room,
// no allocation is made; otherwise append grows it, and slices returned earlier
// keep their own elements.
func appendCapped[T any](pool *[]T, s []T) []T {
	if len(s) == 0 {
		return s[:0:0]
	}
	start := len(*pool)
	*pool = append(*pool, s...)
	return (*pool)[start:len(*pool):len(*pool)]
}

// appendPtr appends *p to *pool and returns a pointer to the copy, or nil for a
// nil p.
func appendPtr[T any](pool *[]T, p *T) *T {
	if p == nil {
		return nil
	}
	*pool = append(*pool, *p)
	return &(*pool)[len(*pool)-1]
}

package discern

import (
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/mrz1836/go-discern/internal/ident"
)

// Metric is an observed quantity a Check compares with its threshold.
type Metric string

// Metrics.
const (
	// MetricChoiceProbability is the probability of the option the provider chose,
	// in [0,1]. Choice only.
	MetricChoiceProbability Metric = "choice.probability"

	// MetricConfidence is the provider's concentration statistic, in [0,1]. It is
	// not the probability that the answer is correct, and values from different
	// providers are not comparable. Choice and Score only.
	MetricConfidence Metric = "confidence"

	// MetricNoul is the Noul probability of yes, in [0,1]. Noul only.
	MetricNoul Metric = "noul"

	// MetricScore is the fractional Score value, in [0, levels-1]. Score only.
	MetricScore Metric = "score"

	// MetricAgreement appears only in CheckResult, for the dual-provider agreement
	// test. It is not valid in a Check.
	MetricAgreement Metric = "agreement"
)

// Op is a threshold comparison: observed <op> threshold.
type Op string

// Comparison operators.
const (
	// AtLeast passes when observed >= threshold.
	AtLeast Op = "gte"

	// Above passes when observed > threshold.
	Above Op = "gt"

	// AtMost passes when observed <= threshold.
	AtMost Op = "lte"

	// Below passes when observed < threshold.
	Below Op = "lt"

	// Equal appears only in CheckResult, for a Choice agreement test (both
	// providers chose the same option). It is not valid in a Check.
	Equal Op = "eq"
)

// Policy is an explicit, typed recommendation policy. It is applied locally to
// observations and never causes a provider call. The zero Policy is invalid:
// Version is required.
//
// Policy and its parts carry JSON tags, so a policy can be loaded from
// configuration. Decode it with a json.Decoder that has DisallowUnknownFields
// set: otherwise a misspelled key, such as "treshold", is silently ignored and
// leaves a zero threshold that every value meets.
//
// A dual run evaluates each provider's answer against the checks that apply to
// it, so a policy for dual runs needs at least one applicable check for each
// provider: with checks scoped only to the preferred provider, the other provider
// has none, and the question is NotApplicable on dual runs.
type Policy struct {
	// Version is a caller label recorded with every result. The SDK does not
	// interpret it.
	Version string `json:"version"`

	// Questions holds at most one entry per question ID.
	Questions []QuestionPolicy `json:"questions"`

	// PreferOnConflict, when set, accepts the named provider's answer if the two
	// providers disagree and that provider passes its checks. The default ("")
	// makes disagreement ReviewRequired.
	PreferOnConflict Provider `json:"prefer_on_conflict"`

	// AllowPartial, when true, evaluates the surviving provider of a dual run alone
	// when the other failed. The default (false) makes such questions Incomplete.
	//
	// With AllowPartial, Evaluate can return an error matching ErrPartialFailure
	// together with Accepted recommendations that select the surviving provider:
	// read the recommendations, not only the error, before acting.
	AllowPartial bool `json:"allow_partial"`
}

// QuestionPolicy holds the checks for one question. All applicable checks must pass
// (AND); each check is evaluated separately for each provider it applies to.
type QuestionPolicy struct {
	Question string  `json:"question"`
	Checks   []Check `json:"checks"`

	// Agreement is the tolerance for dual Noul and Score questions; without it a
	// dual numeric question is NotApplicable. It is invalid on Choice questions,
	// which agree when both providers choose the same option.
	Agreement *Agreement `json:"agreement"`
}

// Check is one threshold rule. An empty Provider applies the check to every
// provider; an empty Model applies it to every model. A check scoped to a model
// matches the model the provider reported, falling back to the requested model, so
// an alias that moves to a new version stops matching.
type Check struct {
	// ID is the rule identifier recorded in results. When empty, it defaults to
	// "<question>/checks/<index>".
	ID        string   `json:"id"`
	Provider  Provider `json:"provider"`
	Model     string   `json:"model"`
	Metric    Metric   `json:"metric"`
	Op        Op       `json:"op"`
	Threshold float64  `json:"threshold"`
}

// Agreement is the largest allowed absolute difference between the two providers'
// Noul or Score values. The comparison allows 1e-9 of floating-point noise, so
// zero is valid but accepts values up to 1e-9 apart rather than only strictly
// equal ones.
type Agreement struct {
	MaxDifference float64 `json:"max_difference"`
}

// PolicySource says where a result's effective policy came from.
type PolicySource string

// Policy sources.
const (
	// PolicyNone means no policy was configured.
	PolicyNone PolicySource = "none"

	// PolicyClientDefault means the evaluator's WithDefaultPolicy applied.
	PolicyClientDefault PolicySource = "client_default"

	// PolicyCallOverride means a per-call WithPolicy applied.
	PolicyCallOverride PolicySource = "call_override"

	// PolicyDisabled means a per-call WithoutPolicy disabled the default.
	PolicyDisabled PolicySource = "disabled"

	// PolicyRecommend means Recommend applied the policy to a stored result.
	PolicyRecommend PolicySource = "recommend"
)

// PolicySnapshot records the effective policy of a result, with defaults made
// explicit. Unmatched lists policy questions that matched no request question.
type PolicySnapshot struct {
	Source    PolicySource `json:"source"`
	Policy    *Policy      `json:"policy"`
	Unmatched []string     `json:"unmatched"`
}

func (s PolicySnapshot) clone() PolicySnapshot {
	if s.Policy != nil {
		s.Policy = s.Policy.clone()
	}
	s.Unmatched = slices.Clone(s.Unmatched)
	return s
}

// clone returns a deep copy. All checks share one backing array, as do all
// agreements.
func (p *Policy) clone() *Policy {
	c := *p
	if p.Questions == nil {
		return &c
	}
	checks, agreements := 0, 0
	for i := range p.Questions {
		checks += len(p.Questions[i].Checks)
		if p.Questions[i].Agreement != nil {
			agreements++
		}
	}
	checkPool := make([]Check, 0, checks)
	agreementPool := make([]Agreement, 0, agreements)
	c.Questions = make([]QuestionPolicy, len(p.Questions))
	for i, q := range p.Questions {
		q.Checks = appendCapped(&checkPool, q.Checks)
		q.Agreement = appendPtr(&agreementPool, q.Agreement)
		c.Questions[i] = q
	}
	return &c
}

// normalizePolicy returns a deep copy with default check IDs filled in.
func normalizePolicy(p *Policy) *Policy {
	c := p.clone()
	if c.Questions == nil {
		c.Questions = []QuestionPolicy{}
	}
	for i := range c.Questions {
		q := &c.Questions[i]
		if q.Checks == nil {
			q.Checks = []Check{}
		}
		for j := range q.Checks {
			if q.Checks[j].ID == "" {
				q.Checks[j].ID = q.Question + "/checks/" + strconv.Itoa(j)
			}
		}
	}
	return c
}

// question returns the policy entry for a question, or nil.
func (p *Policy) question(id string) *QuestionPolicy {
	for i := range p.Questions {
		if p.Questions[i].Question == id {
			return &p.Questions[i]
		}
	}
	return nil
}

// checkPath returns "questions[i].checks[j]" followed by suffix.
func checkPath(i, j int, suffix string) string {
	return fmt.Sprintf("questions[%d].checks[%d]%s", i, j, suffix)
}

// validatePolicy checks the request-independent rules on a normalized policy.
// registered reports whether a provider may be named. Like request validation,
// it formats a field path only for the error it returns.
func validatePolicy(p *Policy, registered func(Provider) bool) error {
	if prob := identifierProblem(p.Version); prob != "" {
		return invalid(ErrInvalidPolicy, "version", prob)
	}
	if p.PreferOnConflict != "" && !registered(p.PreferOnConflict) {
		return invalid(ErrInvalidPolicy, "prefer_on_conflict", "names a provider that is not registered")
	}
	var questions, ids idSet
	questions.reset(len(p.Questions))
	checks := 0
	for i := range p.Questions {
		checks += len(p.Questions[i].Checks)
	}
	ids.reset(checks)
	for i := range p.Questions {
		q := &p.Questions[i]
		if !ident.Name(q.Question, MaxQuestionIDLength) {
			return invalid(ErrInvalidPolicy, questionPath(i, ".question"), "must match [A-Za-z0-9_.-]{1,100}")
		}
		if questions.add(q.Question) {
			return invalid(ErrInvalidPolicy, questionPath(i, ".question"), "duplicate question policy")
		}
		if err := validateQuestionPolicy(q, i, registered, &ids); err != nil {
			return err
		}
	}
	return nil
}

func validateQuestionPolicy(q *QuestionPolicy, i int, registered func(Provider) bool, ids *idSet) error {
	if len(q.Checks) == 0 {
		return invalid(ErrInvalidPolicy, questionPath(i, ".checks"), "at least one check is required")
	}
	if a := q.Agreement; a != nil && (!finite(a.MaxDifference) || a.MaxDifference < 0) {
		return invalid(ErrInvalidPolicy, questionPath(i, ".agreement.max_difference"), "must be a finite number >= 0")
	}
	for j := range q.Checks {
		c := &q.Checks[j]
		if suffix, reason := checkProblem(c, registered); reason != "" {
			return invalid(ErrInvalidPolicy, checkPath(i, j, suffix), reason)
		}
		if ids.add(c.ID) {
			return invalid(ErrInvalidPolicy, checkPath(i, j, ".id"), "duplicate check ID")
		}
	}
	return nil
}

// checkProblem returns why a check is invalid, as a field suffix and a reason, or
// "" reasons when it is valid.
func checkProblem(c *Check, registered func(Provider) bool) (suffix, reason string) {
	if prob := identifierProblem(c.ID); prob != "" {
		return ".id", prob
	}
	if c.Provider != "" && !registered(c.Provider) {
		return ".provider", "names a provider that is not registered"
	}
	if c.Model != "" {
		if prob := identifierProblem(c.Model); prob != "" {
			return ".model", prob
		}
	}
	switch c.Op {
	case AtLeast, Above, AtMost, Below:
	case Equal:
		return ".op", "eq is used only in agreement results"
	default:
		return ".op", "unknown op"
	}
	if !finite(c.Threshold) {
		return ".threshold", "must be finite"
	}
	switch c.Metric {
	case MetricChoiceProbability, MetricConfidence, MetricNoul:
		return boundsProblem(c, 1)
	case MetricScore:
		// The upper bound depends on the question's level count; it is checked
		// against the request.
		return boundsProblem(c, math.Inf(1))
	case MetricAgreement:
		return ".metric", "agreement is configured with QuestionPolicy.Agreement"
	}
	return ".metric", "unknown metric"
}

// boundsProblem rejects thresholds outside [0, upper] and bounds no value can meet.
func boundsProblem(c *Check, upper float64) (suffix, reason string) {
	if c.Threshold < 0 || c.Threshold > upper {
		return ".threshold", "is out of range for the metric"
	}
	if (c.Op == Above && c.Threshold == upper) || (c.Op == Below && c.Threshold == 0) {
		return ".threshold", "no value can satisfy the bound"
	}
	return "", ""
}

// validatePolicyQuestions checks the rules that depend on the request's questions.
// Policy entries for questions not in the request are ignored here and reported in
// the snapshot's Unmatched list.
func validatePolicyQuestions(p *Policy, qs []QuestionInfo) error {
	for i := range p.Questions {
		qp := &p.Questions[i]
		q, ok := findQuestion(qs, qp.Question)
		if !ok {
			continue
		}
		if qp.Agreement != nil && q.Kind == KindChoice {
			return invalid(ErrInvalidPolicy, questionPath(i, ".agreement"), "choice questions agree on the chosen option")
		}
		for j := range qp.Checks {
			c := &qp.Checks[j]
			if !metricFits(c.Metric, q.Kind) {
				return invalid(ErrInvalidPolicy, checkPath(i, j, ".metric"), "does not apply to a "+string(q.Kind)+" question")
			}
			if c.Metric == MetricScore {
				if suffix, reason := boundsProblem(c, float64(len(q.Levels)-1)); reason != "" {
					return invalid(ErrInvalidPolicy, checkPath(i, j, suffix), reason)
				}
			}
		}
	}
	return nil
}

func metricFits(m Metric, k Kind) bool {
	switch m {
	case MetricChoiceProbability:
		return k == KindChoice
	case MetricConfidence:
		return k == KindChoice || k == KindScore
	case MetricNoul:
		return k == KindNoul
	case MetricScore:
		return k == KindScore
	case MetricAgreement:
		return false
	}
	return false
}

func findQuestion(qs []QuestionInfo, id string) (*QuestionInfo, bool) {
	for i := range qs {
		if qs[i].ID == id {
			return &qs[i], true
		}
	}
	return nil, false
}

// unmatchedQuestions lists policy questions absent from the request, in policy order.
func unmatchedQuestions(p *Policy, qs []QuestionInfo) []string {
	out := []string{}
	for _, qp := range p.Questions {
		if _, ok := findQuestion(qs, qp.Question); !ok {
			out = append(out, qp.Question)
		}
	}
	return out
}

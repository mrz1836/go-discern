package discern

import (
	"math"
	"slices"
	"strconv"
	"strings"
)

// RecommendationStatus is the outcome of applying a policy to one question.
type RecommendationStatus string

// Recommendation statuses. Accepted through Incomplete are listed from least to
// most severe, the order in which Overall picks the most severe covered status.
// NoRule, for a question no policy covers, is outside that order.
const (
	// Accepted means every applicable check passed (and, for dual runs, the
	// providers agreed or the preferred provider won the conflict).
	Accepted RecommendationStatus = "accepted"

	// BelowThreshold means at least one applicable check failed.
	BelowThreshold RecommendationStatus = "below_threshold"

	// ReviewRequired means the two providers disagreed and no conflict preference
	// resolved it.
	ReviewRequired RecommendationStatus = "review_required"

	// NotApplicable means the policy could not be applied, for example because no
	// check matched a provider or model.
	NotApplicable RecommendationStatus = "not_applicable"

	// Incomplete means a required provider failed or a required metric was absent.
	Incomplete RecommendationStatus = "incomplete"

	// NoRule means no policy covered the question.
	NoRule RecommendationStatus = "no_rule"
)

// severity orders the statuses: Incomplete > NotApplicable > ReviewRequired >
// BelowThreshold > Accepted. NoRule is outside the order.
func severity(s RecommendationStatus) int {
	switch s {
	case Accepted:
		return 1
	case BelowThreshold:
		return 2
	case ReviewRequired:
		return 3
	case NotApplicable:
		return 4
	case Incomplete:
		return 5
	case NoRule:
		return 0
	}
	return 6 // unknown statuses are treated as the most severe
}

// ReasonCode is a machine-readable reason for a recommendation. The list is closed
// and documented; parsers of stored records must treat an unknown code as
// non-accepting.
type ReasonCode string

// Reason codes.
const (
	// ReasonNoPolicy means no policy was in effect (NoRule).
	ReasonNoPolicy ReasonCode = "no_policy"

	// ReasonQuestionNotInPolicy means the policy has no entry for the question (NoRule).
	ReasonQuestionNotInPolicy ReasonCode = "question_not_in_policy"

	// ReasonProviderFailed means a provider the policy needs failed (Incomplete).
	ReasonProviderFailed ReasonCode = "provider_failed"

	// ReasonMetricAbsent means a checked metric was not reported (Incomplete).
	ReasonMetricAbsent ReasonCode = "metric_absent"

	// ReasonNoApplicableCheck means no check applies to a provider (NotApplicable).
	ReasonNoApplicableCheck ReasonCode = "no_applicable_check"

	// ReasonModelMismatch means a provider's checks are all scoped to other models
	// (NotApplicable).
	ReasonModelMismatch ReasonCode = "model_mismatch"

	// ReasonAgreementMissing means a dual Noul or Score question has no Agreement
	// tolerance (NotApplicable).
	ReasonAgreementMissing ReasonCode = "agreement_missing"

	// ReasonProvidersDisagree means the two providers disagreed.
	ReasonProvidersDisagree ReasonCode = "providers_disagree"

	// ReasonBelowThreshold means at least one applicable check failed. It comes
	// with BelowThreshold, and with ReviewRequired when the providers of a dual
	// run disagreed and a check also failed.
	ReasonBelowThreshold ReasonCode = "below_threshold"

	// ReasonThresholdsMet means a single provider passed every check (Accepted).
	ReasonThresholdsMet ReasonCode = "thresholds_met"

	// ReasonProvidersAgree means both providers agreed and passed (Accepted).
	ReasonProvidersAgree ReasonCode = "providers_agree"

	// ReasonPreferredOnConflict means the preferred provider passed and was selected
	// despite a disagreement (Accepted).
	ReasonPreferredOnConflict ReasonCode = "preferred_on_conflict"

	// ReasonPartialSingleProvider means AllowPartial evaluated the surviving
	// provider of a dual run alone.
	ReasonPartialSingleProvider ReasonCode = "partial_single_provider"

	// ReasonModelCheckSkipped means a check scoped to another model was skipped
	// for a provider while other checks still applied to it, which can signal
	// that the provider's model changed. It is informational and never changes
	// the status.
	ReasonModelCheckSkipped ReasonCode = "model_check_skipped"
)

// Recommendation is the policy outcome for one question. Selected is nil unless
// Status is Accepted. Summary is deterministic text built from observed numbers and
// checks; it is for people and is not a stable contract (the reason codes are).
type Recommendation struct {
	QuestionID string               `json:"question_id"`
	Status     RecommendationStatus `json:"status"`
	Selected   *Selected            `json:"selected"`
	Reasons    []ReasonCode         `json:"reasons"`
	Checks     []CheckResult        `json:"checks"`
	Summary    string               `json:"summary"`
}

// Selected points at the accepted answers: the providers whose answers were
// selected, in fixed order, and for Choice the selected option. Numeric questions
// carry no blended value; use Result.SelectedAnswers for the typed answers.
type Selected struct {
	Providers []Provider `json:"providers"`
	Option    string     `json:"option,omitempty"`
}

// CheckResult is one evaluated check for one provider, or the agreement test
// (RuleID "<question>/agreement", empty Provider). Observed is nil when the metric
// was absent or the check was skipped.
//
// A Choice agreement test compares options, not numbers: its Op is Equal, its
// Threshold is 0 and its Observed is nil, and Passed says whether both providers
// chose the same option.
type CheckResult struct {
	RuleID        string     `json:"rule_id"`
	Provider      Provider   `json:"provider"`
	ScopeProvider Provider   `json:"scope_provider"`
	ScopeModel    string     `json:"scope_model"`
	ObservedModel string     `json:"observed_model"`
	Metric        Metric     `json:"metric"`
	Op            Op         `json:"op"`
	Threshold     float64    `json:"threshold"`
	Observed      *float64   `json:"observed"`
	Passed        bool       `json:"passed"`
	Skipped       ReasonCode `json:"skipped,omitempty"`
}

func (r Recommendation) clone() Recommendation {
	z := newRecArena([]Recommendation{r})
	return z.copy(r)
}

// cloneRecommendations deep-copies recommendations, keeping nil and empty distinct.
func cloneRecommendations(recs []Recommendation) []Recommendation {
	if recs == nil {
		return nil
	}
	z := newRecArena(recs)
	out := make([]Recommendation, len(recs))
	for i := range recs {
		out[i] = z.copy(recs[i])
	}
	return out
}

// recArena holds the parts of many recommendations in shared backing arrays, one
// per part type, both for copies and for recommendations being built. Every slice
// handed out is capped to its own length, so appending to one never overwrites
// another.
type recArena struct {
	selected []selectedCopy
	reasons  []ReasonCode
	checks   []CheckResult
	floats   []float64
}

// selectedCopy holds a Selected and room for the providers of a dual run.
type selectedCopy struct {
	selected  Selected
	providers [2]Provider
}

// newRecArena returns an arena with room for copies of recs.
func newRecArena(recs []Recommendation) recArena {
	var selected, reasons, checks, floats int
	for i := range recs {
		r := &recs[i]
		if r.Selected != nil {
			selected++
		}
		reasons += len(r.Reasons)
		checks += len(r.Checks)
		for j := range r.Checks {
			if r.Checks[j].Observed != nil {
				floats++
			}
		}
	}
	return makeRecArena(selected, reasons, checks, floats)
}

func makeRecArena(selected, reasons, checks, floats int) recArena {
	return recArena{
		selected: make([]selectedCopy, 0, selected),
		reasons:  make([]ReasonCode, 0, reasons),
		checks:   make([]CheckResult, 0, checks),
		floats:   make([]float64, 0, floats),
	}
}

// copy returns a deep copy of r whose parts live in the arena.
func (z *recArena) copy(r Recommendation) Recommendation {
	if r.Selected != nil {
		r.Selected = z.selection(r.Selected.Option, r.Selected.Providers...)
	}
	r.Reasons = appendCapped(&z.reasons, r.Reasons)
	r.Checks = appendCapped(&z.checks, r.Checks)
	for j := range r.Checks {
		r.Checks[j].Observed = appendPtr(&z.floats, r.Checks[j].Observed)
	}
	return r
}

// selection returns a new Selected in the arena, keeping nil and empty provider
// lists distinct.
func (z *recArena) selection(option string, providers ...Provider) *Selected {
	z.selected = append(z.selected, selectedCopy{selected: Selected{Option: option}})
	c := &z.selected[len(z.selected)-1]
	switch {
	case providers == nil:
	case len(providers) <= len(c.providers):
		n := copy(c.providers[:], providers)
		c.selected.Providers = c.providers[:n:n]
	default:
		c.selected.Providers = slices.Clone(providers)
	}
	return &c.selected
}

// Recommend applies a policy to an existing result without any provider call. It
// returns a new Result whose policy source is PolicyRecommend; res is not modified.
// It returns ErrInvalidPolicy for an invalid policy and ErrInvalidRecord when the
// parts of res it keeps are inconsistent. The parts it replaces (the policy
// snapshot, recommendations and overall verdict) are not checked, so a result
// built by hand may leave them zero.
//
// Two-sided gates, such as escalating at noul >= 0.9 and closing at noul <= 0.1,
// are two Recommend calls with two policies.
func Recommend(res *Result, policy Policy) (*Result, error) {
	if res == nil {
		return nil, invalid(ErrInvalidRecord, "result", "is nil")
	}
	if err := validateEvidence(res); err != nil {
		return nil, err
	}
	p := normalizePolicy(&policy)
	if err := validatePolicy(p, Provider.valid); err != nil {
		return nil, err
	}
	if err := validatePolicyQuestions(p, res.Questions); err != nil {
		return nil, err
	}
	c := res.cloneObservations()
	c.Policy = PolicySnapshot{Source: PolicyRecommend, Policy: p}
	applyPolicy(c)
	return c, nil
}

// applyPolicy fills Recommendations, Overall and Policy.Unmatched from the result's
// outcomes and Policy snapshot.
func applyPolicy(r *Result) {
	r.Policy.Unmatched = []string{}
	if r.Policy.Policy != nil {
		r.Policy.Unmatched = unmatchedQuestions(r.Policy.Policy, r.Questions)
	}
	b := newRecommender(r)
	r.Recommendations = make([]Recommendation, len(r.Questions))
	for i := range r.Questions {
		r.Recommendations[i] = b.question(i)
	}
	r.Overall = overallOf(r.Recommendations)
}

func overallOf(recs []Recommendation) Overall {
	uncovered := 0
	for i := range recs {
		if recs[i].Status == NoRule {
			uncovered++
		}
	}
	o := Overall{Status: NoRule, Uncovered: make([]string, 0, uncovered)}
	for _, rec := range recs {
		if rec.Status == NoRule {
			o.Uncovered = append(o.Uncovered, rec.QuestionID)
			continue
		}
		o.Status = overallWith(o.Status, rec.Status)
	}
	return o
}

// overallWith returns the overall status after adding a covered status: the
// first covered status, or the more severe of the two. Unknown statuses rank
// above every known one.
func overallWith(overall, covered RecommendationStatus) RecommendationStatus {
	if overall == NoRule || severity(covered) > severity(overall) {
		return covered
	}
	return overall
}

// evalState is the outcome of one provider's checks for one question.
type evalState uint8

const (
	evalPass evalState = iota
	evalFail
	evalAbsent
	evalNoCheck
	evalModelMismatch
)

type providerEval struct {
	provider Provider
	answer   *Answer
	model    string
	state    evalState
}

// maxReasons bounds the reasons of one recommendation: model_check_skipped, two
// blocking reasons from the two providers of a dual run, and agreement_missing.
const maxReasons = 4

// recommender builds the recommendations of one result, one question at a time.
// Their checks, reasons, observed values and selections share one arena, and the
// summary fragments of the current question are joined in one reused buffer, so
// a question costs its summary string (and, for a dual run, the agreement rule
// ID) rather than an allocation per check and fragment.
//
// Every path writes the providers' check fragments first, in provider order, so
// they are appended while the checks are evaluated.
type recommender struct {
	res   *Result
	pol   *Policy
	arena recArena
	text  []byte // fragments of the current question, separated by "; "

	rec          Recommendation // the current question
	reasonsStart int
	checksStart  int
}

func newRecommender(r *Result) *recommender {
	b := &recommender{res: r, pol: r.Policy.Policy}
	if b.pol != nil {
		b.text = make([]byte, 0, 256)
	}
	_, n := r.Providers.list()
	covered, checks := 0, 0
	if b.pol != nil {
		for i := range r.Questions {
			if qp := b.pol.question(r.Questions[i].ID); qp != nil {
				covered++
				checks += n*len(qp.Checks) + 1
			}
		}
	}
	reasons := len(r.Questions) - covered + maxReasons*covered
	b.arena = makeRecArena(covered, reasons, checks, checks)
	return b
}

func (b *recommender) reason(code ReasonCode) {
	if slices.Contains(b.arena.reasons[b.reasonsStart:], code) {
		return
	}
	b.arena.reasons = append(b.arena.reasons, code)
}

func (b *recommender) addCheck(cr CheckResult) {
	b.arena.checks = append(b.arena.checks, cr)
}

// part starts a summary fragment made of the given pieces.
func (b *recommender) part(pieces ...string) {
	if len(b.text) > 0 {
		b.text = append(b.text, "; "...)
	}
	for _, p := range pieces {
		b.text = append(b.text, p...)
	}
}

// number appends a number to the current fragment, rounded to six decimal places.
func (b *recommender) number(v float64) {
	b.text = strconv.AppendFloat(b.text, math.Round(v*1e6)/1e6, 'f', -1, 64)
}

// noPolicySummary is the summary of every question when no policy is in effect.
const noPolicySummary = "no rule: no policy in effect"

// finish completes the current recommendation with its summary.
func (b *recommender) finish(status RecommendationStatus, selected *Selected) Recommendation {
	r := b.collect(status, selected)
	prefix := summaryPrefix(status)
	option := ""
	if selected != nil {
		option = selected.Option
	}
	var sb strings.Builder
	sb.Grow(len(prefix) + 1 + len(option) + 2 + len(b.text))
	sb.WriteString(prefix)
	if option != "" {
		sb.WriteByte(' ')
		sb.WriteString(option)
	}
	if len(b.text) > 0 {
		sb.WriteString(": ")
		sb.Write(b.text)
	}
	r.Summary = sb.String()
	return r
}

// collect completes the current recommendation without its summary.
func (b *recommender) collect(status RecommendationStatus, selected *Selected) Recommendation {
	r := b.rec
	r.Status, r.Selected = status, selected
	end := len(b.arena.reasons)
	r.Reasons = b.arena.reasons[b.reasonsStart:end:end]
	end = len(b.arena.checks)
	r.Checks = b.arena.checks[b.checksStart:end:end]
	return r
}

func summaryPrefix(status RecommendationStatus) string {
	switch status {
	case Accepted:
		return "accepted"
	case BelowThreshold:
		return "below threshold"
	case ReviewRequired:
		return "review required"
	case NotApplicable:
		return "not applicable"
	case Incomplete:
		return "no recommendation"
	case NoRule:
		return "no rule"
	}
	return string(status)
}

// question builds the recommendation for question i.
func (b *recommender) question(i int) Recommendation {
	q := &b.res.Questions[i]
	b.rec = Recommendation{QuestionID: q.ID}
	b.reasonsStart, b.checksStart = len(b.arena.reasons), len(b.arena.checks)
	b.text = b.text[:0]
	if b.pol == nil {
		b.reason(ReasonNoPolicy)
		r := b.collect(NoRule, nil)
		r.Summary = noPolicySummary
		return r
	}
	qp := b.pol.question(q.ID)
	if qp == nil {
		b.reason(ReasonQuestionNotInPolicy)
		b.part("question not in policy ", b.pol.Version)
		return b.finish(NoRule, nil)
	}

	var (
		evals          [2]providerEval
		failed         [2]Provider
		nEvals, nFails int
	)
	ps, n := b.res.Providers.list()
	for _, p := range ps[:n] {
		o := b.res.outcomeFor(p)
		if o == nil || o.Observation == nil {
			failed[nFails] = p
			nFails++
			continue
		}
		evals[nEvals] = b.evaluate(i, q, qp, o)
		nEvals++
	}

	switch {
	case nEvals == 0:
		b.reason(ReasonProviderFailed)
		b.failures(failed[:nFails])
		return b.finish(Incomplete, nil)
	case nFails > 0 && !b.pol.AllowPartial:
		b.reason(ReasonProviderFailed)
		b.failures(failed[:nFails])
		b.part("policy requires both providers")
		return b.finish(Incomplete, nil)
	case nEvals == 1:
		if nFails > 0 {
			b.reason(ReasonPartialSingleProvider)
		}
		return b.single(q, evals[0], failed[:nFails])
	}
	return b.dual(q, qp, evals[0], evals[1])
}

func (b *recommender) failures(failed []Provider) {
	for _, p := range failed {
		kind := "unknown"
		if o := b.res.outcomeFor(p); o != nil && o.Failure != nil {
			kind = string(o.Failure.Kind)
		}
		b.part(string(p), " failed (", kind, ")")
	}
}

// blocking adds the reason for a state that prevents acceptance and returns the
// status it implies, or "" for pass and fail.
func (b *recommender) blocking(e providerEval) RecommendationStatus {
	switch e.state {
	case evalAbsent:
		b.reason(ReasonMetricAbsent)
		return Incomplete
	case evalNoCheck:
		b.reason(ReasonNoApplicableCheck)
		return NotApplicable
	case evalModelMismatch:
		b.reason(ReasonModelMismatch)
		return NotApplicable
	case evalPass, evalFail:
	}
	return ""
}

func (b *recommender) single(q *QuestionInfo, e providerEval, failed []Provider) Recommendation {
	if len(failed) > 0 {
		b.failures(failed)
		b.part("evaluated ", string(e.provider), " alone")
	}
	if status := b.blocking(e); status != "" {
		return b.finish(status, nil)
	}
	if e.state == evalFail {
		b.reason(ReasonBelowThreshold)
		return b.finish(BelowThreshold, nil)
	}
	b.reason(ReasonThresholdsMet)
	return b.finish(Accepted, b.arena.selection(chosenOption(q, e), e.provider))
}

func (b *recommender) dual(q *QuestionInfo, qp *QuestionPolicy, a, c providerEval) Recommendation {
	worst := worse(b.blocking(a), b.blocking(c))
	agree, evaluable := b.agreement(q, qp, a, c)
	if !evaluable {
		b.reason(ReasonAgreementMissing)
		b.part("a dual ", string(q.Kind), " question needs an agreement tolerance")
		worst = worse(worst, NotApplicable)
	}
	if worst != "" {
		return b.finish(worst, nil)
	}

	bothPass := a.state == evalPass && c.state == evalPass
	switch {
	case agree && bothPass:
		b.reason(ReasonProvidersAgree)
		return b.finish(Accepted, b.arena.selection(chosenOption(q, a), a.provider, c.provider))
	case agree:
		b.reason(ReasonBelowThreshold)
		return b.finish(BelowThreshold, nil)
	}

	b.reason(ReasonProvidersDisagree)
	if e, ok := preferred(b.pol.PreferOnConflict, a, c); ok && e.state == evalPass {
		b.reason(ReasonPreferredOnConflict)
		b.part("preferred ", string(e.provider), " on conflict")
		return b.finish(Accepted, b.arena.selection(chosenOption(q, e), e.provider))
	}
	if a.state == evalFail || c.state == evalFail {
		b.reason(ReasonBelowThreshold)
	}
	return b.finish(ReviewRequired, nil)
}

// worse returns the more severe of two blocking statuses; "" means none.
func worse(x, y RecommendationStatus) RecommendationStatus {
	if x == "" || (y != "" && severity(y) > severity(x)) {
		return y
	}
	return x
}

// preferred returns the evaluation of the conflict-preferred provider, if any.
func preferred(pref Provider, evals ...providerEval) (providerEval, bool) {
	for _, e := range evals {
		if pref != "" && e.provider == pref {
			return e, true
		}
	}
	return providerEval{}, false
}

// agreement records the agreement test. evaluable is false for a dual numeric
// question without an Agreement tolerance.
func (b *recommender) agreement(q *QuestionInfo, qp *QuestionPolicy, a, c providerEval) (agree, evaluable bool) {
	cr := CheckResult{RuleID: q.ID + "/agreement", Metric: MetricAgreement}
	if q.Kind == KindChoice {
		cr.Op = Equal
		agree = a.answer.Choice.Option == c.answer.Choice.Option
		cr.Passed = agree
		b.addCheck(cr)
		if agree {
			b.part("both chose ", a.answer.Choice.Option)
		} else {
			b.part(string(a.provider), " chose ", a.answer.Choice.Option, ", ", string(c.provider), " chose ", c.answer.Choice.Option)
		}
		return agree, true
	}
	if qp.Agreement == nil {
		return false, false
	}
	va, vc := numericValue(a.answer), numericValue(c.answer)
	diff := math.Abs(va - vc)
	cr.Op, cr.Threshold, cr.Observed = AtMost, qp.Agreement.MaxDifference, appendPtr(&b.arena.floats, &diff)
	agree = diff <= qp.Agreement.MaxDifference+floatSlack
	cr.Passed = agree
	b.addCheck(cr)
	b.part(string(a.provider), " ", string(q.Kind), " ")
	b.number(va)
	b.text = append(b.text, " and "...)
	b.text = append(b.text, c.provider...)
	b.text = append(b.text, ' ')
	b.text = append(b.text, q.Kind...)
	b.text = append(b.text, ' ')
	b.number(vc)
	b.text = append(b.text, " differ by "...)
	b.number(diff)
	if agree {
		b.text = append(b.text, " <= "...)
	} else {
		b.text = append(b.text, " > "...)
	}
	b.number(qp.Agreement.MaxDifference)
	return agree, true
}

// chosenOption returns the option e chose for a Choice question, or "".
func chosenOption(q *QuestionInfo, e providerEval) string {
	if q.Kind == KindChoice {
		return e.answer.Choice.Option
	}
	return ""
}

// evaluate applies the question's checks to one provider's answer, appending the
// check results and summary fragments.
func (b *recommender) evaluate(i int, q *QuestionInfo, qp *QuestionPolicy, o *Outcome) providerEval {
	e := providerEval{provider: o.Provider, model: o.Observation.Model, answer: o.Observation.answerFor(i, q.ID)}
	if e.model == "" {
		e.model = o.RequestedModel
	}
	candidates, applied := 0, 0
	absent, failed := false, false
	for _, c := range qp.Checks {
		if c.Provider != "" && c.Provider != o.Provider {
			continue
		}
		candidates++
		cr := CheckResult{
			RuleID: c.ID, Provider: o.Provider, ScopeProvider: c.Provider, ScopeModel: c.Model,
			ObservedModel: e.model, Metric: c.Metric, Op: c.Op, Threshold: c.Threshold,
		}
		if c.Model != "" && c.Model != e.model {
			cr.Skipped = ReasonModelMismatch
			b.addCheck(cr)
			continue
		}
		applied++
		v, ok := metricValue(e.answer, c.Metric)
		if !ok {
			absent = true
			b.addCheck(cr)
			b.part(string(o.Provider), " ", string(c.Metric), " absent")
			continue
		}
		cr.Observed = appendPtr(&b.arena.floats, &v)
		cr.Passed = compare(v, c.Op, c.Threshold)
		failed = failed || !cr.Passed
		b.addCheck(cr)
		b.checkPart(o.Provider, c, v, cr.Passed)
	}
	if applied > 0 && applied < candidates {
		b.reason(ReasonModelCheckSkipped)
	}
	switch {
	case candidates == 0:
		e.state = evalNoCheck
		b.part("no check applies to ", string(o.Provider))
	case applied == 0:
		e.state = evalModelMismatch
		b.part("no check matches ", string(o.Provider), " model ", e.model)
	case absent:
		e.state = evalAbsent
	case failed:
		e.state = evalFail
	default:
		e.state = evalPass
	}
	return e
}

// checkPart writes "<provider> <metric> <observed> [is not ]<op> <threshold>".
func (b *recommender) checkPart(p Provider, c Check, v float64, passed bool) {
	b.part(string(p), " ", string(c.Metric), " ")
	b.number(v)
	b.text = append(b.text, ' ')
	if !passed {
		b.text = append(b.text, "is not "...)
	}
	b.text = append(b.text, opSymbol(c.Op)...)
	b.text = append(b.text, ' ')
	b.number(c.Threshold)
}

func metricValue(a *Answer, m Metric) (float64, bool) {
	if a == nil {
		return 0, false
	}
	switch m {
	case MetricChoiceProbability:
		if a.Choice == nil {
			return 0, false
		}
		for _, p := range a.Choice.Probabilities {
			if p.Option == a.Choice.Option {
				return p.Probability, true
			}
		}
	case MetricConfidence:
		switch {
		case a.Choice != nil && a.Choice.Confidence != nil:
			return *a.Choice.Confidence, true
		case a.Score != nil && a.Score.Confidence != nil:
			return *a.Score.Confidence, true
		}
	case MetricNoul:
		if a.Noul != nil {
			return a.Noul.Value, true
		}
	case MetricScore:
		if a.Score != nil {
			return a.Score.Value, true
		}
	case MetricAgreement:
	}
	return 0, false
}

func numericValue(a *Answer) float64 {
	if a.Noul != nil {
		return a.Noul.Value
	}
	if a.Score != nil {
		return a.Score.Value
	}
	return 0
}

func compare(v float64, op Op, threshold float64) bool {
	switch op {
	case AtLeast:
		return v >= threshold
	case Above:
		return v > threshold
	case AtMost:
		return v <= threshold
	case Below:
		return v < threshold
	case Equal:
		return v == threshold
	}
	return false
}

func opSymbol(op Op) string {
	switch op {
	case AtLeast:
		return "≥"
	case Above:
		return ">"
	case AtMost:
		return "≤"
	case Below:
		return "<"
	case Equal:
		return "="
	}
	return string(op)
}

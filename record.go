package discern

import (
	"encoding/json"
	"fmt"
	"slices"
)

// ParseResult restores a Result encoded with encoding/json. It returns an error
// matching ErrInvalidRecord for another schema version or a record whose parts are
// not consistent with each other or with the rules a live evaluation follows:
//
//   - outcomes that do not match the providers or the status, or answers that do
//     not match the questions (every observation must pass the checks a live one
//     does, with answers and probabilities in request order, as Evaluate stores
//     them);
//   - a correlation ID or evidence references that a request could not carry, a
//     reported model, request ID or ray ID that is not a provider identifier
//     (empty, or 1 to 256 bytes of visible ASCII), or a negative elapsed time;
//   - recommendations that do not match the policy snapshot: without a policy
//     (source none or disabled) every question is NoRule with exactly the
//     no_policy reason; with one, a question is NoRule with exactly the
//     question_not_in_policy reason when the policy has no entry for it, and is
//     not NoRule and carries neither reason when the policy has one;
//   - an Overall that is not the aggregation of the recommendations (the most
//     severe covered status, and the NoRule questions in request order), or a
//     selection that points at a provider or option the record does not support.
//
// The checks are consistency checks, not a replay: recommendations are not
// recomputed, so records written by other v1 versions of this library still
// parse. Statuses and reason codes it does not know are kept, except where a rule
// above needs a specific value. Unknown fields are ignored.
func ParseResult(data []byte) (*Result, error) {
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, invalid(ErrInvalidRecord, "record", "is not a valid JSON result")
	}
	normalizeRecord(&r)
	if err := validateRecord(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// normalizeRecord replaces absent lists with empty ones so that decoded records
// compare equal to the records Evaluate builds.
func normalizeRecord(r *Result) {
	emptyIfNil := func(s *[]string) {
		if *s == nil {
			*s = []string{}
		}
	}
	emptyIfNil(&r.EvidenceRefs)
	emptyIfNil(&r.Redacted)
	emptyIfNil(&r.Policy.Unmatched)
	emptyIfNil(&r.Overall.Uncovered)
	for i := range r.Recommendations {
		rec := &r.Recommendations[i]
		if rec.Reasons == nil {
			rec.Reasons = []ReasonCode{}
		}
		if rec.Checks == nil {
			rec.Checks = []CheckResult{}
		}
	}
	for i := range r.Outcomes {
		if obs := r.Outcomes[i].Observation; obs != nil && obs.Answers == nil {
			obs.Answers = []Answer{}
		}
	}
}

func recordErr(field, reason string) error { return invalid(ErrInvalidRecord, field, reason) }

// validateRecord checks a result's internal references. Field paths are built
// only for the error that is returned, so a valid record costs no formatting.
func validateRecord(r *Result) error {
	if err := validateEvidence(r); err != nil {
		return err
	}
	if err := validateSnapshot(r); err != nil {
		return err
	}
	if err := validateRecommendations(r); err != nil {
		return err
	}
	return validateOverall(r)
}

// validateEvidence checks the parts of a result that Recommend keeps: everything
// except the policy snapshot, recommendations and overall verdict.
func validateEvidence(r *Result) error {
	if r.SchemaVersion != SchemaVersion {
		return recordErr("schema_version", "unsupported schema version")
	}
	if _, n := r.Providers.list(); n == 0 {
		return recordErr("providers", "unknown provider set")
	}
	if err := validateRecordQuestions(r.Questions); err != nil {
		return err
	}
	if err := checkLabels(ErrInvalidRecord, r.CorrelationID, r.EvidenceRefs); err != nil {
		return err
	}
	if r.Elapsed < 0 {
		return recordErr("elapsed_ns", "must not be negative")
	}
	succeeded, err := validateOutcomes(r)
	if err != nil {
		return err
	}
	switch {
	case succeeded == len(r.Outcomes) && r.Status == Complete,
		succeeded == 0 && r.Status == Failed,
		succeeded > 0 && succeeded < len(r.Outcomes) && r.Status == Partial:
	default:
		return recordErr("status", "does not match the outcomes")
	}
	return validateRedactions(r)
}

// validateRecordQuestions applies the request's rules for question, option and
// level IDs to stored question identities.
func validateRecordQuestions(qs []QuestionInfo) error {
	if len(qs) == 0 {
		return recordErr("questions", "at least one question is required")
	}
	var questions, members idSet
	questions.reset(len(qs))
	for i := range qs {
		q := &qs[i]
		if err := checkQuestionID(ErrInvalidRecord, &questions, i, q.ID); err != nil {
			return err
		}
		var err error
		switch q.Kind {
		case KindChoice:
			if len(q.Levels) > 0 {
				return recordErr(questionPath(i, ""), "a choice question takes only options")
			}
			err = validateMembers(ErrInvalidRecord, &members, i, KindChoice, len(q.Options), func(j int) string { return q.Options[j] }, nil)
		case KindScore:
			if len(q.Options) > 0 {
				return recordErr(questionPath(i, ""), "a score question takes only levels")
			}
			err = validateMembers(ErrInvalidRecord, &members, i, KindScore, len(q.Levels), func(j int) string { return q.Levels[j] }, nil)
		case KindNoul:
			if len(q.Options) > 0 || len(q.Levels) > 0 {
				return recordErr(questionPath(i, ""), "a noul question takes no options or levels")
			}
		default:
			return recordErr(questionPath(i, ".kind"), "must be choice, noul or score")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// validateOutcomes checks that the outcomes match the provider set in fixed order
// and that every observation passes the same rules as a live one, and returns
// the number of successful outcomes.
func validateOutcomes(r *Result) (int, error) {
	ps, n := r.Providers.list()
	if len(r.Outcomes) != n {
		return 0, recordErr("outcomes", "do not match the providers")
	}
	succeeded := 0
	for i := range r.Outcomes {
		o := &r.Outcomes[i]
		if o.Provider != ps[i] {
			return 0, recordErr(fmt.Sprintf("outcomes[%d].provider", i), "does not match the providers")
		}
		if suffix, reason := outcomeProblem(r.Questions, o); reason != "" {
			return 0, recordErr(fmt.Sprintf("outcomes[%d]%s", i, suffix), reason)
		}
		if o.Status == OutcomeSucceeded {
			succeeded++
		}
	}
	return succeeded, nil
}

// outcomeProblem returns why a stored outcome is invalid, as a field suffix and a
// reason, or "" reasons when it is valid.
func outcomeProblem(qs []QuestionInfo, o *Outcome) (suffix, reason string) {
	if o.Elapsed < 0 {
		return ".elapsed_ns", "must not be negative"
	}
	if !validProviderID(o.RequestedModel) {
		return ".requested_model", providerIDReason
	}
	switch o.Status {
	case OutcomeSucceeded:
		if o.Observation == nil || o.Failure != nil {
			return "", "a succeeded outcome has exactly an observation"
		}
		if name := providerIDsProblem(o.Observation); name != "" {
			return ".observation." + name, providerIDReason
		}
		if err := checkObservation(qs, o.Observation); err != nil {
			return ".observation", "answers do not match the questions in request order"
		}
	case OutcomeFailed:
		f := o.Failure
		switch {
		case f == nil || o.Observation != nil || f.Kind == "":
			return "", "a failed outcome has exactly a failure"
		case !validProviderID(f.RequestID):
			return ".failure.request_id", providerIDReason
		case !validProviderID(f.RayID):
			return ".failure.ray_id", providerIDReason
		}
	default:
		return ".status", "unknown outcome status"
	}
	return "", ""
}

func validateRedactions(r *Result) error {
	var seen idSet
	seen.reset(len(r.Redacted))
	for i, name := range r.Redacted {
		if seen.add(name) {
			return recordErr(fmt.Sprintf("redacted[%d]", i), "duplicate redaction")
		}
		present, known := redactedPresent(r, name)
		switch {
		case !known:
			return recordErr(fmt.Sprintf("redacted[%d]", i), "unknown redaction")
		case present:
			return recordErr(fmt.Sprintf("redacted[%d]", i), "a redacted field is present")
		}
	}
	return nil
}

// redactedPresent reports whether a redactable field still has a value, and
// whether the redaction name is known.
func redactedPresent(r *Result, name string) (present, known bool) {
	switch name {
	case redactedCorrelationID:
		return r.CorrelationID != "", true
	case redactedEvidenceRefs:
		return len(r.EvidenceRefs) > 0, true
	case redactedRequestIDs:
		for _, o := range r.Outcomes {
			if obs := o.Observation; obs != nil && (obs.RequestID != "" || obs.RayID != "") {
				return true, true
			}
			if f := o.Failure; f != nil && (f.RequestID != "" || f.RayID != "") {
				return true, true
			}
		}
		return false, true
	case redactedUsage:
		for _, o := range r.Outcomes {
			if o.Observation != nil && o.Observation.Usage != nil {
				return true, true
			}
		}
		return false, true
	}
	return false, false
}

func validateSnapshot(r *Result) error {
	s := r.Policy
	switch s.Source {
	case PolicyNone, PolicyDisabled:
		if s.Policy != nil {
			return recordErr("policy.policy", "must be null for this source")
		}
		return nil
	case PolicyClientDefault, PolicyCallOverride, PolicyRecommend:
		if s.Policy == nil {
			return recordErr("policy.policy", "is required for this source")
		}
	default:
		return recordErr("policy.source", "unknown policy source")
	}
	if err := validatePolicy(s.Policy, Provider.valid); err != nil {
		return recordErr("policy.policy", "is not a valid policy")
	}
	if err := validatePolicyQuestions(s.Policy, r.Questions); err != nil {
		return recordErr("policy.policy", "does not fit the questions")
	}
	return nil
}

func validateRecommendations(r *Result) error {
	if len(r.Recommendations) != len(r.Questions) {
		return recordErr("recommendations", "must have one entry per question")
	}
	for i := range r.Recommendations {
		rec := &r.Recommendations[i]
		if rec.QuestionID != r.Questions[i].ID {
			return recordErr(fmt.Sprintf("recommendations[%d].question_id", i), "does not match the questions")
		}
		if rec.Status == "" {
			return recordErr(fmt.Sprintf("recommendations[%d].status", i), "is required")
		}
		if reason := coverageProblem(r.Policy.Policy, rec); reason != "" {
			return recordErr(fmt.Sprintf("recommendations[%d]", i), reason)
		}
		if rec.Status != Accepted && rec.Selected != nil {
			return recordErr(fmt.Sprintf("recommendations[%d].selected", i), "is set on a recommendation that is not accepted")
		}
		if rec.Status != Accepted {
			continue
		}
		if suffix, reason := selectionProblem(r, i, rec.Selected); reason != "" {
			return recordErr(fmt.Sprintf("recommendations[%d].selected%s", i, suffix), reason)
		}
	}
	return nil
}

// coverageProblem returns why rec does not match the policy coverage of its
// question, or "". Without a policy, every recommendation is NoRule with exactly
// the no_policy reason. With one, a question the policy has no entry for is
// NoRule with exactly the question_not_in_policy reason, and a covered question is
// never NoRule and never carries either reason.
func coverageProblem(pol *Policy, rec *Recommendation) string {
	switch {
	case pol == nil:
		if !noRuleFor(rec, ReasonNoPolicy) {
			return "must be no_rule with exactly the no_policy reason when no policy is in effect"
		}
	case pol.question(rec.QuestionID) == nil:
		if !noRuleFor(rec, ReasonQuestionNotInPolicy) {
			return "must be no_rule with exactly the question_not_in_policy reason when the policy has no entry for the question"
		}
	case rec.Status == NoRule:
		return "must not be no_rule for a question the policy covers"
	default:
		for _, code := range rec.Reasons {
			if code == ReasonNoPolicy || code == ReasonQuestionNotInPolicy {
				return "has a no-rule reason for a question the policy covers"
			}
		}
	}
	return ""
}

// noRuleFor reports whether rec is NoRule with exactly the given reason.
func noRuleFor(rec *Recommendation, reason ReasonCode) bool {
	return rec.Status == NoRule && len(rec.Reasons) == 1 && rec.Reasons[0] == reason
}

// validateOverall checks that Overall is the aggregation overallOf builds from
// the recommendations, without building it: the most severe covered status (or
// NoRule), and the NoRule questions in request order.
func validateOverall(r *Result) error {
	status, uncovered := NoRule, r.Overall.Uncovered
	for i := range r.Recommendations {
		rec := &r.Recommendations[i]
		if rec.Status != NoRule {
			status = overallWith(status, rec.Status)
			continue
		}
		if len(uncovered) == 0 || uncovered[0] != rec.QuestionID {
			return recordErr("overall.uncovered", "must list exactly the no_rule questions in request order")
		}
		uncovered = uncovered[1:]
	}
	if len(uncovered) > 0 {
		return recordErr("overall.uncovered", "must list exactly the no_rule questions in request order")
	}
	if r.Overall.Status != status {
		return recordErr("overall.status", "does not aggregate the recommendations")
	}
	return nil
}

// selectionProblem returns why the selection of an accepted recommendation for
// question i does not point at the record's observations, as a field suffix and
// a reason, or "" reasons when it does. It reads the outcomes in place.
func selectionProblem(r *Result, i int, s *Selected) (suffix, reason string) {
	q := &r.Questions[i]
	if s == nil || len(s.Providers) == 0 {
		return "", "an accepted recommendation selects at least one provider"
	}
	if (q.Kind == KindChoice) != (s.Option != "") {
		return ".option", "is required exactly for choice questions"
	}
	last := -1
	for _, p := range s.Providers {
		idx := providerIndex(r.Providers, p)
		if idx <= last {
			return ".providers", "must be distinct selected providers in fixed order"
		}
		last = idx
		o := r.outcomeFor(p)
		if o == nil || o.Observation == nil {
			return ".providers", "selects a provider without an observation"
		}
		if q.Kind == KindChoice {
			a := o.Observation.answerFor(i, q.ID)
			if a == nil || a.Choice == nil || a.Choice.Option != s.Option {
				return ".option", "does not match the selected provider's choice"
			}
		}
	}
	return "", ""
}

// providerIndex returns p's position in the set, or -1.
func providerIndex(s ProviderSet, p Provider) int {
	ps, n := s.list()
	return slices.Index(ps[:n], p)
}

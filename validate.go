package discern

import (
	"fmt"
	"slices"
	"unicode"
	"unicode/utf8"

	"github.com/mrz1836/go-discern/internal/ident"
)

// Documented library limits. Some are narrower than a provider contract so that a
// request valid for one provider is valid for both, and records stay bounded.
const (
	// MaxQuestionIDLength is the longest question ID (Clef's documented limit).
	MaxQuestionIDLength = 100

	// MinChoiceOptions and MaxChoiceOptions bound a Choice question's options.
	MinChoiceOptions = 2
	MaxChoiceOptions = 255

	// MinScoreLevels and MaxScoreLevels bound a Score question's levels.
	MinScoreLevels = 2
	MaxScoreLevels = 10

	// MaxIdentifierLength bounds option IDs, level IDs, the correlation ID, each
	// evidence reference, check IDs, check models and the policy version, in
	// bytes. These identifiers must be valid UTF-8 without control or invisible
	// format characters (Unicode categories Cc and Cf, such as U+200B and U+202E).
	// They are compared byte for byte, without Unicode normalization, so two IDs
	// that look the same but are encoded differently are different IDs.
	MaxIdentifierLength = 256

	// MaxEvidenceRefs bounds Request.EvidenceRefs.
	MaxEvidenceRefs = 32
)

// identifierProblem returns why s is not a valid identifier, or "" if it is: it
// must be non-empty, at most MaxIdentifierLength bytes of valid UTF-8, and free of
// control characters and of invisible format characters (Unicode category Cf),
// which can reorder or hide text where the identifier is displayed.
func identifierProblem(s string) string {
	switch {
	case s == "":
		return "must not be empty"
	case len(s) > MaxIdentifierLength:
		return fmt.Sprintf("must be at most %d bytes", MaxIdentifierLength)
	case !utf8.ValidString(s):
		return "must be valid UTF-8"
	}
	for _, r := range s {
		switch {
		case unicode.IsControl(r):
			return "must not contain control characters"
		case r >= utf8.RuneSelf && unicode.Is(unicode.Cf, r):
			return "must not contain invisible format characters"
		}
	}
	return ""
}

// providerIDReason states the provider identifier rule in validation errors.
const providerIDReason = "must be at most 256 bytes of visible ASCII"

// validProviderID reports whether s is a valid provider identifier: a model name,
// request ID or ray ID that a provider reported. It is either absent (empty) or 1
// to 256 bytes of visible ASCII (0x21 to 0x7E), which keeps provider-echoed text,
// such as line breaks or request content, out of records and summaries.
func validProviderID(s string) bool { return ident.Visible(s, ident.MaxProviderID) }

// providerID returns s when it is a valid provider identifier, and "" otherwise.
func providerID(s string) string {
	if validProviderID(s) {
		return s
	}
	return ""
}

// validateRequest checks everything that does not depend on a provider. Field
// paths are built only for the error that is returned, so a valid request costs
// no formatting.
func validateRequest(r *Request) error {
	switch r.Providers {
	case JevOnly, ClefOnly, JevAndClef:
	case "":
		return invalid(ErrInvalidRequest, "providers", "must be set")
	default:
		return invalid(ErrInvalidRequest, "providers", "unknown provider set")
	}

	// A blank state is checked last, so an invalid or mistyped state reports
	// that first. Without content, every answer would be billed and meaningless.
	switch t, blank := r.State.check(); {
	case t == jsonInvalid:
		return invalid(ErrInvalidRequest, "state", invalidJSONReason)
	case t != jsonString && t != jsonObject && t != jsonArray:
		return invalid(ErrInvalidRequest, "state", "must be a string, object or array")
	case blank:
		return invalid(ErrInvalidRequest, "state", "must not be empty")
	}

	if len(r.Questions) == 0 {
		return invalid(ErrInvalidRequest, "questions", "at least one question is required")
	}
	var questions, members idSet
	questions.reset(len(r.Questions))
	for i := range r.Questions {
		q := &r.Questions[i]
		if err := checkQuestionID(ErrInvalidRequest, &questions, i, q.ID); err != nil {
			return err
		}
		if err := validateQuestion(q, i, &members); err != nil {
			return err
		}
	}

	return checkLabels(ErrInvalidRequest, r.CorrelationID, r.EvidenceRefs)
}

// checkLabels checks the optional correlation ID and evidence references.
// Requests and stored records share the rules; sentinel says which failed.
func checkLabels(sentinel error, correlationID string, refs []string) error {
	if correlationID != "" {
		if p := identifierProblem(correlationID); p != "" {
			return invalid(sentinel, "correlation_id", p)
		}
	}
	if len(refs) > MaxEvidenceRefs {
		return invalid(sentinel, "evidence_refs", fmt.Sprintf("at most %d references are allowed", MaxEvidenceRefs))
	}
	for i, ref := range refs {
		if p := identifierProblem(ref); p != "" {
			return invalid(sentinel, fmt.Sprintf("evidence_refs[%d]", i), p)
		}
	}
	return nil
}

// questionPath returns "questions[i]" followed by suffix.
func questionPath(i int, suffix string) string { return fmt.Sprintf("questions[%d]%s", i, suffix) }

// memberPath returns "questions[i].<group>[j]" followed by suffix.
func memberPath(i int, group string, j int, suffix string) string {
	return fmt.Sprintf("questions[%d].%s[%d]%s", i, group, j, suffix)
}

// checkQuestionID checks the ID of question i and that no earlier question has
// it. Requests and stored records share the rule; sentinel says which failed.
func checkQuestionID(sentinel error, seen *idSet, i int, id string) error {
	if !ident.Name(id, MaxQuestionIDLength) {
		return invalid(sentinel, questionPath(i, ".id"), "must match [A-Za-z0-9_.-]{1,100}")
	}
	if seen.add(id) {
		return invalid(sentinel, questionPath(i, ".id"), "duplicate question ID")
	}
	return nil
}

// validateQuestion checks question i; members is scratch space for its option or
// level IDs.
func validateQuestion(q *Question, i int, members *idSet) error {
	switch t, blank := q.Instructions.check(); {
	case t == jsonInvalid:
		return invalid(ErrInvalidRequest, questionPath(i, ".instructions"), invalidJSONReason)
	case t != jsonString && t != jsonObject && t != jsonArray:
		return invalid(ErrInvalidRequest, questionPath(i, ".instructions"), "must be text, an object or an array")
	case blank:
		return invalid(ErrInvalidRequest, questionPath(i, ".instructions"), "must not be empty")
	}
	switch q.Kind {
	case KindChoice:
		if len(q.Levels) > 0 || !q.Yes.IsZero() || !q.No.IsZero() {
			return invalid(ErrInvalidRequest, questionPath(i, ""), "a choice question takes only options")
		}
		return validateMembers(ErrInvalidRequest, members, i, KindChoice, len(q.Options),
			func(j int) string { return q.Options[j].ID },
			func(j int) string { return descriptionProblem(q.Options[j].Description, true) })
	case KindScore:
		if len(q.Options) > 0 || !q.Yes.IsZero() || !q.No.IsZero() {
			return invalid(ErrInvalidRequest, questionPath(i, ""), "a score question takes only levels")
		}
		return validateMembers(ErrInvalidRequest, members, i, KindScore, len(q.Levels),
			func(j int) string { return q.Levels[j].ID },
			func(j int) string { return descriptionProblem(q.Levels[j].Description, false) })
	case KindNoul:
		return validateNoul(q, i)
	}
	return invalid(ErrInvalidRequest, questionPath(i, ".kind"), "must be choice, noul or score")
}

// validateMembers checks the count, IDs and uniqueness of the n options (for a
// Choice question i) or levels (for a Score question i); id returns member j's
// ID. describe, when not nil, returns why member j's description is invalid, or
// "". seen is reset and reused, so a request or record builds at most one set of
// member IDs. Requests and stored records share these rules; sentinel says which
// failed.
func validateMembers(sentinel error, seen *idSet, i int, kind Kind, n int, id, describe func(j int) string) error {
	group, minN, maxN := "options", MinChoiceOptions, MaxChoiceOptions
	if kind == KindScore {
		group, minN, maxN = "levels", MinScoreLevels, MaxScoreLevels
	}
	if n < minN || n > maxN {
		return invalid(sentinel, questionPath(i, "."+group), fmt.Sprintf("must have %d to %d %s", minN, maxN, group))
	}
	seen.reset(n)
	for j := range n {
		mid := id(j)
		if p := identifierProblem(mid); p != "" {
			return invalid(sentinel, memberPath(i, group, j, ".id"), p)
		}
		if seen.add(mid) {
			return invalid(sentinel, memberPath(i, group, j, ".id"), "duplicate ID within the question")
		}
		if describe != nil {
			if p := describe(j); p != "" {
				return invalid(sentinel, memberPath(i, group, j, ".description"), p)
			}
		}
	}
	return nil
}

func validateNoul(q *Question, i int) error {
	if len(q.Options) > 0 || len(q.Levels) > 0 {
		return invalid(ErrInvalidRequest, questionPath(i, ""), "a noul question takes no options or levels")
	}
	if !q.Yes.IsZero() {
		if p := descriptionProblem(q.Yes, false); p != "" {
			return invalid(ErrInvalidRequest, questionPath(i, ".yes"), p)
		}
	}
	if !q.No.IsZero() {
		if p := descriptionProblem(q.No, false); p != "" {
			return invalid(ErrInvalidRequest, questionPath(i, ".no"), p)
		}
	}
	return nil
}

// descriptionProblem returns why v is not a valid description, or "".
func descriptionProblem(v Value, nullable bool) string {
	switch t, _ := v.check(); t {
	case jsonString, jsonObject, jsonArray:
		return ""
	case jsonNull:
		if nullable {
			return ""
		}
		return "is required"
	case jsonInvalid:
		return invalidJSONReason
	case jsonScalar:
	}
	return "must be text, an object or an array"
}

// smallIDSet is how many IDs an idSet compares in place before switching to a map.
const smallIDSet = 16

// idSet detects repeated IDs. The first smallIDSet IDs are compared in place, and
// only longer lists use a map, so checking a typical request or record for
// duplicates allocates nothing. The zero value is empty; reset reuses the set,
// and any map it built, for another list.
type idSet struct {
	small [smallIDSet]string
	n     int
	large map[string]struct{}
}

// reset empties the set and prepares it for n IDs.
func (s *idSet) reset(n int) {
	s.n = 0
	switch {
	case s.large != nil:
		clear(s.large)
	case n > len(s.small):
		s.large = make(map[string]struct{}, n)
	}
}

// add records id and reports whether it was already present.
func (s *idSet) add(id string) bool {
	if s.large == nil {
		if slices.Contains(s.small[:s.n], id) {
			return true
		}
		if s.n < len(s.small) {
			s.small[s.n] = id
			s.n++
			return false
		}
		s.large = make(map[string]struct{}, 2*len(s.small))
		for _, seen := range s.small {
			s.large[seen] = struct{}{}
		}
	}
	if _, dup := s.large[id]; dup {
		return true
	}
	s.large[id] = struct{}{}
	return false
}

// questionInfos returns the storable question identities. All option and level
// IDs share one backing array.
func questionInfos(qs []Question) []QuestionInfo {
	total := 0
	for i := range qs {
		total += len(qs[i].Options) + len(qs[i].Levels)
	}
	pool := make([]string, 0, total)
	out := make([]QuestionInfo, len(qs))
	for i := range qs {
		q := &qs[i]
		info := QuestionInfo{ID: q.ID, Kind: q.Kind}
		if len(q.Options) > 0 {
			info.Options = appendIDs(&pool, len(q.Options), func(j int) string { return q.Options[j].ID })
		}
		if len(q.Levels) > 0 {
			info.Levels = appendIDs(&pool, len(q.Levels), func(j int) string { return q.Levels[j].ID })
		}
		out[i] = info
	}
	return out
}

// appendIDs appends n IDs to *pool and returns them as a slice capped to its own
// length, like appendCapped.
func appendIDs(pool *[]string, n int, id func(j int) string) []string {
	start := len(*pool)
	for j := range n {
		*pool = append(*pool, id(j))
	}
	return (*pool)[start:len(*pool):len(*pool)]
}

package discern

import "slices"

// Provider identifies a System One provider.
type Provider string

// Providers.
const (
	// Jev is TypeSafe's hosted Jev API.
	Jev Provider = "jev"

	// Clef is Cloudflare Workers AI's Clef model family.
	Clef Provider = "clef"
)

// valid reports whether p is a known provider.
func (p Provider) valid() bool { return p == Jev || p == Clef }

// ProviderSet selects the providers a request runs on. The zero value is invalid,
// so every request must choose explicitly.
type ProviderSet string

// Provider sets.
const (
	// JevOnly runs the request on Jev.
	JevOnly ProviderSet = "jev"

	// ClefOnly runs the request on Clef.
	ClefOnly ProviderSet = "clef"

	// JevAndClef runs the request on both providers concurrently: two potentially
	// billable calls.
	JevAndClef ProviderSet = "jev+clef"
)

// Providers returns the selected providers in fixed order (Jev, then Clef), or nil
// for an invalid set.
func (s ProviderSet) Providers() []Provider {
	ps, n := s.list()
	if n == 0 {
		return nil
	}
	return append([]Provider(nil), ps[:n]...)
}

// Has reports whether the set selects p.
func (s ProviderSet) Has(p Provider) bool {
	ps, n := s.list()
	return slices.Contains(ps[:n], p)
}

// list returns the selected providers in fixed order without allocating; n is 0
// for an invalid set.
func (s ProviderSet) list() (ps [2]Provider, n int) {
	switch s {
	case JevOnly:
		return [2]Provider{Jev}, 1
	case ClefOnly:
		return [2]Provider{Clef}, 1
	case JevAndClef:
		return [2]Provider{Jev, Clef}, 2
	}
	return ps, 0
}

// Kind is a question type.
type Kind string

// Question kinds.
const (
	// KindChoice picks one option from a caller-defined list.
	KindChoice Kind = "choice"

	// KindNoul returns the probability that the answer is yes.
	KindNoul Kind = "noul"

	// KindScore rates the state on an ordered list of levels.
	KindScore Kind = "score"
)

// Request is one evaluation: a state and the questions asked about it.
//
// Evaluate copies the request on entry; callers must still not mutate it from
// another goroutine while a call is in progress.
type Request struct {
	// Providers selects the providers. It is required.
	Providers ProviderSet

	// State is the content to evaluate: a string, object or array. It must not
	// be blank: text of only white space, and an empty object or array, are
	// rejected before any call.
	State Value

	// Questions are evaluated independently against State.
	Questions []Question

	// CorrelationID is an optional caller label stored verbatim in the result.
	CorrelationID string

	// EvidenceRefs are optional opaque references stored verbatim in the result.
	// They are never fetched or validated beyond their size.
	EvidenceRefs []string
}

// Question is one typed question. Build it with Choice, Noul or Score.
type Question struct {
	// ID identifies the question; it must match [A-Za-z0-9_.-]{1,100}.
	ID string

	// Kind is the question type.
	Kind Kind

	// Instructions say what to decide: non-empty text, or a JSON object or array.
	Instructions Value

	// Options are the Choice alternatives, in the order sent to the provider.
	// Order matters: models can lean toward earlier options.
	Options []ChoiceOption

	// Levels are the Score levels, lowest first.
	Levels []ScoreLevel

	// Yes and No optionally describe what a yes and a no mean for a Noul question.
	Yes, No Value
}

// ChoiceOption is one Choice alternative. ID is sent to the provider as the option
// name; Description may be the zero Value (sent as null).
type ChoiceOption struct {
	ID          string
	Description Value
}

// ScoreLevel is one Score level. ID is local: the provider sees only the level's
// position. Description is required.
type ScoreLevel struct {
	ID          string
	Description Value
}

// Choice returns a Choice question with the options in the given order.
func Choice(id string, instructions Value, options ...ChoiceOption) Question {
	return Question{ID: id, Kind: KindChoice, Instructions: instructions, Options: append([]ChoiceOption(nil), options...)}
}

// Noul returns a Noul (yes/no probability) question. Set Yes and No on the result
// to describe the two ends.
func Noul(id string, instructions Value) Question {
	return Question{ID: id, Kind: KindNoul, Instructions: instructions}
}

// Score returns a Score question with the levels lowest first.
func Score(id string, instructions Value, levels ...ScoreLevel) Question {
	return Question{ID: id, Kind: KindScore, Instructions: instructions, Levels: append([]ScoreLevel(nil), levels...)}
}

// Opt returns a Choice option.
func Opt(id string, description Value) ChoiceOption {
	return ChoiceOption{ID: id, Description: description}
}

// Level returns a Score level.
func Level(id string, description Value) ScoreLevel {
	return ScoreLevel{ID: id, Description: description}
}

// clone returns a deep copy. Values are immutable, so they are shared. All
// options share one backing array, as do all levels.
func (r *Request) clone() *Request {
	c := *r
	c.EvidenceRefs = slices.Clone(r.EvidenceRefs)
	if r.Questions != nil {
		options, levels := 0, 0
		for i := range r.Questions {
			options += len(r.Questions[i].Options)
			levels += len(r.Questions[i].Levels)
		}
		optionPool := make([]ChoiceOption, 0, options)
		levelPool := make([]ScoreLevel, 0, levels)
		c.Questions = make([]Question, len(r.Questions))
		for i, q := range r.Questions {
			q.Options = appendCapped(&optionPool, q.Options)
			q.Levels = appendCapped(&levelPool, q.Levels)
			c.Questions[i] = q
		}
	}
	return &c
}

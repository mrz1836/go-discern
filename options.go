package discern

// DefaultBatchConcurrency is the number of requests EvaluateBatch runs at once
// unless WithBatchConcurrency says otherwise.
const DefaultBatchConcurrency = 4

// Option configures an Evaluator in New. Nil options are ignored, and the last
// option of each kind wins, except that registering a second adapter for the same
// provider is an error.
type Option func(*config)

type config struct {
	adapters []Adapter
	policy   *Policy
	batch    int
}

// WithAdapter registers a provider adapter, such as one from jev.New or clef.New.
func WithAdapter(a Adapter) Option {
	return func(c *config) { c.adapters = append(c.adapters, a) }
}

// WithDefaultPolicy sets the policy applied to every evaluation that does not
// override it. The policy is copied now and validated in New.
func WithDefaultPolicy(p Policy) Option {
	cp := p.clone()
	return func(c *config) { c.policy = cp }
}

// WithBatchConcurrency sets how many requests EvaluateBatch runs at once. A dual
// request makes two concurrent calls, so up to 2n calls can be in flight. n must be
// at least 1.
func WithBatchConcurrency(n int) Option {
	return func(c *config) { c.batch = n }
}

// EvalOption configures one Evaluate or EvaluateBatch call. It never changes the
// Evaluator. The last policy option wins.
type EvalOption func(*evalConfig)

type policyMode uint8

const (
	policyInherit policyMode = iota
	policyOverride
	policyDisable
)

type evalConfig struct {
	mode   policyMode
	policy *Policy
}

// WithPolicy replaces the effective policy for this call only. The policy is copied
// now; the evaluator's default is untouched.
func WithPolicy(p Policy) EvalOption {
	cp := p.clone()
	return func(c *evalConfig) { c.mode, c.policy = policyOverride, cp }
}

// WithoutPolicy disables the default policy for this call: the result holds
// observations and NoRule recommendations only.
func WithoutPolicy() EvalOption {
	return func(c *evalConfig) { c.mode, c.policy = policyDisable, nil }
}

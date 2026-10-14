package discern

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"sync"
	"time"
)

// Evaluator runs requests on registered adapters and applies recommendation
// policies. It is immutable after New and safe for concurrent use.
type Evaluator struct {
	adapters map[Provider]Adapter
	policy   *Policy
	batch    int
	library  Library
}

// New returns an Evaluator. It returns an error matching ErrInvalidConfig when no
// adapter is registered, an adapter is nil (including a typed nil pointer, such as
// a nil *jev.Client, whose Provider or Model panics) or duplicates a provider, or
// the batch concurrency is below 1, and one matching ErrInvalidPolicy when the
// default policy is invalid.
func New(opts ...Option) (*Evaluator, error) {
	cfg := config{batch: DefaultBatchConcurrency}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	if len(cfg.adapters) == 0 {
		return nil, invalid(ErrInvalidConfig, "adapters", "at least one adapter is required")
	}
	e := &Evaluator{adapters: make(map[Provider]Adapter, len(cfg.adapters)), batch: cfg.batch, library: libraryInfo()}
	for i, a := range cfg.adapters {
		field := fmt.Sprintf("adapters[%d]", i)
		if a == nil {
			return nil, invalid(ErrInvalidConfig, field, "is nil")
		}
		p, model, ok := adapterProvider(a)
		if !ok {
			return nil, invalid(ErrInvalidConfig, field, "is a nil adapter")
		}
		if !validProviderID(model) {
			// The requested model is recorded and quoted in summaries.
			return nil, invalid(ErrInvalidConfig, field+".model", providerIDReason)
		}
		if !p.valid() {
			return nil, invalid(ErrInvalidConfig, field, "unknown provider")
		}
		if _, dup := e.adapters[p]; dup {
			return nil, invalid(ErrInvalidConfig, field, "duplicate provider")
		}
		e.adapters[p] = a
	}
	if cfg.batch < 1 {
		return nil, invalid(ErrInvalidConfig, "batch_concurrency", "must be at least 1")
	}
	if cfg.policy != nil {
		p := normalizePolicy(cfg.policy)
		if err := validatePolicy(p, e.registered); err != nil {
			return nil, err
		}
		e.policy = p
	}
	return e, nil
}

// adapterProvider returns the adapter's provider and requested model, with ok
// false when Provider or Model panics. A typed nil pointer, such as a nil *jev.Client or a nil
// *discerntest.Fake, is a non-nil Adapter whose methods panic when called, so
// New rejects it rather than letting the first evaluation panic.
func adapterProvider(a Adapter) (p Provider, model string, ok bool) {
	defer func() {
		if recover() != nil {
			p, model, ok = "", "", false
		}
	}()
	return a.Provider(), a.Model(), true
}

// Evaluate validates the request and the effective policy, prepares every selected
// adapter, and then makes one call per selected provider; a dual request runs both
// calls concurrently and waits for both. One provider's failure never cancels the
// other.
//
// The returned Result is non-nil exactly when at least one call was attempted;
// persist it before inspecting err. err is nil when every provider succeeded
// (Result.Status Complete); it matches ErrPartialFailure when one of two failed
// (Partial); otherwise it joins the provider errors (Failed). When no call was made,
// err matches ErrInvalidRequest, ErrUnsupported or ErrInvalidPolicy, or is the
// context's error.
func (e *Evaluator) Evaluate(ctx context.Context, req Request, opts ...EvalOption) (*Result, error) {
	p, err := e.prepare(&req, evalConfigOf(opts))
	if err != nil {
		return nil, err
	}
	return p.start(ctx)
}

// EvaluateBatch evaluates independent requests with at most WithBatchConcurrency
// requests in flight. Every item is validated and prepared first: if any item is
// invalid, it returns an error naming the index and makes no call at all.
// Otherwise each item follows Evaluate's contract and the items are returned in
// input order. Items are billed separately and never deduplicated. When ctx is
// done, items not yet started get Result nil and the context's error.
//
// If an adapter panics, no further item is started; the panic is re-raised once
// the items in flight have finished.
//
// Prepared requests (including each adapter's encoded body) are held until their
// item runs, so peak memory is roughly the size of the batch's input.
func (e *Evaluator) EvaluateBatch(ctx context.Context, reqs []Request, opts ...EvalOption) ([]BatchItem, error) {
	ec := evalConfigOf(opts)
	preps := make([]*prepared, len(reqs))
	for i := range reqs {
		p, err := e.prepare(&reqs[i], ec)
		if err != nil {
			return nil, fmt.Errorf("discern: batch item %d: %w", i, err)
		}
		preps[i] = p
	}
	items := make([]BatchItem, len(reqs))
	if len(reqs) == 0 {
		return items, nil
	}

	next := make(chan int)
	var (
		wg     sync.WaitGroup
		panics panicBox
	)
	for range min(e.batch, len(reqs)) {
		wg.Go(func() {
			for i := range next {
				// An item received after a panic was recorded is skipped: the
				// batch re-raises the panic, so it must not make the call.
				if !panics.recorded() {
					panics.run(func() {
						res, err := preps[i].start(ctx)
						items[i] = BatchItem{Result: res, Err: err}
					})
				}
				preps[i] = nil // release the encoded request as soon as the item is done
			}
		})
	}
	for i := range reqs {
		if panics.recorded() {
			break
		}
		next <- i
	}
	close(next)
	wg.Wait()
	panics.repanic()
	return items, nil
}

// panicBox keeps the first panic from concurrent work so it can be re-raised after
// all goroutines finish.
type panicBox struct {
	mu  sync.Mutex
	set bool
	val any
}

func (b *panicBox) run(f func()) {
	defer func() {
		if v := recover(); v != nil {
			b.mu.Lock()
			if !b.set {
				b.set, b.val = true, v
			}
			b.mu.Unlock()
		}
	}()
	f()
}

// recorded reports whether a panic has been kept.
func (b *panicBox) recorded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.set
}

func (b *panicBox) repanic() {
	if b.set {
		panic(b.val)
	}
}

func (e *Evaluator) registered(p Provider) bool {
	_, ok := e.adapters[p]
	return ok
}

func evalConfigOf(opts []EvalOption) evalConfig {
	if len(opts) == 0 {
		return evalConfig{} // the options take a pointer, which would move ec to the heap
	}
	ec := new(evalConfig)
	for _, o := range opts {
		if o != nil {
			o(ec)
		}
	}
	return *ec
}

// prepared is a validated request with one encoded call per selected provider.
// It keeps only the request fields a Result records.
type prepared struct {
	library       Library
	providers     ProviderSet
	correlationID string
	evidenceRefs  []string // the prepared request's own copy, never shared with an adapter
	questions     []QuestionInfo
	snapshot      PolicySnapshot
	calls         []preparedCall
	callBuf       [2]preparedCall // backs calls: at most two providers
}

type preparedCall struct {
	provider Provider
	model    string
	call     Call
}

func (e *Evaluator) prepare(req *Request, ec evalConfig) (*prepared, error) {
	r := req.clone()
	if err := validateRequest(r); err != nil {
		return nil, err
	}
	providers, n := r.Providers.list()
	for _, p := range providers[:n] {
		if !e.registered(p) {
			return nil, invalid(ErrUnsupported, "providers", "no adapter is registered for "+string(p))
		}
	}
	questions := questionInfos(r.Questions)
	snap, err := e.snapshot(ec, questions)
	if err != nil {
		return nil, err
	}

	refs := slices.Clone(r.EvidenceRefs)
	if refs == nil {
		refs = []string{}
	}
	p := &prepared{
		library: e.library, providers: r.Providers, correlationID: r.CorrelationID, evidenceRefs: refs,
		questions: questions, snapshot: snap,
	}
	if err := p.addCalls(e, r, providers[:n]); err != nil {
		return nil, err
	}
	return p, nil
}

// snapshot returns the policy an evaluation applies: none, the client default,
// the call's override or disabled. An override is validated, and any policy
// must fit the questions.
func (e *Evaluator) snapshot(ec evalConfig, questions []QuestionInfo) (PolicySnapshot, error) {
	var snap PolicySnapshot
	switch ec.mode {
	case policyInherit:
		snap.Source = PolicyNone
		if e.policy != nil {
			snap = PolicySnapshot{Source: PolicyClientDefault, Policy: e.policy.clone()}
		}
	case policyOverride:
		p := normalizePolicy(ec.policy)
		if err := validatePolicy(p, e.registered); err != nil {
			return PolicySnapshot{}, err
		}
		snap = PolicySnapshot{Source: PolicyCallOverride, Policy: p}
	case policyDisable:
		snap.Source = PolicyDisabled
	}
	if snap.Policy != nil {
		if err := validatePolicyQuestions(snap.Policy, questions); err != nil {
			return PolicySnapshot{}, err
		}
	}
	return snap, nil
}

// addCalls has each provider's adapter prepare its call, in the set's order,
// and stops at the first adapter that fails.
func (p *prepared) addCalls(e *Evaluator, r *Request, providers []Provider) error {
	p.calls = p.callBuf[:0]
	for k, prov := range providers {
		a := e.adapters[prov]
		// Every adapter owns its request. The validated copy goes to the last one,
		// so a request is copied once per selected provider.
		own := r
		if k < len(providers)-1 {
			own = r.clone()
		}
		call, err := a.Prepare(own)
		switch {
		case err != nil && !errors.Is(err, ErrInvalidRequest) && !errors.Is(err, ErrUnsupported):
			return &prepareError{provider: prov, err: err}
		case err != nil:
			return err
		case call == nil:
			return invalid(ErrUnsupported, "providers", "the "+string(prov)+" adapter prepared no call")
		}
		p.calls = append(p.calls, preparedCall{provider: prov, model: a.Model(), call: call})
	}
	return nil
}

// prepareError reports an adapter's Prepare error that matches neither
// ErrInvalidRequest nor ErrUnsupported. Its text is fixed, since a third-party
// adapter's message may contain caller data; it matches ErrUnsupported, and the
// adapter's error stays reachable through errors.Is and errors.As.
type prepareError struct {
	provider Provider
	err      error
}

// Error returns "discern: unsupported by provider: the <provider> adapter
// rejected the request".
func (e *prepareError) Error() string {
	return ErrUnsupported.Error() + ": the " + string(e.provider) + " adapter rejected the request"
}

// Unwrap returns ErrUnsupported and the adapter's error.
func (e *prepareError) Unwrap() []error { return []error{ErrUnsupported, e.err} }

// start makes the prepared calls unless ctx is already done.
func (p *prepared) start(ctx context.Context) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	started := time.Now()
	outcomes, failures := p.dispatch(ctx)
	res := &Result{
		SchemaVersion: SchemaVersion,
		Library:       p.library,
		CorrelationID: p.correlationID,
		EvidenceRefs:  p.evidenceRefs,
		Redacted:      []string{},
		Providers:     p.providers,
		StartedAt:     started.UTC(),
		Elapsed:       time.Since(started),
		Questions:     p.questions,
		Outcomes:      outcomes,
		Policy:        p.snapshot,
	}
	applyStatus(res, len(failures))
	applyPolicy(res)
	return res, callError(failures, len(p.calls))
}

// dispatch makes the calls, the two of a dual request concurrently, and returns
// their outcomes and failures in call order. A panic in a call is re-raised once
// every call has returned.
func (p *prepared) dispatch(ctx context.Context) ([]Outcome, []error) {
	outcomes := make([]Outcome, len(p.calls))
	errs := make([]*ProviderError, len(p.calls))
	if len(p.calls) == 1 {
		outcomes[0], errs[0] = p.invoke(ctx, p.calls[0])
	} else {
		var (
			wg     sync.WaitGroup
			panics panicBox
		)
		for i := range p.calls {
			wg.Go(func() {
				panics.run(func() { outcomes[i], errs[i] = p.invoke(ctx, p.calls[i]) })
			})
		}
		wg.Wait()
		panics.repanic()
	}
	var failures []error
	for _, pe := range errs {
		if pe != nil { // a nil *ProviderError must not become a non-nil error
			failures = append(failures, pe)
		}
	}
	return outcomes, failures
}

// callError returns Evaluate's error for the failures of an evaluation's calls:
// nil when there are none, ErrPartialFailure wrapping the first when another
// call succeeded, the failure itself for a single call, and all of them joined
// otherwise.
func callError(failures []error, calls int) error {
	switch {
	case len(failures) == 0:
		return nil
	case len(failures) < calls:
		return fmt.Errorf("%w: %w", ErrPartialFailure, failures[0])
	case len(failures) == 1:
		return failures[0]
	}
	return errors.Join(failures...)
}

func applyStatus(r *Result, failures int) {
	switch {
	case failures == 0:
		r.Status = Complete
	case failures < len(r.Outcomes):
		r.Status = Partial
	default:
		r.Status = Failed
	}
}

// invoke makes one call and validates its observation.
func (p *prepared) invoke(ctx context.Context, pc preparedCall) (Outcome, *ProviderError) {
	start := time.Now()
	obs, err := pc.call(ctx)
	out := Outcome{Provider: pc.provider, RequestedModel: pc.model, Elapsed: time.Since(start)}
	if err == nil {
		norm, verr := normalizeObservation(p.questions, obs)
		if verr == nil {
			out.Status, out.Observation = OutcomeSucceeded, norm
			return out, nil
		}
		pe := NewProviderError(pc.provider, FailureMalformed, verr)
		if obs != nil {
			pe.RequestID, pe.RayID = providerID(obs.RequestID), providerID(obs.RayID)
		}
		err = pe
	}
	pe := classify(ctx, pc.provider, err)
	out.Status, out.Failure = OutcomeFailed, pe.failure()
	return out, pe
}

// classify converts a call error into a *ProviderError for the provider. A
// ProviderError is accepted as a pointer or as a value. A cause that matches a
// local sentinel is replaced by a static one, because those sentinels promise
// that no call was made.
func classify(ctx context.Context, provider Provider, err error) *ProviderError {
	if pe, ok := errors.AsType[*ProviderError](err); ok {
		if pe == nil {
			// A nil *ProviderError inside a non-nil error is an adapter bug. Its
			// value-receiver methods would panic in errors.Is, so it is dropped.
			return NewProviderError(provider, FailureAdapter, errNilProviderError)
		}
		return adopt(provider, pe)
	}
	if v, ok := errors.AsType[ProviderError](err); ok {
		return adopt(provider, &v)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		kind := FailureCanceled
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			kind = FailureDeadlineExceeded
		}
		return NewProviderError(provider, kind, contextCause(err, ctxErr))
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return NewProviderError(provider, FailureTimeout, contextCause(err, context.DeadlineExceeded))
	case errors.Is(err, context.Canceled):
		return NewProviderError(provider, FailureCanceled, contextCause(err, context.Canceled))
	}
	if matchesLocalSentinel(err) {
		err = errCallLocalSentinel
	}
	return NewProviderError(provider, FailureAdapter, err)
}

// adopt returns a copy of an adapter's ProviderError for the provider. The
// adapter's value is never modified, so a shared error stays intact. Request and
// ray IDs that are not provider identifiers are dropped, and a cause that matches
// a local sentinel, or that panics when inspected (as one holding a nil
// *ProviderError does), is replaced by a static one.
func adopt(provider Provider, pe *ProviderError) *ProviderError {
	c := *pe
	c.Provider = provider
	c.Codes = slices.Clone(pe.Codes)
	c.RequestID, c.RayID = providerID(pe.RequestID), providerID(pe.RayID)
	if c.Kind == "" {
		c.Kind = FailureAdapter
	}
	d := pe.detail()
	if static := staticCause(d.cause); static != nil {
		d.cause = static
		c.private = func() providerDetail { return d }
	}
	return &c
}

// staticCause returns the static error that replaces a ProviderError's cause, or
// nil to keep it: a cause that matches a local sentinel, or that panics when
// inspected, must not reach the caller.
func staticCause(cause error) (static error) {
	if cause == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			static = errUninspectableCause
		}
	}()
	if matchesLocalSentinel(cause) {
		return errCallLocalSentinel
	}
	return nil
}

// contextCause returns err when it matches the context error want and no local
// sentinel, and want otherwise.
func contextCause(err, want error) error {
	if errors.Is(err, want) && !matchesLocalSentinel(err) {
		return err
	}
	return want
}

// matchesLocalSentinel reports whether err matches a sentinel that only local
// validation or Evaluate itself reports: ErrInvalidConfig, ErrInvalidRequest,
// ErrInvalidPolicy, ErrUnsupported, ErrInvalidRecord or ErrPartialFailure.
func matchesLocalSentinel(err error) bool {
	for _, sentinel := range [...]error{
		ErrInvalidConfig, ErrInvalidRequest, ErrInvalidPolicy, ErrUnsupported, ErrInvalidRecord, ErrPartialFailure,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

// libraryInfo reads this module's version from the build information.
func libraryInfo() Library {
	lib := Library{Module: modulePath, Version: "unknown"}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return lib
	}
	if bi.Main.Path == modulePath && bi.Main.Version != "" {
		lib.Version = bi.Main.Version
	}
	for _, d := range bi.Deps {
		if d.Path != modulePath {
			continue
		}
		lib.Version = d.Version
		if d.Replace != nil && d.Replace.Version != "" {
			lib.Version = d.Replace.Version
		}
	}
	return lib
}

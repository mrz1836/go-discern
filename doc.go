// Package discern is a small decision layer over two hosted System One judgment
// APIs: TypeSafe Jev (package jev) and Cloudflare Workers AI Clef (package clef).
//
// One typed Request asks one or more independent questions about a state. Each
// question is a Choice (pick one of named options), a Noul (the probability that
// the answer is yes) or a Score (a probability-weighted position on ordered
// levels). The caller picks the provider for every request: Jev, Clef, or an
// explicit run on both.
//
//	jc, err := jev.New(apiKey, jev.WithModel("jev-1.13.0"))
//	cc, err := clef.New(accountID, apiToken, clef.WithModel(clef.ModelFlash))
//	ev, err := discern.New(discern.WithAdapter(jc), discern.WithAdapter(cc))
//
//	res, err := ev.Evaluate(ctx, discern.Request{
//		Providers: discern.ClefOnly,
//		State:     discern.Text(ticketText),
//		Questions: []discern.Question{
//			discern.Choice("queue", discern.Text("Which support queue should handle this ticket?"),
//				discern.Opt("billing", discern.Text("Invoices, refunds, failed payments")),
//				discern.Opt("technical", discern.Text("Errors, outages, integration problems"))),
//		},
//	})
//
// # Observations
//
// A Result always carries every provider's full observation: the chosen option
// and the probability of every option in request order, Noul and Score values,
// confidence when reported, usage, model and request identifiers. Optional
// provider fields are pointers, so an absent value is never read as zero.
//
// Confidence is a concentration statistic computed from the probabilities. It is
// not the probability that an answer is correct, and values from Jev and Clef are
// not comparable. A Noul value is a probability, not a boolean. Neither provider
// returns a rationale; the SDK never invents one.
//
// # Recommendations
//
// A Policy is an explicit set of threshold checks per question, applied locally
// to the observations. Without a policy no recommendation is made: every question
// gets the NoRule status and the observations are returned unchanged. Policies
// come from WithDefaultPolicy (per evaluator) or WithPolicy and WithoutPolicy (per
// call; the last option wins and the evaluator is never modified). Recommend
// re-applies another policy to a stored result without any provider call.
//
// Each question gets one Recommendation with a status (Accepted, BelowThreshold,
// ReviewRequired, NotApplicable, Incomplete or NoRule), machine-readable reason
// codes, every evaluated check, and a deterministic summary. For a dual run the
// providers must agree (same Choice option, or Noul and Score values within the
// policy's Agreement tolerance); disagreement needs review unless the policy names
// a PreferOnConflict provider. When one provider of a dual run fails, questions
// are Incomplete unless the policy sets AllowPartial; with AllowPartial, Evaluate
// can return an error matching ErrPartialFailure together with Accepted
// recommendations for the surviving provider. Result.Overall is Accepted only if
// every question the policy covers is Accepted.
//
// # Calls, billing and retries
//
// Every selected provider gets exactly one attempt. The SDK never retries, falls
// back to another provider, hedges or races requests: each call may be billed, and
// a dual run makes two potentially billable calls, run concurrently. One attempt
// includes the standard library's own transparent replays, which the adapters
// disable: net/http's HTTP/2 client would otherwise resend a request whose stream
// was refused or reset, even after the server read it. Request, policy and
// adapter validation all happen before any call, so a local error means nothing
// was sent. Retry hints from providers are reported in ProviderError and Failure;
// whether repeating a call is safe is the caller's decision. Injected HTTP
// transports that retry or follow redirects on their own are outside these
// guarantees.
//
// The SDK never reads environment variables itself. When no HTTP client is
// injected, the adapters use http.DefaultTransport, which honors the standard
// proxy variables (HTTPS_PROXY, HTTP_PROXY and NO_PROXY) as net/http always does;
// inject a client to control that.
//
// # Errors
//
// Evaluate returns a non-nil Result exactly when at least one call was attempted.
// Persist it before inspecting the error. ErrPartialFailure means one of two
// providers failed and the other's observation is usable; under a policy with
// AllowPartial its recommendations may be Accepted. Local failures match
// ErrInvalidRequest, ErrInvalidPolicy or ErrUnsupported, and the error of a call
// that was made never matches them. Provider failures are *ProviderError values
// that match ErrRejected or ErrMalformedResponse, or wrap context.Canceled and
// context.DeadlineExceeded. Error strings never contain credentials, URLs,
// request state or provider bodies. Tools that walk error chains, such as
// testify's failure messages, also print the causes; the jev and clef adapters
// keep only sanitized causes, which never include credentials, URLs, payloads or
// provider messages.
//
// # Records
//
// A Result is a versioned JSON record (SchemaVersion) that never contains the
// state, instructions, descriptions, credentials or provider bodies. It does
// contain caller identifiers (question, option and level IDs, the correlation ID
// and evidence references) and provider outputs, which may still be sensitive;
// Redact removes the optional identifiers. Provider-reported identifiers (the
// model, request ID and ray ID) are kept only when they are visible ASCII of at
// most 256 bytes, so provider-echoed text cannot reach a record or a summary.
// ParseResult restores a record and checks its internal references and that its
// recommendations and overall verdict are consistent with its policy snapshot. A
// record supports traceability and local policy replay;
// it is not proof that an answer is true, not tamper-evident, and not a promise
// that a repeated call would return the same values.
//
// # Testing
//
// Package discerntest provides fake adapters with call counting, so application
// code can be tested without network access.
package discern

<div align="center">

# 🧭&nbsp;&nbsp;go-discern

**A small, typed Go decision layer for the TypeSafe Jev and Cloudflare Clef System One APIs.**

<br/>

<a href="https://github.com/mrz1836/go-discern/releases"><img src="https://img.shields.io/github/release-pre/mrz1836/go-discern?include_prereleases&style=flat-square&logo=github&color=black" alt="Release"></a>
<a href="https://golang.org/"><img src="https://img.shields.io/github/go-mod/go-version/mrz1836/go-discern?style=flat-square&logo=go&color=00ADD8" alt="Go Version"></a>
<a href="https://github.com/mrz1836/go-discern/blob/master/LICENSE"><img src="https://img.shields.io/github/license/mrz1836/go-discern?style=flat-square&color=blue" alt="License"></a>

<br/>

<table align="center" border="0">
  <tr>
    <td align="right">
       <code>CI / CD</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://github.com/mrz1836/go-discern/actions"><img src="https://img.shields.io/github/actions/workflow/status/mrz1836/go-discern/fortress.yml?branch=master&label=build&logo=github&style=flat-square" alt="Build"></a>
       <a href="https://github.com/mrz1836/go-discern/actions"><img src="https://img.shields.io/github/last-commit/mrz1836/go-discern?style=flat-square&logo=git&logoColor=white&label=last%20update" alt="Last Commit"></a>
    </td>
    <td align="right">
       &nbsp;&nbsp;&nbsp;&nbsp; <code>Quality</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://codecov.io/gh/mrz1836/go-discern"><img src="https://codecov.io/gh/mrz1836/go-discern/branch/master/graph/badge.svg?style=flat-square" alt="Coverage"></a>
    </td>
  </tr>

  <tr>
    <td align="right">
       <code>Security</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://scorecard.dev/viewer/?uri=github.com/mrz1836/go-discern"><img src="https://api.scorecard.dev/projects/github.com/mrz1836/go-discern/badge?style=flat-square" alt="Scorecard"></a>
       <a href=".github/SECURITY.md"><img src="https://img.shields.io/badge/policy-active-success?style=flat-square&logo=security&logoColor=white" alt="Security"></a>
    </td>
    <td align="right">
       &nbsp;&nbsp;&nbsp;&nbsp; <code>Community</code> &nbsp;&nbsp;
    </td>
    <td align="left">
       <a href="https://github.com/mrz1836/go-discern/graphs/contributors"><img src="https://img.shields.io/github/contributors/mrz1836/go-discern?style=flat-square&color=orange" alt="Contributors"></a>
       <a href="https://mrz1818.com/"><img src="https://img.shields.io/badge/donate-bitcoin-ff9900?style=flat-square&logo=bitcoin" alt="Bitcoin"></a>
    </td>
  </tr>
</table>

</div>

<br/>
<br/>

<div align="center">

### <code>Project Navigation</code>

</div>

<table align="center">
  <tr>
    <td align="center" width="20%">
       🚀&nbsp;<a href="#-installation"><code>Installation</code></a>
    </td>
    <td align="center" width="20%">
       🏁&nbsp;<a href="#-quick-start"><code>Quick&nbsp;Start</code></a>
    </td>
    <td align="center" width="20%">
       💡&nbsp;<a href="#-usage"><code>Usage</code></a>
    </td>
    <td align="center" width="20%">
       ⚙️&nbsp;<a href="#%EF%B8%8F-behavior"><code>Behavior</code></a>
    </td>
    <td align="center" width="20%">
       📚&nbsp;<a href="#-documentation"><code>Documentation</code></a>
    </td>
  </tr>
  <tr>
    <td align="center">
       🧪&nbsp;<a href="#-examples--tests"><code>Examples&nbsp;&&nbsp;Tests</code></a>
    </td>
    <td align="center">
      ⚡&nbsp;<a href="#-benchmarks"><code>Benchmarks</code></a>
    </td>
    <td align="center">
      🛠️&nbsp;<a href="#%EF%B8%8F-code-standards"><code>Code&nbsp;Standards</code></a>
    </td>
    <td align="center">
      🤖&nbsp;<a href="#-ai-usage--assistant-guidelines"><code>AI&nbsp;Usage</code></a>
    </td>
    <td align="center">
       🤝&nbsp;<a href="#-contributing"><code>Contributing</code></a>
    </td>
  </tr>
</table>
<br/>

go-discern asks typed questions about a piece of text or JSON (pick one of your options, give the
probability of *yes*, or rate on your scale) and gets the answers from TypeSafe's
[Jev](https://docs.typesafe.ai/introduction) API, Cloudflare Workers AI's
[Clef](https://developers.cloudflare.com/workers-ai/models/clef/) models, or both at once. Both follow
TypeSafe's "System One" API: the models return probabilities, not text. Use go-discern when a Go service
needs a fast classification or triage decision, such as routing a support ticket, backed by real
probabilities, your own thresholds, and a record you can store.

<br/>

## 📦 Installation

**go-discern** requires a [supported release of Go](https://golang.org/doc/devel/release.html#policy) (Go 1.26 or newer).
```shell script
go get github.com/mrz1836/go-discern
```

The library has **zero third-party runtime dependencies**: the core, both adapters and `discerntest`
use only the standard library. `testify` is used only by the tests and their internal helper package.

<br/>

### Credentials

You need credentials only for the providers you call. go-discern never reads environment variables or
configuration files itself: your code passes the values to `jev.New` and `clef.New`.

| Provider | You need | Where to get it |
|---|---|---|
| Jev | A TypeSafe API key | Your TypeSafe account; see [TypeSafe's documentation](https://docs.typesafe.ai/introduction) |
| Clef | Your Cloudflare account ID (32 hexadecimal characters) and an API token that can use Workers AI | The Cloudflare dashboard: **Workers AI → Use REST API** shows the account ID and can create a token with the right permissions |

The examples below read them from `TYPESAFE_API_KEY`, `CLOUDFLARE_ACCOUNT_ID` and
`CLOUDFLARE_API_TOKEN`, the names the [example program](examples) and the
[live tests](#live-contract-tests) use. Any names work.

<br/>

## 🏁 Quick Start

Save this as `main.go` in a new module (`go mod init example.com/triage`, then
`go get github.com/mrz1836/go-discern`), set `TYPESAFE_API_KEY`, and run `go run .`. It makes one
billable call to Jev.

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"
    "time"

    "github.com/mrz1836/go-discern"
    "github.com/mrz1836/go-discern/jev"
)

func main() {
    // go-discern never reads the environment: your code passes the key in.
    jc, err := jev.New(os.Getenv("TYPESAFE_API_KEY"), jev.WithModel("jev-1.13.0"))
    if err != nil {
        log.Fatal(err) // matches discern.ErrInvalidConfig, for example when the key is empty
    }
    ev, err := discern.New(discern.WithAdapter(jc))
    if err != nil {
        log.Fatal(err)
    }

    req := discern.Request{
        Providers: discern.JevOnly, // required: every request names its provider or providers
        State:     discern.Text("Subject: Charged twice\n\nMy card was charged twice for this month's invoice."),
        Questions: []discern.Question{
            discern.Choice("queue", discern.Text("Which support queue should handle this ticket?"),
                discern.Opt("billing", discern.Text("Invoices, refunds, failed payments")),
                discern.Opt("technical", discern.Text("Errors, outages, integration problems")),
                discern.Opt("account", discern.Text("Login, profile, permissions"))),
        },
    }

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    res, err := ev.Evaluate(ctx, req)
    cancel()
    if err != nil {
        log.Fatal(err) // for example "discern: jev: rejected (HTTP 401)" when the key is wrong
    }

    obs, _ := res.Observation(discern.Jev)
    queue, _ := obs.Choice("queue")
    fmt.Println("chosen:", queue.Option)
    for _, p := range queue.Probabilities { // every option, in request order
        fmt.Printf("  %-9s %.2f\n", p.Option, p.Probability)
    }
    fmt.Println("model:", obs.Model) // the version that answered
}
```

It prints the chosen option and the probability of every option. The numbers come from the model, so
yours will differ:

```text
chosen: billing
  billing   0.99
  technical 0.01
  account   0.00
model: jev-1.13.0
```

<br/>

### Both providers and a policy

To ask Jev and Clef the same question and get a recommendation, register both adapters and give the
evaluator a policy. Add this function to the program above, add the `errors` and
`github.com/mrz1836/go-discern/clef` imports, set `CLOUDFLARE_ACCOUNT_ID` and `CLOUDFLARE_API_TOKEN`,
and call it at the end of `main` with
`if err := routeBoth(context.Background(), jc, req); err != nil { log.Fatal(err) }`:

```go
func routeBoth(ctx context.Context, jc *jev.Client, req discern.Request) error {
    cc, err := clef.New(os.Getenv("CLOUDFLARE_ACCOUNT_ID"), os.Getenv("CLOUDFLARE_API_TOKEN"))
    if err != nil {
        return err
    }
    ev, err := discern.New(
        discern.WithAdapter(jc),
        discern.WithAdapter(cc),
        discern.WithDefaultPolicy(discern.Policy{
            Version: "routing-v1", // required; your label, recorded with every result
            Questions: []discern.QuestionPolicy{{
                Question: "queue",
                Checks:   []discern.Check{{Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.80}},
            }},
        }),
    )
    if err != nil {
        return err
    }

    req.Providers = discern.JevAndClef // two billable calls, made concurrently
    res, err := ev.Evaluate(ctx, req)
    if err != nil && !errors.Is(err, discern.ErrPartialFailure) {
        return err // no usable answer (with ErrPartialFailure, one provider still answered)
    }

    rec, _ := res.Recommendation("queue")
    if rec.Status == discern.Accepted {
        fmt.Println("route to", rec.Selected.Option)
    } else {
        fmt.Println("send to a person:", rec.Status, rec.Reasons)
    }
    fmt.Println(rec.Summary)
    return nil
}
```

The question is `accepted` only when both providers chose the same option and both passed the check.
The summary says why, for example:

```text
route to billing
accepted billing: jev choice.probability 0.98 ≥ 0.8; clef choice.probability 0.95 ≥ 0.8; both chose billing
```

Otherwise the status says why: `below_threshold` when they agree but a probability is under 0.8,
`review_required` (reason `providers_disagree`) when they chose different options, and `incomplete`
(reason `provider_failed`) when one of them failed. The policy never calls a provider; it only reads
the answers.

<br/>

### Next steps

- [Usage](#-usage): the three question kinds, the models, JSON state, policies, batches, storing
  results, an HTTP handler, and testing your code with fakes.
- [Behavior](#%EF%B8%8F-behavior): billing and retries, errors, what a stored record holds, and the limits.
- [examples/](examples): in a clone of this repository, `go run ./examples` routes synthetic tickets
  against fake providers, offline. `DISCERN_LIVE=1 go run ./examples -live` makes real, billable calls
  with the three variables above.
- [pkg.go.dev](https://pkg.go.dev/github.com/mrz1836/go-discern): the full API reference.

<br/>

## 💡 Usage

go-discern asks typed questions about a piece of content and returns every provider observation, with
an optional, explicit recommendation on top. Three question kinds are supported:

| Kind | Asks | Returns |
|---|---|---|
| **Choice** | Pick one of your named options (2–255) | The chosen option, the probability of every option (in your order), confidence |
| **Noul** | A yes/no question | The probability of *yes*, in [0,1]: a number, never a boolean |
| **Score** | Rate on ordered levels (2–10), lowest first | A fractional level position, the probability of every level, confidence |

Two providers sit behind one API, and **every request chooses its provider**:

| Provider | Package | Models |
|---|---|---|
| [TypeSafe Jev](https://docs.typesafe.ai/introduction) | `jev` | `jev-latest` (default), `jev-preview`, or a versioned ID such as `jev-1.13.0` |
| [Cloudflare Workers AI Clef](https://developers.cloudflare.com/workers-ai/models/clef/) | `clef` | `clef` (default), `clef-flash`, `clef-omni` |

In October 2026 both Jev aliases resolved to `jev-1.13.0`, and the shorter names `jev` and `jev-1.13`
that appear in TypeSafe's SDK examples were refused as unknown models (HTTP 400). Pin a versioned
model in production: the aliases can move.

The three Clef models trade context and price. The figures below are from Cloudflare's model pages
(October 2026), and Cloudflare changes them:

| Clef model | Constant | Context window | Input price | Choose it for |
|---|---|---|---|---|
| `clef` (default) | `clef.ModelClef` | 65,536 tokens | $0.24 per M tokens | the safest answer quality and the most state |
| `clef-flash` | `clef.ModelFlash` | 24,576 tokens | $0.038 per M tokens | the lowest cost, with small state |
| `clef-omni` | `clef.ModelOmni` | 64,000 tokens | $0.15 per M tokens | a mixture of experts (30B, 3B active) priced in between |

Tokens cannot be counted locally, so go-discern never checks these windows before a call; see
*Oversized state* under [Behavior](#%EF%B8%8F-behavior) for what each model did with state that did
not fit.

<br/>

### Set up once

```go
jc, err := jev.New(apiKey, jev.WithModel("jev-1.13.0"), jev.WithTimeout(15*time.Second))
if err != nil {
    return err // discern.ErrInvalidConfig: empty or header-unsafe key, bad model, timeout <= 0
}
cc, err := clef.New(accountID, apiToken, clef.WithModel(clef.ModelFlash))
if err != nil {
    return err // discern.ErrInvalidConfig: account ID not 32 hex characters, bad token or model
}
ev, err := discern.New(
    discern.WithAdapter(jc),
    discern.WithAdapter(cc),
    discern.WithDefaultPolicy(routing), // optional; routing is a discern.Policy, see Recommendations below
)
```

Both adapters take the same options: `WithModel`, `WithTimeout` (default 30 seconds),
`WithMaxResponseBytes` (default 4 MiB, at most 64 MiB), `WithHTTPClient`, and `WithTestEndpoint`, which
accepts only an `https` server on a loopback address and exists for tests. `discern.New` takes one
`WithAdapter` per provider, `WithDefaultPolicy` and `WithBatchConcurrency`. The `Evaluator` and both
adapters are immutable and safe for concurrent use.

<br/>

### Ask questions

```go
req := discern.Request{
    Providers: discern.ClefOnly, // discern.JevOnly | discern.ClefOnly | discern.JevAndClef (required)
    State:     discern.Text(subject + "\n\n" + body),
    Questions: []discern.Question{
        discern.Choice("queue", discern.Text("Which support queue should handle this ticket?"),
            discern.Opt("billing", discern.Text("Invoices, refunds, failed payments")),
            discern.Opt("technical", discern.Text("Errors, outages, integration problems")),
            discern.Opt("account", discern.Text("Login, profile, permissions"))),
        discern.Noul("outage", discern.Text("Does the customer report a production outage?")),
        discern.Score("severity", discern.Text("How severe is the customer impact?"),
            discern.Level("cosmetic", discern.Text("Cosmetic; work continues")),
            discern.Level("degraded", discern.Text("Degraded; workaround exists")),
            discern.Level("blocked", discern.Text("Blocked; no workaround"))),
    },
    CorrelationID: "ticket-1042",          // optional, stored verbatim in the record
    EvidenceRefs:  []string{"kb:article-17"}, // optional, opaque; never fetched or validated
}

res, err := ev.Evaluate(ctx, req)
if err != nil {
    return err
}
obs, _ := res.Observation(discern.Clef)
q, _ := obs.Choice("queue")       // q.Option "billing"; q.Probabilities in request order; q.Confidence *float64
n, _ := obs.Noul("outage")        // n.Value 0.07 = P(yes)
s, _ := obs.Score("severity")     // s.Value 1.05 (fractional); s.Nearest() == "degraded"
```

Questions are answered independently. A Noul question can also describe both ends: set `Yes` and `No`
on the `Question` that `discern.Noul` returns. Option order is sent exactly as given (models can lean
toward earlier options).

The state is text, a JSON object or a JSON array, and must not be blank. Structured state is sent byte
for byte, so large integers and key order survive:

```go
state := discern.JSON([]byte(`{"ticket":{"id":12345678901234567890,"text":"..."}}`)) // raw JSON, sent as is
state, err := discern.Marshal(ticket) // or encode a Go value once, without HTML escaping
```

<br/>

### Run both providers

`JevAndClef` makes **exactly two potentially billable calls**, concurrently. One provider failing never
cancels the other, and the result keeps a fixed order (Jev, then Clef) regardless of which finishes
first.

```go
req.Providers = discern.JevAndClef
res, err := ev.Evaluate(ctx, req)
if res != nil {
    audit.Save(res) // persist first: res != nil whenever any call was attempted
}
switch {
case err == nil: // res.Status == discern.Complete
case errors.Is(err, discern.ErrPartialFailure): // one observation is usable; check this case first
case res != nil: // both failed; res.Outcomes hold both typed failures
default: // nothing was sent (invalid, unsupported, or already canceled)
}
```

<br/>

### Recommendations

Without a policy, go-discern returns observations and **never manufactures an acceptance decision**.
A `Policy` is a small, typed set of threshold checks per question:

```go
routing := discern.Policy{
    Version: "routing-v3", // required; your label, recorded but not interpreted
    Questions: []discern.QuestionPolicy{{
        Question: "queue",
        Checks: []discern.Check{ // all applicable checks must pass
            {ID: "queue-jev", Provider: discern.Jev, Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.80},
            {ID: "queue-clef", Provider: discern.Clef, Metric: discern.MetricChoiceProbability, Op: discern.AtLeast, Threshold: 0.75},
        },
    }, {
        Question:  "severity",
        Checks:    []discern.Check{{Metric: discern.MetricScore, Op: discern.AtMost, Threshold: 1.0}}, // lower is better
        Agreement: &discern.Agreement{MaxDifference: 0.25}, // dual Noul/Score tolerance; 0 allows only float noise
    }},
    // PreferOnConflict: discern.Jev — opt in to pick a provider when the two disagree
    // AllowPartial: true             — opt in to evaluate the survivor when one provider fails
}

rec, _ := res.Recommendation("queue") // Status, Selected (nil unless Accepted), Reasons, Checks, Summary
picked := res.SelectedAnswers("queue") // the typed answers the selection points to
fmt.Println(res.Overall.Status)         // accepted only if every covered question is accepted

res, err = ev.Evaluate(ctx, req, discern.WithPolicy(strict)) // this call only; the evaluator is untouched
res, err = ev.Evaluate(ctx, req, discern.WithoutPolicy())    // observations only
again, err := discern.Recommend(res, experimental)           // replay locally: zero calls, input untouched
```

| Status | Meaning |
|---|---|
| `accepted` | Every applicable check passed; for dual runs the providers agreed, or the preferred provider passed a disagreement |
| `below_threshold` | At least one applicable check failed |
| `review_required` | The providers disagreed and no conflict preference resolved it (the default) |
| `not_applicable` | No check applied to a provider or model, or a dual Noul/Score question has no agreement tolerance |
| `incomplete` | A provider the policy needs failed (with `AllowPartial`, a dual run's surviving provider is evaluated alone), or a checked metric was absent |
| `no_rule` | No policy covered the question |

Metrics are `choice.probability` (the probability of the chosen option), `confidence` (Choice and
Score), `noul` and `score` (constants `discern.MetricChoiceProbability`, `MetricConfidence`, `MetricNoul`
and `MetricScore`); ops are `gte`, `gt`, `lte` and `lt` (`discern.AtLeast`, `Above`, `AtMost` and
`Below`), with exact boundary behavior. Checks can
be scoped to a provider and a model. A model-scoped check matches the model the provider *reported*
(or the requested model when none is reported), so an alias that moves to a new version stops matching.
If that leaves no check for the provider, the question is `not_applicable` (`model_mismatch`); if
unscoped checks still apply, they decide and the reason `model_check_skipped` records the skip. Reason
codes are a closed, documented list; the summary text is for people and is not a stable contract.
Two-sided gates (escalate at `noul ≥ 0.9`, close at `noul ≤ 0.1`) are two `Recommend` calls.

A dual-run policy needs an applicable check for *both* providers, even with `PreferOnConflict`. With
`AllowPartial`, a dual run can return an error matching `ErrPartialFailure` together with `accepted`
recommendations for the surviving provider. `Policy` has JSON tags; when you load one from
configuration, decode it with `json.Decoder.DisallowUnknownFields` so a misspelled key cannot silently
become a zero threshold.

<br/>

### Batches

```go
items, err := ev.EvaluateBatch(ctx, reqs, discern.WithPolicy(routing))
if err != nil {
    return err // only when an item is invalid: zero calls for the whole batch
}
for _, it := range items {
    _ = it.Result // same contract as Evaluate, in input order
    _ = it.Err
}
```

At most `WithBatchConcurrency(n)` requests run at once (default 4), so up to `2n` calls for dual
requests. Every item is billed; nothing is deduplicated. Provider rate limits apply (in October 2026,
Jev documented 80 requests per second, and Cloudflare 300 requests per minute for Clef's task type).

<br/>

### Storage

A `Result` is a versioned JSON record (`discern.result/v1`):

```go
data, err := json.Marshal(res.Redact(discern.Redaction{CorrelationID: true}))
restored, err := discern.ParseResult(data) // ErrInvalidRecord on another schema, broken references or inconsistent verdicts
```

<br/>

### In a net/http handler

```go
func (h *Handler) Route(w http.ResponseWriter, r *http.Request) {
    ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
    defer cancel()
    res, err := h.ev.Evaluate(ctx, buildRequest(r))
    if res != nil {
        h.audit.Save(r.Context(), res)
    }
    switch {
    case errors.Is(err, discern.ErrInvalidRequest), errors.Is(err, discern.ErrUnsupported),
        errors.Is(err, discern.ErrInvalidPolicy):
        http.Error(w, "invalid request", http.StatusBadRequest) // zero calls made
        return
    case err != nil && !errors.Is(err, discern.ErrPartialFailure) && errors.Is(err, context.DeadlineExceeded):
        http.Error(w, "timeout", http.StatusGatewayTimeout)
        return
    case err != nil && !errors.Is(err, discern.ErrPartialFailure):
        http.Error(w, "upstream failure", http.StatusBadGateway)
        return
    }
    respond(w, res) // Complete or Partial
}
```

<br/>

### Testing your application

`discerntest` provides offline fakes that count calls. The evaluator validates fake answers exactly like
real ones.

```go
fake := discerntest.Static(discern.Jev, discern.Observation{Model: "jev-test", Answers: answers})
ev, err := discern.New(discern.WithAdapter(fake))
// ... exercise your code ...
calls := fake.Calls()
// discerntest.Func(provider, model, fn) and discerntest.Failing(provider, err) also exist.
```

<br/>

## ⚙️ Behavior

<details>
<summary><strong><code>Billing, retries and timeouts</code></strong></summary>
<br/>

- **One attempt per selected provider.** No retries, fallback to the other provider, hedging, racing
  or shadow calls: each of these can bill twice. A dual request is the only way to make two calls.
  The SDK also switches off the standard library's own transparent replays: Go's HTTP/2 client would
  otherwise resend a POST whose stream was refused or reset, even after the server read the whole body.
- **Validation first.** The request, the effective policy and every selected adapter (including
  provider limits such as Clef's 64 questions and 13 MiB body, and whether the answer could even fit
  in `WithMaxResponseBytes`) are checked before any call. A local error means nothing was sent; an
  already-canceled context also sends nothing.
- **Timeouts.** Each adapter applies `WithTimeout` (default 30s) to the request *and* the full body
  read; the caller's context deadline still applies. An adapter timeout is `timeout`, and so is the
  `Timeout` of an injected `http.Client` when it fires first; the caller's deadline or cancellation is
  `deadline_exceeded` or `canceled`.
- **Retry hints.** `retry-after-ms` (rounded up to a whole millisecond) and then `Retry-After` (seconds
  or an HTTP date) are reported in `ProviderError.RetryAfter` (set when `HasRetryAfter` is true) and in
  the record, capped at 24 hours. They are hints, not proof that repeating a call is safe or free.
  Retrying is your call.
- **No guarantees beyond the SDK.** Cancelling a request does not promise the provider stopped work, and
  there is no exactly-once promise. An injected `http.Client` is copied with redirects disabled and no
  cookie jar; a custom `Transport` that retries or redirects on its own is outside these guarantees.
  The default transport honors the standard `HTTPS_PROXY` and `NO_PROXY` settings.
- **Bounded responses.** Bodies above `WithMaxResponseBytes` (default 4 MiB) fail as
  `response_too_large`, and reading stops at the limit. Every body is closed, and one within the limit
  is read to the end, so its connection can be reused.

</details>

<details>
<summary><strong><code>Errors</code></strong></summary>
<br/>

```go
var pe *discern.ProviderError
errors.Is(err, discern.ErrInvalidRequest)     // also ErrInvalidPolicy, ErrUnsupported: zero calls
errors.Is(err, discern.ErrInvalidConfig)      // constructors: jev.New, clef.New, discern.New
errors.Is(err, discern.ErrPartialFailure)     // dual run: one provider failed, the other's observation is in the Result
errors.Is(err, discern.ErrRejected)           // non-2xx (including a redirect), or Cloudflare success:false or any errors[] entry
errors.Is(err, discern.ErrMalformedResponse)  // includes response_too_large
errors.Is(err, discern.ErrInvalidRecord)      // ParseResult and Recommend
errors.Is(err, context.Canceled)              // preserved through ProviderError
errors.As(err, &pe)                           // pe.Provider, pe.Kind, pe.StatusCode, pe.RequestID, pe.RetryAfter, pe.Codes
```

`pe.Kind`, also stored as the record's `Failure.Kind`, is one of `timeout`, `deadline_exceeded`,
`canceled`, `transport`, `rejected`, `malformed_response`, `response_too_large` or `adapter_error`
(an error from a custom or fake adapter that is not a `ProviderError`).

Error strings never contain credentials, account IDs, URLs, request state or provider bodies. Provider
error messages can echo input, so they are only available through `pe.ProviderMessages()`: Cloudflare's
`errors[].message`, and for Jev, whose error bodies are undocumented, the observed FastAPI-style
`detail`, one message per error as `error_type: message` (for example
`api_usage_error: Unknown model: jev`, or just `max_tokens_exceeded`). Refusal details are read
leniently and kept bounded: at most 64 codes and 64 messages, each message cut to 1 KiB.
Tools that walk error chains (such as testify failure messages) print the sanitized causes, which never
include credentials, URLs, payloads or provider messages.

</details>

<details>
<summary><strong><code>Confidence, scores and what the models return</code></strong></summary>
<br/>

- **Confidence is not the probability of being right.** Both providers compute it from the
  probabilities as a concentration statistic. Jev's and Clef's values are not comparable, and the SDK
  never compares raw confidences across providers or picks a winner by them.
- **Noul is a probability**, not a boolean, and has no separate confidence.
- **Score is ordinal and fractional** (Σ level·probability). `Nearest()` rounds to a level when you need
  one; magnitudes between levels are not calibrated.
- **No rationale.** System One models do not explain themselves, and go-discern never invents a reason.
  Summaries describe only observed numbers and the SDK's own checks.
- **Rounding.** Providers round probabilities without documenting how. Validation infers each
  distribution's precision and allows one rounding unit per value; nothing is ever renormalized. With
  many options at two decimals, one unit per value adds up, so the sum check is necessarily loose there.
- **Oversized state.** Tokens cannot be counted locally, so the SDK never checks a context window
  before a call. What the providers did with state that did not fit, observed in October 2026:
  - **Jev** answered a state of about 31K tokens and refused one of about 70K with HTTP 400; its
    `ProviderMessages()` held `max_tokens_exceeded`.
  - **`clef`** (65,536-token window) and **`clef-flash`** (24,576) refused the request before the model
    ran: HTTP 422 with Cloudflare code 3030 ("Request requires N tokens; the maximum is M"), or, far
    over the window, HTTP 413 with code 5021.
  - **`clef-omni`** (64,000) cut the state to 64,000 tokens and answered, so not all of the state is
    guaranteed to influence its answer.

  Every refusal is `rejected` (it matches `ErrRejected`), with Cloudflare's code in the record's
  `Failure.Codes` and the provider's text in `ProviderMessages()`. Cloudflare's model pages say all three
  Clef models truncate, so treat this as observed behavior that can change.

</details>

<details>
<summary><strong><code>Audit records and privacy</code></strong></summary>
<br/>

Records include caller identifiers (question, option and level IDs, the correlation ID, evidence
references, the policy version), provider outputs and metadata, every SDK check, and storage-safe
failures (kind, HTTP status, request IDs, retry hint, Cloudflare codes). They **never** include the state,
instructions, option or level descriptions, legend text, credentials, headers, bodies, provider error
messages or Go error strings. Values a provider reports about itself (the model name, request ID and ray
ID) are kept only when they are at most 256 bytes of visible ASCII, so a provider cannot echo request
content or line breaks into a record or a summary; a reported model that breaks the rule fails the call
as `malformed_response`, and an invalid request or ray ID is dropped.

Identifiers and outputs can still be sensitive. `Redact` returns a copy without the fields you select
(the correlation ID, evidence references, request and ray IDs, usage) and lists them in `redacted`;
question, option and level IDs are kept because replay needs them, so keep them free of personal data.
Nothing is persisted, logged or sent anywhere by the SDK.

A record supports traceability and local policy replay. It is not proof that an answer is true, not a
tamper-evident ledger, and not a promise that a repeated call would return the same values.
Optional values are pointers, so absent is never confused with zero; `started_at` is client wall-clock
time (UTC) and `elapsed_ns` is client-observed time, not provider compute time. Within `v1` the schema
changes additively only; parsers must treat unknown reason codes as non-accepting.

</details>

<details>
<summary><strong><code>Limits and unverified assumptions</code></strong></summary>
<br/>

| | Jev | Clef |
|---|---|---|
| Questions per request | not documented (65 answered in October 2026) | 1–64 (checked) |
| Question IDs | not documented | `[A-Za-z0-9_.-]{1,100}` — applied to both so requests stay portable |
| Choice options | ≤255 | 2–255 — 2–255 applied to both |
| Score levels | 2–10 | 2–10 |
| Request body | not documented | ≤13 MiB (checked on the exact bytes; documented for `clef` and `clef-flash`, applied to `clef-omni` too) |

Option and level IDs, the correlation ID and each evidence reference are limited to 256 bytes of UTF-8
without control or invisible formatting characters, and at most 32 evidence references are accepted;
these are library limits that keep records bounded. IDs are compared byte for byte, without Unicode
normalization. The state and instructions must be text, a JSON object or a JSON array, and not blank:
text of only white space, `{}` and `[]` are rejected before any call, since a provider would still
bill for a blank state. Score level descriptions are required (Jev's docs also allow `null`), and JSON
values with duplicate keys are rejected. Jev documents a context of 64k tokens per request (32k for the
state plus the longest question) without saying what happens beyond it; in October 2026 it refused an
oversized state with HTTP 400 (see *Oversized state* above). go-discern sends only text and JSON
state: it does not support the image input of the Clef models, or the audio and video input of
`clef-omni`.

The offline test suite cannot verify: actual rounding precision, whether Jev keeps its error-body
shape (observed, not documented; read leniently), Cloudflare's
`cf-ai-req-id`, `cf-ray` and `cf-ai-neurons` headers (not documented; seen on live calls in October
2026 and checked by the [live tests](#live-contract-tests), but captured best-effort), whether
Cloudflare ever returns `success:false` with HTTP 200 (handled as a rejection) or reports truncation in
`messages[]` (ignored), and whether providers keep option IDs byte for byte (for example, a JavaScript
layer could reorder integer-like keys such as `"2"`, `"1"`). Pin a versioned Jev model in production:
`jev-latest` can move.

</details>

<br/>

## 📚 Documentation

View the generated [documentation](https://pkg.go.dev/github.com/mrz1836/go-discern?tab=doc), the
[TypeSafe API reference](https://docs.typesafe.ai/api) and the
[Cloudflare Clef model page](https://developers.cloudflare.com/workers-ai/models/clef/).

| Package | Purpose |
|---|---|
| [`discern`](https://pkg.go.dev/github.com/mrz1836/go-discern) | Requests, the `Evaluator`, policies, recommendations and records |
| [`jev`](https://pkg.go.dev/github.com/mrz1836/go-discern/jev) | TypeSafe Jev adapter |
| [`clef`](https://pkg.go.dev/github.com/mrz1836/go-discern/clef) | Cloudflare Workers AI Clef adapter |
| [`discerntest`](https://pkg.go.dev/github.com/mrz1836/go-discern/discerntest) | Offline fakes for application tests |

<details>
<summary><strong><code>Development Setup (Getting Started)</code></strong></summary>
<br/>

Install [MAGE-X](https://github.com/mrz1836/mage-x) build tool for development:

```bash
# Install MAGE-X for development and building
go install github.com/mrz1836/mage-x/cmd/magex@latest
magex update:install
```
</details>

<details>
<summary><strong><code>Library Deployment</code></strong></summary>
<br/>

This project uses [goreleaser](https://github.com/goreleaser/goreleaser) for streamlined binary and library deployment to GitHub. To get started, install it via:

```bash
brew install goreleaser
```

The release process is defined in the [.goreleaser.yml](.goreleaser.yml) configuration file.

Then create and push a new Git tag using:

```bash
magex version:bump bump=patch push=true branch=master
```

</details>

<details>
<summary><strong><code>Build Commands</code></strong></summary>
<br/>

View all build commands

```bash script
magex help
```

</details>

<details>
<summary><strong><code>GitHub Workflows</code></strong></summary>
<br/>

All workflows are driven by modular configuration in [`.github/env/`](.github/env/README.md) — no YAML editing required.

**[View all workflows and the control center →](.github/docs/workflows.md)**

</details>

<details>
<summary><strong><code>Updating Dependencies</code></strong></summary>
<br/>

To update all dependencies (Go modules, linters, and related tools), run:

```bash
magex deps:update
```

</details>

<br/>

## 🧪 Examples & Tests

All unit tests, fuzz tests and examples run via [GitHub Actions](https://github.com/mrz1836/go-discern/actions) on Go 1.26.x (the supported baseline) and the current Go release. View the [configuration file](.github/workflows/fortress.yml).

Run all tests (fast):

```bash script
magex test
```

Run all tests with race detector (slower):
```bash script
magex test:race
```

Runnable examples live in the `Example*` functions of the `discern`, `jev` and `clef` packages and in
[examples/](examples), a synthetic support-ticket router that runs offline against fake providers by
default:

```bash
go run ./examples                         # offline, no credentials needed
DISCERN_LIVE=1 go run ./examples -live    # billable: needs TYPESAFE_API_KEY, CLOUDFLARE_ACCOUNT_ID and CLOUDFLARE_API_TOKEN
```

### Live contract tests

Unit tests never touch the network. A separate, opt-in suite in [internal/livetest](internal/livetest)
calls the real Jev and Clef APIs and asserts the behavior the adapters rely on: every model's answers
decode, refusals carry the expected HTTP status, Cloudflare codes and provider messages, timeouts are
classified, the documented limits hold, and oversized state is refused or truncated as observed. When a
provider changes its contract, a test fails with a message that starts with `provider behavior changed?`
and shows what was observed. Run it on demand: before a release, or when a provider announces a change.

Credentials in your environment never start a call by themselves. Every gate must pass:

| Variable                                        | Default | Effect                                                                         |
|-------------------------------------------------|---------|--------------------------------------------------------------------------------|
| `DISCERN_LIVE`                                  | unset   | Must be `1`, or every live test skips. Set it only for the run.                |
| `TYPESAFE_API_KEY`                              | unset   | Enables the Jev tests.                                                         |
| `CLOUDFLARE_ACCOUNT_ID`, `CLOUDFLARE_API_TOKEN` | unset   | Enable the Clef tests. Tests that use both providers need both sets.           |
| `DISCERN_LIVE_LARGE`                            | unset   | `1` adds the large-state probes, about $0.025 more per run.                    |
| `DISCERN_LIVE_MAX_USD`                          | `0.10`  | Estimated spend, in US dollars, after which the remaining tests skip.          |
| `CI`, `DISCERN_LIVE_ALLOW_CI`                   | unset   | When `CI` is set, the suite skips unless `DISCERN_LIVE_ALLOW_CI=1` is set too. |

The suite also needs the `live` build tag. The `magex` test targets and CI compile it with that tag, and it
skips there because `DISCERN_LIVE` is unset.

```bash
# Standard tier: about 60 calls, roughly $0.002
DISCERN_LIVE=1 go test -tags live -run TestLive -v ./internal/livetest

# Add the large-state probes
DISCERN_LIVE=1 DISCERN_LIVE_LARGE=1 go test -tags live -run TestLive -v ./internal/livetest

# One provider: unset the other provider's credentials
env -u CLOUDFLARE_API_TOKEN DISCERN_LIVE=1 go test -tags live -run TestLive -v ./internal/livetest

# One area, or one test
DISCERN_LIVE=1 go test -tags live -run 'TestLive/err/clef' -v ./internal/livetest
DISCERN_LIVE=1 go test -tags live -run 'TestLive/clef/kinds/clef-omni' -v ./internal/livetest
```

Tests run one at a time. A provider that answers HTTP 429 is skipped for the rest of the run, and the
calls, tokens, neurons and estimated spend are logged at the end. Logs hold only response metadata
(models, request and ray IDs, usage and answers), and every error and record is checked for the
credentials.

<br/>

## ⚡ Benchmarks

Run the Go benchmarks:

```bash script
magex bench
```

### 📊 Performance Results

Benchmarks measure local CPU work only (validation, encoding, decoding, policy evaluation and record
encoding) with in-memory fakes. They make no claim about network or provider latency, which dominates
real calls. For lower latency, run a single provider per request: a dual run waits for the slower one.

<br/>

## 🛠️ Code Standards
Read more about this Go project's [code standards](.github/CODE_STANDARDS.md).

<br/>

## 🤖 AI Usage & Assistant Guidelines
Read the [AI Usage & Assistant Guidelines](.github/tech-conventions/ai-compliance.md) for details on how AI is used in this project and how to interact with the AI assistants.

<br/>

## 👥 Maintainers
| [<img src="https://github.com/mrz1836.png" height="50" alt="MrZ" />](https://github.com/mrz1836) |
|:------------------------------------------------------------------------------------------------:|
|                                [MrZ](https://github.com/mrz1836)                                 |

<br/>

## 🤝 Contributing
View the [contributing guidelines](.github/CONTRIBUTING.md) and please follow the [code of conduct](.github/CODE_OF_CONDUCT.md).

### How can I help?
All kinds of contributions are welcome :raised_hands:!
The most basic way to show your support is to star :star2: the project, or to raise issues :speech_balloon:.
You can also support this project by [becoming a sponsor on GitHub](https://github.com/sponsors/mrz1836) :clap:
or by making a [**bitcoin donation**](https://mrz1818.com/?tab=tips&utm_source=github&utm_medium=sponsor-link&utm_campaign=go-discern&utm_term=go-discern&utm_content=go-discern) to ensure this journey continues indefinitely! :rocket:


[![Stars](https://img.shields.io/github/stars/mrz1836/go-discern?label=Please%20like%20us&style=social)](https://github.com/mrz1836/go-discern/stargazers)

<br/>

## 📝 License

[![License](https://img.shields.io/github/license/mrz1836/go-discern.svg?style=flat&v=1)](LICENSE)

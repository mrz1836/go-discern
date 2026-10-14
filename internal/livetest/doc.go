// Package livetest holds go-discern's opt-in live contract suite: tests that
// call the real Jev and Clef APIs and assert the provider behavior the adapters
// rely on, so a provider change shows up as a failing test whose message starts
// with "provider behavior changed?". The package has no code; its tests carry
// the live build tag and skip unless they are enabled on purpose.
//
// A test makes a billable call only when every gate passes:
//
//   - the live build tag (go test -tags live);
//   - DISCERN_LIVE=1, set for the run. Credentials in the environment never
//     start a call on their own;
//   - when CI is set, DISCERN_LIVE_ALLOW_CI=1 as well;
//   - the provider's credentials: TYPESAFE_API_KEY for Jev, and
//     CLOUDFLARE_ACCOUNT_ID and CLOUDFLARE_API_TOKEN for Clef. Tests for a
//     provider without credentials skip, and dual tests need both;
//   - DISCERN_LIVE_LARGE=1 for the large-state probes, which cost the most;
//   - the budget: the suite estimates its spend from the usage the providers
//     report and skips the remaining tests once it passes DISCERN_LIVE_MAX_USD
//     (US dollars, default 0.10).
//
// A provider that answers HTTP 429 is skipped for the rest of the run. Tests run
// one at a time, log only response metadata, and check every error and record
// for the credentials. A standard run makes about 60 calls for roughly
// $0.002; the large tier adds about $0.025.
//
//	DISCERN_LIVE=1 go test -tags live -run TestLive -v ./internal/livetest
//	DISCERN_LIVE=1 DISCERN_LIVE_LARGE=1 go test -tags live -run TestLive -v ./internal/livetest
//	DISCERN_LIVE=1 go test -tags live -run 'TestLive/err/clef' -v ./internal/livetest
package livetest

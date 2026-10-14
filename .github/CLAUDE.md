# CLAUDE.md

## 🤖 Welcome, Claude

This repository uses **`AGENTS.md`** as the single source of truth for:

* Coding conventions (naming, formatting, commenting, testing)
* Contribution workflows (branch prefixes, commit message style, PR templates)
* Release, CI, and dependency‑management policies
* Security reporting and governance links

> **TL;DR:** **Read `AGENTS.md` first.**
> All technical or procedural questions are answered there.

### Quick Checklist for Claude

1. **Study `AGENTS.md`**
   Make sure every automated change or suggestion respects those rules.
2. **Follow branch‑prefix and commit‑message standards**
   They drive Mergify auto‑labeling and CI gates.
3. **Never tag releases**
4. **Pass CI**
   Run `go fmt`, `goimports`, `go vet`, `staticcheck`, and `golangci‑lint` locally before opening a PR.

If you encounter conflicting guidance elsewhere, `AGENTS.md` wins.
Questions or ambiguities? Open a discussion or ping a maintainer instead of guessing.

Happy hacking!

---

## About This Project

`go-discern` is a small Go decision layer over two hosted System One judgment APIs: TypeSafe
**Jev** and Cloudflare Workers AI **Clef**. It is a library, not an application: there is no
binary, no CLI, and no runtime configuration file.

- Module: `github.com/mrz1836/go-discern` (package `discern`)
- Go version: 1.26 (CI also checks the current release)
- Runtime dependencies: none (`testify` is used only by tests and `internal/testutil`)

**Invariants that changes must preserve:**
- **No runtime dependencies.** The packages callers import (root, `jev`, `clef`, `discerntest`)
  and their internal dependencies import only the standard library; `testify` is used only by
  tests and `internal/testutil`. The `depguard` linter enforces this for every non-test file.
- **One attempt per selected provider.** No retries, fallback, hedging or first-wins racing:
  each call may be billed.
- **No environment reads, globals or `init`.** Credentials and options are passed explicitly.
- **No secrets or payloads in errors or records.** Keys, headers, state, instructions and
  provider bodies never appear in `Error()` strings or audit records.
- **Unit tests stay offline.** The opt-in live contract suite in `internal/livetest` needs the
  `live` build tag and `DISCERN_LIVE=1`, and costs money. When a provider changes behavior on
  purpose, update the suite's expectations and the docs together.

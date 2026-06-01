---
name: unit-test-expand
description: Increase test coverage by targeting untested branches and edge cases in Go or Python packages. Use when the user asks to write tests, expand coverage, or test edge cases.
---

# Expand Unit Tests (Go + Python)

Expand existing unit tests. Use Go's standard `testing` package for Go code and `pytest` for Python.

## Step 1 — analyze coverage

- **Go:** `go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out`
- **Python:** `pytest --cov=src --cov-report=term-missing`

Identify untested functions, branches, and edge cases.

## Step 2 — identify gaps

Review code for: logical branches, error paths, boundary conditions, empty/nil inputs, time- and TTL-sensitive paths, and any path that interacts with `CONTRACT.md` constraints.

## Step 3 — write tests targeting:

- **Error handling paths** — what happens when SPIRE is unavailable, Vault returns 403, the JWKS endpoint times out, an upstream signature is invalid
- **Boundary values** — empty subject_token, missing `actor_token`, scope with extra colons, audience mismatch, `exp` exactly equal to `now()`
- **State transitions** — first-hop exchange (no existing `act`) → minted token has single-level `act`; subsequent-hop exchange (existing `act`) → minted token nests correctly
- **`CONTRACT.md` invariants** — `sub` never mutates across an exchange; outermost `act.sub` equals the calling actor; chain depth respects the configured cap; impersonation guard fires on mutated `sub`

## Step 4 — follow existing patterns

- Go: table-driven tests when there are multiple input/output cases; `t.Helper()` in shared assertion helpers; parallel where safe
- Python: `pytest.mark.parametrize` for table-driven cases; explicit fixtures over `setUp`/`tearDown` patterns

## Step 5 — verify improvement

Re-run coverage and confirm a measurable increase. New tests must pass; existing tests must not regress.

## Focus areas by package

- `services/authz/internal/exchange/` — the act-chain builder and its nesting rule (RFC 8693 §2.1 examples). The most important tests in the repo.
- `services/authz/internal/policy/` — fail-closed paths; unknown scope grammar; chain-depth cap
- `services/authz/internal/audit/` — audit emitter buffer + retry behaviour under control-plane outages
- `sdks/resource-py/` — JWT validation: missing `act`, single `act`, nested `act`, signature mismatch, audience mismatch, expired token, mutated `sub` (the impersonation guard)
- `sdks/agent-py/` — SVID-fetch + token-exchange + Vault-lease pipeline; failure isolation at each step

Present new test code blocks only. Match the naming and structure of existing tests in the package.

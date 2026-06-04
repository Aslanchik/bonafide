# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project overview

bonafide is a security-first, agent-native identity provider — an alternative to Okta built for the agent era. It authenticates both human and non-human (AI agent) identities and treats the *human → agent → sub-agent → tool* delegation chain as a first-class, signed primitive (RFC 8693 nested `act` claim). Workload identity is SPIFFE-rooted; secrets come from Vault via SPIFFE-authenticated short-lived leases. No static long-lived secrets, TTL-only revocation in MVP. Personal learning project — clean-room, self-contained, runnable locally.

For product framing read `PRODUCT.md`; for system architecture read `DESIGN.md`; for every wire format read `CONTRACT.md`. Those three documents are authoritative; this file describes *how we work*.

## Development methodology: spec-driven

**Code does not get written until the relevant spec is reviewed and approved.** Specs live in `specs/<slice>/`:

1. `requirements.md` — what and why (capability blocks with binary acceptance criteria, no implementation details)
2. `design.md` — how (architecture, data models, interface contracts, tradeoffs explicit)
3. `tasks.md` — ordered, atomic, testable work items; each references the requirement(s) it satisfies and carries a binary `Verified when:` clause

If a task reveals a spec problem, update the spec and re-review before continuing. Do not improvise outside the approved `tasks.md`.

**Capability IDs:** each slice uses a 3-letter prefix (`TEC` for `token-exchange-core`, `SWI` for `spire-workload-identity`, `VSA`, `OPE`, `AUD`, `SAN`). Capabilities are numbered within the slice: `TEC-1`, `TEC-2`, …

**Slice order (the build order):** `token-exchange-core` → `spire-workload-identity` → `vault-spiffe-auth` → `opa-policy-engine` → `audit-persistence` → `subagent-nesting`. One slice end-to-end before the next.

### What to spec tightly vs. loosely

**Tight** (deterministic protocol surface — anything in `CONTRACT.md`): RFC 8693 endpoint shape, JWT claim presence/values, scope grammar, SPIFFE ID format, audit event schema, Vault API surface, SPIRE registration entries.

**Loose** (implementation latitude): internal package layout, log line formats, struct names, error wording, Postgres column-level details that don't appear in `CONTRACT.md`, container build minutiae.

If you find yourself writing a struct field name in `design.md` that doesn't appear in `CONTRACT.md`, stop and ask whether `CONTRACT.md` needs it or whether `design.md` is over-specifying.

## Architecture summary

Two services, three off-the-shelf components, two SDKs, three demo apps. Full picture in `DESIGN.md` §1; abbreviated:

- **`services/authz`** (Go) — the data plane. OIDC provider + RFC 8693 token-exchange + policy gate + JWT signing + audit emission. Built on `github.com/zitadel/oidc/v3` `/pkg/op`.
- **`services/control`** (Python, FastAPI) — the control plane. Agent registry, policy CRUD, audit ingest + delegation-chain reconstruction.
- **SPIRE Server + Agent** — workload identity, single trust domain `bonafide.local` (introduced in Slice 2).
- **HashiCorp Vault 1.21+** — secrets backend. KV in M1, DB secrets engine in M3, SPIFFE auth method bound to `spiffe://bonafide.local/agent/*` in M3 (fallback: Vault JWT auth + SPIRE OIDC discovery).
- **Postgres 16** — calendar fixture + audit log (from M5).
- **`sdks/agent-py`** — Python agent SDK that wraps fetch-SVID → token-exchange → fetch-Vault-lease into one ergonomic client.
- **`sdks/resource-py`** — FastAPI middleware that validates the task token, decodes the `act` claim, and exposes a typed `ActorChain` on the request state.
- **`apps/demo-human`**, **`apps/demo-agent`**, **`apps/demo-calendar`** — the end-to-end demo.

## Safety constraints (non-negotiable)

These must appear as explicit acceptance criteria in any `requirements.md` whose slice touches them — not as nice-to-haves:

- **All credentials short-lived.** TTL ceilings in `DESIGN.md` §4 (user JWT ≤ 15 min, task token ≤ 5 min, JWT-SVID ≤ 5 min, Vault DB lease ≤ 5 min). No code path may extend these.
- **No static long-lived secrets** in source, env files, or container images outside SPIRE and Vault. The agent SDK never reads a credential from disk.
- **Fail closed.** Missing `actor_token`, unknown agent, unknown scope grammar, unparseable token, unreachable policy engine, expired anything → deny. There is no permissive mode and no fallback that grants access.
- **The `act` chain in minted tokens must always nest, never overwrite.** A test asserting this against `CONTRACT.md` §6 ships in Slice 1 and is never deleted.
- **The impersonation guard is unconditional.** A resource SDK rejects with HTTP 401 any token whose `sub` does not match the subject_token's `sub`. See `CONTRACT.md` §6.3.
- **Authorization decisions use top-level `sub` + outermost `act.sub` only.** Inner `act` entries are evidence and audit material, never authorization input. Encoded in the resource SDK; documented in `CONTRACT.md` §6.2.

If a slice's `requirements.md` touches a path that interacts with these constraints, the constraint appears verbatim as one of its acceptance criteria. If you cannot satisfy a constraint, surface it rather than working around it.

## Stack pins

- **Languages:** Go (latest stable) for the data plane; Python 3.12 for control plane, SDKs, and demo apps
- **OIDC server:** `github.com/zitadel/oidc/v3` (`/pkg/op`) — has working server-side RFC 8693 token-exchange grant. *Critical pitfall:* the exchange returns opaque tokens unless `requested_token_type=urn:ietf:params:oauth:token-type:jwt` is set. Always set it; assert it in tests.
- **Workload API:** `github.com/spiffe/go-spiffe/v2` (v2.4+) for Go; `pyspiffe` for Python (verify currency at Slice 2; raw gRPC fallback documented)
- **SPIRE:** latest stable, single trust domain `bonafide.local`, `x509pop` node attestor in dev
- **Vault:** 1.21+. Native SPIFFE auth method primary path; Vault JWT auth + SPIRE OIDC discovery is the documented fallback
- **Policy:** `github.com/open-policy-agent/opa/rego` embedded as a Go library (from Slice 4)
- **Postgres:** 16
- **JWT signing:** Ed25519 throughout. Go side uses `github.com/go-jose/go-jose/v4` (pulled in transitively by zitadel/oidc, promoted to direct in T-06). Python side uses **PyJWT ≥ 2.10** with the `cryptography` extra — `python-jose` does not support EdDSA at the JWS layer and is *not* on this stack (see `agent-notes.md` 2026-06-04).
- **Container topology:** Docker Compose, one file

Ask before adding any dependency not on this list.

## Working agreements

- **Specs before code, always.** No back-filling specs after the fact. The `spec-writer` agent produces `requirements.md` for review; design and tasks are written and reviewed before any code lands.
- **Thin vertical slices.** End-to-end working flow before any one component is "done." Each slice extends `scripts/smoke.sh` by one block and that block must pass before the slice is considered complete.
- **One commit per completed task.** Git history tells the story. Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`).
- **Update `agent-notes.md` when an agent fails an interesting way before patching.** Half the value of this project is seeing failure modes clearly. Note the symptom, the diagnosis, and the fix — in that order.
- **`CONTRACT.md` is sacred.** Any change to a wire format defined there is a breaking change unless it strictly relaxes an existing constraint. Slice `design.md`s that propose a wire change update `CONTRACT.md` in the same PR.
- **Keep `docs/` up to date.** Architecture, identity-flow, and trust-chain diagrams live in `docs/`. A slice that changes the picture updates the diagrams in the same PR.
- **Delete compiled binaries after build/test.** Never leave `authz`, `agent`, or any Go output binary as an untracked file. Remove immediately after the step that produced them.
- **The `act-chain` builder is the most important code in the project.** It lives at `services/authz/internal/exchange/act_chain.go` from Slice 1. Later slices extend its tests (depth-2 in Slice 6) but never replace the function. Any change to it requires a parallel update to the table-driven tests against `CONTRACT.md` §6.1 examples.
- **Don't widen the surface unnecessarily.** This is an MVP. Don't add endpoints, claims, env vars, or container ports not required by an approved slice. Half-finished implementations are worse than missing ones.

## Out of scope for the MVP

Multi-trust-domain federation; active token revocation (rely on TTLs); behavioural monitoring or anomaly detection; a real human login UI (CLI-issued JWT throughout); multi-tenancy; high availability or clustering; persistent agent registry beyond a single Postgres table; CI / deployment automation; secrets rotation beyond SPIRE/Vault defaults.

Each of these is genuinely out of scope, not deferred to "later." When the MVP wraps, deciding which of these to take on becomes its own planning exercise.

## When something feels wrong

Stop and surface it. Examples:
- A library forces opaque tokens when the protocol requires JWT — flag it; do not work around.
- The policy gate would accept an unmodelled scope shape — flag it; do not extend the grammar quietly.
- A slice would require touching `CONTRACT.md` for a non-trivial reason — call it out and update `CONTRACT.md` first.
- A dependency requires an Enterprise license at runtime that we expected to be Community — flag it; fall back to the documented contingency path; do not silently swap implementations.

The product's value rests on a small number of guarantees in `PRODUCT.md` §"Design Principles" and `CONTRACT.md` §6. Any time work in flight risks one of those guarantees, stop.

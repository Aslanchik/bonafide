# bonafide

A security-first, agent-native identity provider. Authenticates both human and non-human (AI agent) identities and treats the *human → agent → sub-agent → tool* delegation chain as a first-class, signed primitive (RFC 8693 nested `act` claim). Workload identity is SPIFFE-rooted; secrets come from Vault via SPIFFE-authenticated short-lived leases. No static long-lived secrets; TTL-only revocation in MVP. Personal learning project.

## What it does (in one paragraph)

A human authenticates via OIDC. They delegate a narrow task to an agent. The agent — which has its own SPIFFE identity from the org's SPIRE deployment — calls the bonafide token-exchange endpoint, presenting the user's JWT as `subject_token` and its own JWT-SVID as `actor_token` (RFC 8693). bonafide checks policy, then mints a short-lived task token whose `sub` is the human (unchanged) and whose `act` claim names the agent. Sub-agents repeat the exchange and the `act` claim nests rather than overwrites — the chain is read inside-out from a single decoded JWT. The agent authenticates to Vault with its SPIFFE identity and pulls a short-lived dynamic database credential. The downstream resource validates the task token, reads the act chain, authorizes against `sub` + outermost `act.sub`, and the audit layer reconstructs the full delegation chain on demand.

## Running locally

Once Slice 1 lands:

```bash
./scripts/bootstrap.sh   # docker compose up + idempotent post-up wiring
./scripts/smoke.sh       # the cumulative end-to-end acceptance test
```

Prerequisites: Docker (only). Everything else runs in containers.

## Reading order

| Document | What's in it |
|---|---|
| [`PRODUCT.md`](./PRODUCT.md) | Who it's for, why it exists, design principles |
| [`DESIGN.md`](./DESIGN.md) | System architecture, services, trust topology, TTL budget |
| [`CONTRACT.md`](./CONTRACT.md) | Every wire format — token claims, exchange request/response, scope grammar, audit event shape |
| [`CLAUDE.md`](./CLAUDE.md) | Spec-driven workflow, safety constraints, stack pins |
| `specs/<slice>/` | Per-slice requirements / design / tasks |
| `agent-notes.md` | Failure-mode log — what went wrong while building |

## Project status

In bootstrap. No slices have shipped yet. The slice order is `token-exchange-core` → `spire-workload-identity` → `vault-spiffe-auth` → `opa-policy-engine` → `audit-persistence` → `subagent-nesting`.

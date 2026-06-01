# vault-spiffe-auth: Requirements

## Overview

This slice removes the last static-secret stub from the bonafide hot path. The agent SDK no longer reads a fixed value out of Vault KV; instead it authenticates to Vault with its SPIFFE identity (issued by SPIRE in the previous slice, `spire-workload-identity`) and pulls a short-lived dynamic Postgres credential from Vault's database secrets engine. The calendar app stops carrying any database credential of its own and receives one per request from the agent. Vault's audit log becomes the third independent record of who acted: every credential fetch is attributable to a `spiffe://bonafide.local/agent/*` identity, never to a long-lived token. The slice also pins a documented contingency: if the native Vault SPIFFE auth method requires Enterprise at runtime, the slice swaps to Vault JWT auth backed by SPIRE's OIDC discovery without changing the agent SDK's external surface. This slice unblocks `opa-policy-engine`.

---

## VSA-1: Vault SPIFFE auth method bound to the agent SPIFFE ID grammar

A Vault auth method authenticates callers by their SPIFFE identity and admits only identities that match the agent role of the trust domain.

**Acceptance criteria:**
- A login attempt presenting a SPIFFE identity whose URI matches `spiffe://bonafide.local/agent/{name}` (per `CONTRACT.md` §1) succeeds and yields a Vault token scoped to that identity.
- A login attempt presenting any SPIFFE identity whose URI does not match `spiffe://bonafide.local/agent/{name}` — including `human/*`, `service/*`, and identities outside the `bonafide.local` trust domain (per `CONTRACT.md` §1) — is rejected.
- The Vault auth method is bound to the same trust domain (`bonafide.local`) declared in `CONTRACT.md` §1; identities issued by any other trust domain are rejected.
- A login attempt that presents no SPIFFE identity, an expired SPIFFE credential, or a malformed SPIFFE credential is rejected.

---

## VSA-2: Vault database secrets engine enabled for Postgres

Vault is configured to issue short-lived Postgres credentials against the calendar database.

**Acceptance criteria:**
- The database secrets engine is enabled and configured against the Postgres instance that backs the calendar fixture.
- The engine is configured with a Postgres role whose privileges are sufficient to read the calendar fixture and no more (no write, no admin).
- The engine can issue a credential on demand and that credential authenticates to Postgres as a freshly-created role.
- Disabling the engine, or making Postgres unreachable from Vault, causes credential issuance to fail closed; no cached or fallback credential is returned.

---

## VSA-3: `calendar_reader` Vault role with TTL ceiling

A Vault role gates the database-secrets-engine path that the agent uses; its lease lifetime is bounded by the project TTL budget.

**Acceptance criteria:**
- The role's issued lease has `exp - iat ≤ 300` seconds (5 minutes), matching the Vault DB lease ceiling in `DESIGN.md` §4.
- The role is configured so that issued leases cannot be renewed (renewal attempts are denied).
- The role grants only the `calendar_reader` Postgres privileges defined by VSA-2; no other database, schema, or role is reachable through it.
- A credential whose lease has expired is rejected by Postgres on the next use; the agent is forced to fetch a new lease.

---

## VSA-4: Agent SDK fetches DB credentials over SPIFFE-authenticated Vault calls

The agent SDK replaces the prior static KV read with a SPIFFE-authenticated dynamic credential fetch.

**Acceptance criteria:**
- The SDK obtains its caller identity from the SPIRE Workload API (introduced in `spire-workload-identity`) and uses it to authenticate to Vault; no Vault token, root token, or other credential is read from disk, environment, or build artifact.
- The SDK requests a credential from the `calendar_reader` role defined by VSA-3 and surfaces that credential to its caller for use within the lease lifetime.
- The SDK never persists, caches across process restart, or reuses a credential past its returned lease expiry; expiry is honored strictly with no leeway, matching the agent SDK behaviour described in `DESIGN.md` §4.
- If the SDK cannot obtain a SPIFFE identity, cannot authenticate to Vault, or cannot fetch a lease, it returns an error to its caller and does not return a credential.

---

## VSA-5: Calendar app holds no database credential

The calendar app receives its Postgres credential per request from the agent and carries no DB credential of its own.

**Acceptance criteria:**
- The calendar container image and its runtime environment contain no Postgres username, password, connection string with embedded credential, or other database secret in source, environment variables, mounted files, or image layers.
- For each incoming request, the calendar app uses only the database credential supplied by the calling agent for that request; it does not fall back to any built-in, default, or pre-configured credential.
- A request that arrives without an accompanying valid database credential is rejected; the calendar app does not open a database connection on its own behalf.
- The calendar app does not retain the supplied credential beyond the request that supplied it; subsequent requests must each carry their own credential.

---

## VSA-6: Vault audit log records the SPIFFE identity of every credential fetch

Vault's audit log is the third independent record of delegation, complementing the task token's `act` chain (`CONTRACT.md` §6) and the authz audit event (`CONTRACT.md` §9).

**Acceptance criteria:**
- Every successful credential issuance on the `calendar_reader` role produces a Vault audit log entry that names the calling SPIFFE identity (`spiffe://bonafide.local/agent/{name}` per `CONTRACT.md` §1) as the authenticated principal.
- No credential issuance in this slice is attributable in Vault's audit log to a root token, a long-lived operator token, or any non-SPIFFE principal.
- Every rejected login attempt (per VSA-1) also produces a Vault audit log entry distinguishing it as a failure.
- The audit log entries are readable end-to-end without decrypting any operator secret beyond the audit log's own integrity material.

---

## VSA-7: Revoking the agent's SPIRE registration breaks the next Vault call (fail-closed end-to-end)

Removing the agent's right to a SPIFFE identity must, within the TTL budget, cut off its ability to fetch Vault credentials.

**Acceptance criteria:**
- After the agent's SPIRE registration is removed and any previously-issued SPIFFE credential has expired (per the JWT-SVID and Vault DB lease ceilings in `DESIGN.md` §4), the next Vault login attempt by the agent is rejected.
- After such a rejection, the agent SDK returns an error to its caller; it does not return a stale credential, a cached credential, or a credential obtained by any alternative authentication path.
- After such a rejection, the calendar app receives no usable credential from the agent and returns an error to the requestor; it does not open a database connection on the agent's behalf.
- The Vault audit log records the rejection (per VSA-6) and attributes it to the absence of a valid SPIFFE identity.

---

## VSA-8: Contingency path — Vault JWT auth via SPIRE OIDC discovery preserves the SDK surface

If Vault's native SPIFFE auth method requires Enterprise at runtime, the slice swaps to the documented fallback without changing what the agent SDK exposes.

**Acceptance criteria:**
- The slice operates in one of two configurations: (a) Vault's native SPIFFE auth method bound per VSA-1, or (b) Vault's JWT auth method configured against SPIRE's OIDC discovery document, in either case admitting only identities matching `spiffe://bonafide.local/agent/{name}` (`CONTRACT.md` §1).
- The contingency configuration enforces the same binding, TTL ceiling (`DESIGN.md` §4), and audit-log attribution (per VSA-6) as the primary configuration; the trust domain and identity grammar do not change.
- The agent SDK's external surface — the set of inputs callers provide and outputs callers receive — is identical in both configurations; callers cannot tell which mode is active from the SDK's API.
- The choice of configuration is recorded in operator-readable form so that the audit log entries from VSA-6 and the registration-revocation behaviour from VSA-7 can be interpreted unambiguously.

---

## VSA-9: Smoke check extension

The cumulative end-to-end smoke check is extended by a block that exercises this slice and must pass before the slice is considered complete.

**Acceptance criteria:**
- The smoke check exercises an end-to-end flow in which the agent fetches a `calendar_reader` lease over a SPIFFE-authenticated Vault call (VSA-4), passes it to the calendar app (VSA-5), and the calendar app serves the request using only that lease.
- The smoke check verifies that the Vault audit log entry for the fetch names the agent's SPIFFE identity per `CONTRACT.md` §1 (VSA-6).
- The smoke check verifies the fail-closed behaviour of VSA-7: with the agent's SPIRE registration removed and any prior credential expired, the same end-to-end flow fails and no calendar response containing fixture data is returned.
- The smoke check passes in both the native-SPIFFE-auth and JWT-auth-via-OIDC configurations of VSA-8 without changes to the agent SDK invocation.

---

## Safety acceptance criteria

Non-negotiable. Lifted verbatim from `CLAUDE.md` "Safety constraints" for every constraint this slice touches.

- **All credentials short-lived.** TTL ceilings in `DESIGN.md` §4 (user JWT ≤ 15 min, task token ≤ 5 min, JWT-SVID ≤ 5 min, Vault DB lease ≤ 5 min). No code path may extend these.
- **No static long-lived secrets** in source, env files, or container images outside SPIRE and Vault. The agent SDK never reads a credential from disk.
- **Fail closed.** Missing `actor_token`, unknown agent, unknown scope grammar, unparseable token, unreachable policy engine, expired anything → deny. There is no permissive mode and no fallback that grants access.

---

## Out of scope

Explicit list of what this slice does NOT include:

- **OPA Rego policy engine** — owned by `opa-policy-engine`. This slice does not introduce, replace, or alter the policy gate; the authz server's policy gate continues to behave as it did in `spire-workload-identity`.
- **Postgres-backed audit persistence and chain reconstruction** — owned by `audit-persistence`. This slice does not persist Vault audit entries to Postgres, does not extend the control plane's audit ingest, and does not implement `GET /audit/chain/{event_id}` (`CONTRACT.md` §10).
- **Depth-2 `act` nesting / sub-agent delegation** — owned by `subagent-nesting`. This slice does not exercise or extend the nested-`act` rule beyond what `CONTRACT.md` §6 already requires of prior slices.
- **Post-MVP polish: chaining SPIRE under Vault PKI as UpstreamAuthority** is explicitly NOT in this slice. The single root of trust remains SPIRE's CA per `DESIGN.md` §3.
- **Active revocation of leases.** Revocation in this slice is by TTL expiry only, consistent with `PRODUCT.md` design principle 6 ("TTLs are the floor of safety, not the ceiling") and `DESIGN.md` §4.
- **JWKS rotation cadence, key rotation, multi-trust-domain federation, high availability, and persistent agent-registry storage beyond what prior slices already ship** — all out of scope for the MVP per `CLAUDE.md`.

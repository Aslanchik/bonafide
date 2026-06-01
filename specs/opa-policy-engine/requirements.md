# opa-policy-engine: Requirements

## Overview

This slice replaces the hard-coded Go policy map shipped in `token-exchange-core` with a real, file-backed policy engine: Open Policy Agent's Rego, embedded in the authz server as a library. The policy gate becomes the single decision point that the exchange handler consults before minting a task token, and its denial reason becomes the canonical `error_description` on a rejected exchange and the `policy_reason` on an audit event. This slice builds on `vault-spiffe-auth` (which proved short-lived secrets end-to-end) and unblocks `audit-persistence` (which will swap the still-file-backed audit emitter for a Postgres-backed one). After this slice, four of the five MVP stubs from `token-exchange-core` have been replaced with real components; only the audit store remains a file.

---

## OPE-1: Embedded policy engine

The authz server evaluates every token-exchange request against a policy expressed in Rego, evaluated in-process at request time.

**Acceptance criteria:**
- The authz server starts only if it can successfully load and parse a Rego policy at a configured path; if the file is missing, unreadable, or fails to parse, startup fails with a non-zero exit code and an error log identifying the policy path.
- No token-exchange request proceeds to the `act`-chain builder until the policy engine has returned a decision for that request.
- The policy engine evaluates each request in-process, with no network call to an external policy server during the mint hot path.

---

## OPE-2: Rego input contract

The exchange handler passes the policy engine a stable, documented input document. The shape of that document is the input contract slice `design.md` is expected to freeze.

**Acceptance criteria:**
- For every token-exchange request, the policy engine receives an input document containing exactly these named fields: `subject`, `subject_claims`, `actor`, `actor_claims`, `scope`, `audience`, `existing_chain`.
- `subject` is the SPIFFE ID from the subject_token's `sub` per `CONTRACT.md` §4; `actor` is the SPIFFE ID from the actor_token's `sub` per `CONTRACT.md` §1; `scope` is the unmodified `scope` parameter from the request per `CONTRACT.md` §7; `audience` is the unmodified `audience` parameter from the request per `CONTRACT.md` §7.
- `existing_chain` is the ordered list of SPIFFE IDs reconstructed from the subject_token's `act` claim per `CONTRACT.md` §6.1, outermost-first, and is an empty list when the subject_token carries no `act` claim.
- `subject_claims` and `actor_claims` carry the validated claims of the subject_token and actor_token respectively; neither is ever populated from an unverified token.
- The input document is identical for every evaluation of a given request; the engine never sees a field absent on one evaluation and present on another.

---

## OPE-3: Rego output contract

Every policy evaluation returns a small, fixed-shape decision document that the exchange handler interprets without ambiguity.

**Acceptance criteria:**
- The policy returns a document with exactly three fields: `allowed` (boolean), `scope_grant` (string), `reason` (string).
- When `allowed` is `true`, `scope_grant` is a single scope value matching the grammar in `CONTRACT.md` §2 and `reason` is the empty string.
- When `allowed` is `false`, `scope_grant` is the empty string and `reason` is a non-empty short identifier suitable for inclusion in an OAuth `error_description` per `CONTRACT.md` §7 and in an audit event's `policy_reason` per `CONTRACT.md` §9.
- The exchange handler never inspects any field other than these three; an unexpected extra field does not affect the decision and a missing required field is treated as a denial per OPE-4.
- The `scope` set on a minted task token (`CONTRACT.md` §5) is exactly `scope_grant`; the handler does not synthesize, broaden, or narrow it.

---

## OPE-4: Fail-closed semantics

The policy gate denies on every form of malformed input, malformed policy, or unexpected output. There is no permissive path.

**Acceptance criteria:**
- An unparseable Rego policy at startup prevents the authz server from listening; a Rego policy that becomes unparseable on reload (see OPE-7) causes the previously loaded good policy to remain in force and a denial is **not** silently introduced for in-flight requests, but a new bad policy is never adopted.
- A policy that does not define the required output rule, or returns a document missing `allowed`, `scope_grant`, or `reason`, results in HTTP 400 with `error=access_denied` per `CONTRACT.md` §7 and an audit event with `outcome="denied"` and a non-null `policy_reason` per `CONTRACT.md` §9.
- A `scope` parameter that does not match the grammar in `CONTRACT.md` §2 is denied before policy evaluation with `error=invalid_scope` per `CONTRACT.md` §7; the policy is not consulted for syntactically invalid scopes.
- An agent that is not registered for the requested scope is denied by policy with a stable `reason` value, and that `reason` appears verbatim in both the HTTP response `error_description` per `CONTRACT.md` §7 and the audit event's `policy_reason` per `CONTRACT.md` §9.
- A request whose `existing_chain` length plus the new hop would exceed the configured cap (OPE-5) is denied by policy with `reason="chain_too_deep"` per `CONTRACT.md` §6.1.
- If the policy gate is unreachable from the exchange handler's point of view for any reason, the request is denied; no code path grants access in the absence of an explicit `allowed: true`.

---

## OPE-5: Configurable chain-depth cap

A maximum delegation chain depth is enforced as a policy rule, with a default value and an operator-tunable override.

**Acceptance criteria:**
- The system exposes a configuration knob `max_chain_depth` whose default value is `4`.
- The chain-depth check is performed inside the policy (Rego), not in the Go handler; the handler's only role is to pass `existing_chain` per OPE-2 and to honour the returned `allowed`/`reason`.
- A request that would mint a token whose total chain depth (the new hop plus `existing_chain`) exceeds `max_chain_depth` is denied with `reason="chain_too_deep"` per `CONTRACT.md` §6.1.
- A request that would mint a token whose total chain depth equals `max_chain_depth` is allowed (the cap is inclusive of the new hop).
- Changing `max_chain_depth` does not require recompiling the authz binary.

---

## OPE-6: Control-plane policy and decision-trace introspection

The control plane exposes read-only endpoints that let an operator see what policy is currently loaded and why a specific past exchange was denied.

**Acceptance criteria:**
- The control plane exposes a read-only endpoint that returns the text of the Rego policy currently loaded by the authz server.
- The control plane exposes a read-only endpoint that, given an exchange's `event_id` per `CONTRACT.md` §9, returns the denial trace for that exchange: at minimum the `policy_reason` value and the input document that was evaluated.
- The decision-trace endpoint returns 404 for an `event_id` that does not exist; it returns the same `policy_reason` string that appears in the corresponding audit event per `CONTRACT.md` §9 and, for `outcome="minted"` events, indicates that no denial trace exists.
- Neither endpoint accepts a write; both reject any method other than `GET`.
- Neither endpoint exposes the contents of `subject_claims` or `actor_claims` fields that are not already present in the audit event per `CONTRACT.md` §9.

---

## OPE-7: Operator policy update mechanism

The system documents a mechanism by which an operator can update the loaded policy without dropping in-flight token-exchange requests.

**Acceptance criteria:**
- The system documents — in this slice's `design.md` — a single, named mechanism by which an operator updates the policy in the authz server (e.g. signal-driven reload, restart-only, control-plane push). The mechanism is one explicit choice, not a list.
- Whichever mechanism is chosen, requests that have entered the exchange handler before the update is applied complete against the policy that was in force at the moment they entered; no request is evaluated against a mix of old and new policy.
- A failed update (an unparseable replacement policy) does not unload the previously good policy; the authz server continues to serve requests using the prior policy and surfaces the failure on a non-blocking log.
- The mechanism does not introduce any new wire format not defined in `CONTRACT.md`; if it would, this slice halts and `CONTRACT.md` is updated first.

---

## OPE-8: Smoke check — forbidden scope is denied with the policy reason

The cumulative smoke harness gains a new block that exercises a denial path end-to-end and asserts the reason surfaces correctly on the wire.

**Acceptance criteria:**
- The smoke harness includes an exchange request for a scope that is syntactically valid per `CONTRACT.md` §2 but not permitted to the calling agent by the loaded policy.
- The authz server returns HTTP 400 with a JSON body conforming to `CONTRACT.md` §7, where `error="access_denied"` and `error_description` is exactly the `reason` string returned by the policy.
- The audit event emitted for the denied request has `outcome="denied"`, `scope_granted=null`, and `policy_reason` equal to the same `reason` string, per `CONTRACT.md` §9.
- The prior slice's smoke blocks (allowed exchange ending in a successful resource call) continue to pass; this slice's block is additive, not a replacement.

---

## Safety acceptance criteria

The following constraints from `CLAUDE.md` apply to this slice and appear verbatim as acceptance criteria. They are non-negotiable.

- **Fail closed.** Missing `actor_token`, unknown agent, unknown scope grammar, unparseable token, unreachable policy engine, expired anything → deny. There is no permissive mode and no fallback that grants access.
- **The `act` chain in minted tokens must always nest, never overwrite.** The chain-depth cap added by this slice is enforced *in addition to* the nesting rule of `CONTRACT.md` §6.1; the policy gate never authorizes a request whose resulting `act` chain would violate §6.1, and the chain-depth check operates on the same chain shape §6.1 defines.
- **Authorization decisions use top-level `sub` + outermost `act.sub` only.** Inner `act` entries are evidence and audit material, never authorization input. Encoded in the resource SDK; documented in `CONTRACT.md` §6.2. The Rego input document of OPE-2 reflects this rule: `subject` is the top-level `sub`, `actor` is the outermost `act.sub` (the agent presenting the actor_token), and `existing_chain` is supplied as evidence only — policy rules may inspect it for the depth cap but must not use inner entries as the authorization principal.
- **All credentials short-lived per `DESIGN.md` §4 TTL budget.** The policy gate does not extend, override, or otherwise interact with the TTL ceilings on the user JWT (≤ 15 min) or the task token (≤ 5 min); a policy that returned a longer `expires_in` would have no effect and the handler caps the minted token's `exp` per `CONTRACT.md` §5 regardless of policy output.

---

## Out of scope

- **Postgres-backed audit storage.** The audit emitter still writes file-backed events in this slice; the swap to Postgres ingest is owned by `audit-persistence`. Denial events emitted by this slice conform to `CONTRACT.md` §9 so that `audit-persistence` can ingest them unchanged.
- **Demonstrating a depth-2 nested `act` chain in the smoke harness.** The chain-depth cap is *enforced* in this slice via `max_chain_depth`, but no slice yet *produces* an exchange whose `existing_chain` is non-empty; that demonstration is owned by `subagent-nesting`.
- **Rich, real-world policy bundles.** This slice ships exactly one example policy file (`policies/delegation.rego`) sufficient to prove the integration end-to-end: it encodes a small registration map (agent → permitted scopes), the `max_chain_depth` rule, and the output contract of OPE-3. Authoring production-grade policies, multi-file bundles, policy testing harnesses beyond the smoke check, and policy versioning are out of scope for the MVP.
- **A policy authoring UI on the control plane.** OPE-6 exposes only read-only introspection. Policy CRUD on the control plane is mentioned in `DESIGN.md` §2.2 as a future direction; no slice in the MVP owns it.
- **Active policy hot-reload semantics beyond the documented mechanism of OPE-7.** Coordinated multi-instance reload, signed policy bundles, and policy rollback are all out of scope for the MVP.

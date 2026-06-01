# subagent-nesting: Requirements

## Overview

This slice is the sixth and final MVP slice. It demonstrates depth-2 of the signed delegation chain end-to-end by splitting the single demo agent introduced in `token-exchange-core` into two distinct workloads — a planner agent and a tool agent — each with its own SPIFFE identity and SPIRE registration entry. The planner exchanges the user JWT for a planner-scoped task token; that task token (which now carries an `act` claim) becomes the `subject_token` of a second exchange where the tool presents its own JWT-SVID as `actor_token`, yielding a tool-scoped task token whose `act` nests the planner inside. The calendar resource, given a depth-2 token, exposes the full chain through the resource SDK but continues to authorize against only the top-level `sub` and the outermost `act.sub`. The audit event for the second hop populates `existing_chain` and `resulting_chain`, and the chain-reconstruction endpoint from `audit-persistence` returns three participants with `consistent: true` when cross-checked against the decoded token. Builds on `audit-persistence`. Unblocks nothing; the MVP is complete after this slice.

---

## SAN-1: Two distinct sub-agent workloads with distinct SPIFFE identities

The demo topology runs two separate agent workloads — a planner and a tool — each registered with SPIRE under its own SPIFFE ID.

**Acceptance criteria:**
- A planner workload runs in the compose stack and is issued the SPIFFE ID `spiffe://bonafide.local/agent/planner` by a SPIRE registration entry, per `CONTRACT.md` §1.
- A tool workload runs in the compose stack and is issued the SPIFFE ID `spiffe://bonafide.local/agent/tool` by a separate SPIRE registration entry, per `CONTRACT.md` §1.
- The two registration entries are bound to their respective workloads by selectors that pin both the container image and the Unix UID under which each workload runs.
- Each workload fetches its JWT-SVID exclusively via the Workload API socket mounted into its container; neither reads any actor credential from disk, environment, or container image layers.
- A workload that does not match the selectors of either registration entry receives no SVID and any code path requiring a JWT-SVID for that workload fails closed.

---

## SAN-2: Depth-1 exchange by the planner produces a planner-scoped task token

The planner exchanges the user JWT for a task token whose `act` claim names the planner at depth 1.

**Acceptance criteria:**
- The planner sends a token-exchange request to the authz token endpoint with `subject_token` equal to the user JWT, `actor_token` equal to the planner's JWT-SVID, and the full parameter set required by `CONTRACT.md` §7.
- The exchange returns HTTP 200 with a response body conforming to `CONTRACT.md` §8 and an `access_token` that is a JWT, per `CONTRACT.md` §8.
- The minted task token's `sub` equals the user JWT's `sub` byte-for-byte and is the human SPIFFE ID `spiffe://bonafide.local/human/{email}`, per `CONTRACT.md` §§1, 5, 6.1.
- The minted task token's `act.sub` equals `spiffe://bonafide.local/agent/planner` and the minted token's `act` contains no nested `act` field at this depth, per `CONTRACT.md` §6.1.
- The minted task token's `iat`, `exp`, and `jti` are present and `exp - iat` is less than or equal to 300, per `CONTRACT.md` §§3, 5 and `DESIGN.md` §4.
- The minted task token's header `alg` is `EdDSA` and its signature verifies against the authz JWKS, per `CONTRACT.md` §3.

---

## SAN-3: Depth-2 exchange by the tool nests the planner inside `act.act`

The tool exchanges the planner's task token for a tool-scoped task token whose `act` claim names the tool at the outermost layer and nests the planner inside.

**Acceptance criteria:**
- The tool sends a token-exchange request to the authz token endpoint with `subject_token` equal to the planner's task token from SAN-2, `actor_token` equal to the tool's JWT-SVID, and the full parameter set required by `CONTRACT.md` §7.
- The exchange returns HTTP 200 with a response body conforming to `CONTRACT.md` §8 and an `access_token` that is a JWT, per `CONTRACT.md` §8.
- The minted task token's `sub` equals the planner task token's `sub` byte-for-byte, which equals the original user JWT's `sub` byte-for-byte; the handler does not mutate `sub` across either hop, per `CONTRACT.md` §§5, 6.1.
- The minted task token's `act.sub` equals `spiffe://bonafide.local/agent/tool` and its `act.act` equals the entire `act` subtree carried by the planner task token (a single-actor object with `sub` equal to `spiffe://bonafide.local/agent/planner`), per `CONTRACT.md` §6.1.
- The minted task token's `act.act.sub` equals `spiffe://bonafide.local/agent/planner`, demonstrating that the nesting rule preserved the inner actor rather than overwriting it, per `CONTRACT.md` §6.1.
- The minted task token's `iat`, `exp`, and `jti` are present and `exp - iat` is less than or equal to 300; the depth-2 token's TTL ceiling is not relaxed relative to the depth-1 ceiling, per `CONTRACT.md` §§3, 5 and `DESIGN.md` §4.
- A test asserts that for the depth-2 mint, the new token's `act.act` equals the subject_token's `act` byte-for-byte (the act-chain builder nests, never overwrites), per `CONTRACT.md` §6.1.

---

## SAN-4: Impersonation guard remains enforced at depth 2

The impersonation guard defined in `CONTRACT.md` §6.3 continues to hold for depth-2 tokens both at mint time and at resource-validation time.

**Acceptance criteria:**
- For every accepted `subject_token` shape, including a depth-1 task token carrying a single-actor `act` claim, a test asserts that `mint(subject_token).sub == subject_token.sub`, per `CONTRACT.md` §6.3.
- The resource SDK rejects with HTTP 401 any depth-2 token whose `sub` does not equal the `sub` of the subject_token from which it was minted, per `CONTRACT.md` §6.3.
- The resource SDK rejects with HTTP 401 any depth-2 token whose `act` shape does not match the nesting structure required by `CONTRACT.md` §6.1.
- A rejection caused by the impersonation guard at depth 2 is recorded as an `impersonation_guard_triggered` event, per `CONTRACT.md` §6.3.

---

## SAN-5: Resource-side authorization at depth 2 uses only top-level `sub` and outermost `act.sub`

The calendar application, given a depth-2 token, authorizes against the human and the current actor only and treats inner `act` entries as evidence and audit material.

**Acceptance criteria:**
- A request to the calendar resource bearing the depth-2 task token from SAN-3 receives HTTP 200 when the tuple `(sub, current_actor, scope)` is permitted by the resource's policy, where `current_actor` equals `token.act.sub`, per `CONTRACT.md` §6.2.
- The resource SDK exposes the full decoded chain to the calendar application — current actor first, then prior actors in nesting order — with three entries: the human (`sub`), the tool (`act.sub`), and the planner (`act.act.sub`), per `CONTRACT.md` §§6, 6.2.
- The HTTP 200 response body from the calendar application reveals the human (`sub`), the current actor (`act.sub`, the tool), and the prior actor (`act.act.sub`, the planner) as evidence material; the response makes clear that the prior actor was not used as authorization input, per `CONTRACT.md` §6.2.
- A test asserts that flipping the inner `act.act.sub` from the planner to any other SPIFFE ID does not change the calendar application's authorization decision for the same `(sub, act.sub, scope)` tuple, per `CONTRACT.md` §6.2.
- Authorization decisions performed by the calendar application are a function of `(sub, current_actor, scope)` only; inner `act` entries are never used as authorization input, per `CONTRACT.md` §6.2.

---

## SAN-6: Audit event for the depth-2 mint carries `existing_chain` and `resulting_chain`

The audit event emitted for the second hop populates the chain fields defined in `CONTRACT.md` §9.

**Acceptance criteria:**
- The audit event for the planner's depth-1 exchange has `existing_chain` equal to the empty array `[]` and `resulting_chain` equal to `["spiffe://bonafide.local/agent/planner"]`, per `CONTRACT.md` §9.
- The audit event for the tool's depth-2 exchange has `existing_chain` equal to `["spiffe://bonafide.local/agent/planner"]` and `resulting_chain` equal to `["spiffe://bonafide.local/agent/tool", "spiffe://bonafide.local/agent/planner"]`, per `CONTRACT.md` §9.
- Both audit events have `subject` equal to `spiffe://bonafide.local/human/{email}` (the original human, never mutated), per `CONTRACT.md` §§6.1, 9.
- Both audit events have `actor` equal to the SPIFFE ID of the workload whose JWT-SVID was presented as `actor_token` on that hop (the planner for the depth-1 event, the tool for the depth-2 event), per `CONTRACT.md` §9.
- Each audit event's `event_id` equals the `jti` of the task token minted by that hop, per `CONTRACT.md` §9.
- Each audit event's `outcome` is `"minted"`, `scope_granted` is populated with a value matching `CONTRACT.md` §2, `token_jti` equals the minted token's `jti`, and `token_exp` equals the minted token's `exp` expressed as an RFC 3339 UTC timestamp, per `CONTRACT.md` §9.

---

## SAN-7: Chain reconstruction at depth 2 returns three participants and is consistent

The control plane's chain-reconstruction endpoint returns the full ordered participant list for the depth-2 exchange and cross-checks the audit-event source against the decoded token's `act` claim.

**Acceptance criteria:**
- A `GET` to `/audit/chain/{event_id}` on the control plane, with `event_id` equal to the `jti` of the depth-2 task token from SAN-3, returns HTTP 200 with a body conforming to `CONTRACT.md` §10.
- The response's `subject` equals `spiffe://bonafide.local/human/{email}` (the original human), per `CONTRACT.md` §§6.1, 10.
- The response's `actors` array equals `["spiffe://bonafide.local/agent/tool", "spiffe://bonafide.local/agent/planner"]` in that order — current actor first, then the prior actor — per `CONTRACT.md` §10.
- The response's `current_actor` equals `spiffe://bonafide.local/agent/tool`, per `CONTRACT.md` §10.
- The response's `reconstructed_from` includes both `"audit_event"` and `"token_act_claim"`, per `CONTRACT.md` §10.
- The response's `consistent` field is `true`; the chain derived from the audit event's `resulting_chain` equals the chain derived from decoding the minted token's `act` claim, per `CONTRACT.md` §10.
- A `GET` to `/audit/chain/{event_id}` for the depth-1 event from SAN-2 returns a response whose `actors` array has exactly one element (`["spiffe://bonafide.local/agent/planner"]`), demonstrating that the same endpoint reconstructs both depths correctly, per `CONTRACT.md` §10.

---

## SAN-8: Depth-2 chains respect the `max_chain_depth` cap

The policy-engine chain-depth cap introduced in `opa-policy-engine` continues to govern nested exchanges; depth 2 is permitted under the default cap and a hypothetical further hop that would exceed the cap is denied.

**Acceptance criteria:**
- The depth-2 exchange of SAN-3 succeeds with HTTP 200 because the resulting chain depth is less than or equal to the configured `max_chain_depth` cap, whose default is 4.
- A token-exchange request that would yield a chain whose depth exceeds the configured `max_chain_depth` cap is denied with HTTP 400, `error=access_denied`, and a denial reason of `chain_too_deep` recorded in the audit event's `policy_reason` field, per `CONTRACT.md` §§7, 9.
- The denial in the over-cap case occurs before any task token is minted; no token is issued and no `outcome="minted"` audit event is emitted for the denied request, per `CONTRACT.md` §9.
- The `max_chain_depth` cap is read from policy configuration; no code path in the exchange handler bypasses or overrides this cap.

---

## SAN-9: Smoke check exercises the depth-2 chain end-to-end

The cumulative smoke check is extended by exactly one block that exercises the SAN capabilities end-to-end against a freshly brought-up topology.

**Acceptance criteria:**
- The smoke check performs a depth-1 exchange in which the planner presents its JWT-SVID as `actor_token` and obtains a task token whose `act.sub` equals `spiffe://bonafide.local/agent/planner`, per `CONTRACT.md` §§6.1, 8.
- The smoke check performs a depth-2 exchange in which the tool presents the planner's task token as `subject_token` and its own JWT-SVID as `actor_token`, obtaining a task token whose `act.sub` equals `spiffe://bonafide.local/agent/tool` and whose `act.act.sub` equals `spiffe://bonafide.local/agent/planner`, per `CONTRACT.md` §6.1.
- The smoke check calls the calendar resource with the depth-2 task token and asserts that the response body reveals the human, the tool as current actor, and the planner as prior actor while the authorization decision was a function of `(sub, current_actor, scope)` only, per `CONTRACT.md` §6.2.
- The smoke check fetches `GET /audit/chain/{event_id}` for the depth-2 event and asserts that `actors` equals `["spiffe://bonafide.local/agent/tool", "spiffe://bonafide.local/agent/planner"]`, `current_actor` equals `spiffe://bonafide.local/agent/tool`, `reconstructed_from` includes both `"audit_event"` and `"token_act_claim"`, and `consistent` equals `true`, per `CONTRACT.md` §10.
- The smoke check asserts that the audit event for the depth-2 hop has `existing_chain` equal to `["spiffe://bonafide.local/agent/planner"]` and `resulting_chain` equal to `["spiffe://bonafide.local/agent/tool", "spiffe://bonafide.local/agent/planner"]`, per `CONTRACT.md` §9.
- All prior smoke-check blocks from `token-exchange-core`, `spire-workload-identity`, `vault-spiffe-auth`, `opa-policy-engine`, and `audit-persistence` continue to pass unchanged.

---

## Safety acceptance criteria

The following constraints are lifted verbatim from `CLAUDE.md` "Safety constraints" and are non-negotiable for this slice. Implementations that violate any one of these fail the slice regardless of other criteria.

- **All credentials short-lived.** TTL ceilings in `DESIGN.md` §4 (user JWT ≤ 15 min, task token ≤ 5 min, JWT-SVID ≤ 5 min, Vault DB lease ≤ 5 min). No code path may extend these. Every hop in the depth-2 chain produces a task token whose `exp - iat` is at most 300 seconds, per `CONTRACT.md` §5.
- **Fail closed.** Missing `actor_token`, unknown agent, unknown scope grammar, unparseable token, unreachable policy engine, expired anything → deny. There is no permissive mode and no fallback that grants access. A depth-2 exchange whose `subject_token` is the planner's task token but whose `actor_token` is missing or malformed is denied, per `CONTRACT.md` §7.
- **The `act` chain in minted tokens must always nest, never overwrite.** A test asserting this against `CONTRACT.md` §6 ships in Slice 1 and is never deleted. This slice extends those tests to assert that the depth-2 mint's `act.act` equals the subject_token's `act` byte-for-byte, per `CONTRACT.md` §6.1.
- **The impersonation guard is unconditional.** A resource SDK rejects with HTTP 401 any token whose `sub` does not match the subject_token's `sub`. See `CONTRACT.md` §6.3. The guard remains active and is verified at depth 2 in SAN-4.
- **Authorization decisions use top-level `sub` + outermost `act.sub` only.** Inner `act` entries are evidence and audit material, never authorization input. Encoded in the resource SDK; documented in `CONTRACT.md` §6.2. The depth-2 calendar request in SAN-5 is authorized as a function of `(sub, current_actor, scope)` only, where `current_actor` equals `act.sub` (the tool); the inner `act.act.sub` (the planner) does not enter the decision.

---

## Out of scope

The following work is explicitly excluded from this slice and is owned either by no later slice (MVP-complete) or by post-MVP planning:

- **Demonstration of depth-3 and depth-4 chains.** The `max_chain_depth` cap defaults to 4 and the nesting rule already supports arbitrary depth via the recursion in `CONTRACT.md` §6.1, but this slice demonstrates only depth 2; depths 3 and 4 are legal under the cap but not exercised by the smoke check.
- **Rich policy rules over chain shape** (for example, "tool X may only act under planner Y," or "the inner actor must hold a particular scope"). The policy gate authorizes against `(sub, current_actor, scope)` per `CONTRACT.md` §6.2; chain-shape constraints beyond the depth cap are not modeled in the MVP.
- **Cross-trust-domain delegation.** The trust domain remains the single MVP domain `bonafide.local`, per `CONTRACT.md` §1 and `DESIGN.md` §3. Federation across trust domains is out of scope for the MVP.
- **Revoking an inner actor mid-chain.** Active revocation is not part of the MVP; TTLs are the floor of safety, per `PRODUCT.md` "Design Principles" and `DESIGN.md` §4. An inner actor's identity remains evidentially valid for the lifetime of any token in which it is nested; expiry of those tokens is the only revocation mechanism.
- **Multi-trust-domain federation, active token revocation, behavioural monitoring, multi-tenancy, high availability, and CI/deployment automation** — out of scope for the MVP per `CLAUDE.md` §"Out of scope for the MVP".

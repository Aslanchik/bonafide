# audit-persistence: Requirements

## Overview

This slice replaces the file-backed audit emitter shipped in `token-exchange-core` with a Postgres-backed audit pipeline owned by the control plane, and adds the delegation-chain reconstruction endpoint that gives bonafide its third independent record of every delegation hop. The authz server continues to decide and mint as before, but each successful or denied exchange now produces a structured audit event per `CONTRACT.md` §9 that is delivered asynchronously to the control plane, persisted into a `audit_events` row and a set of `delegation_edges` rows in a single transaction, and is queryable through `GET /audit/chain/{event_id}` per `CONTRACT.md` §10. The endpoint reconstructs the chain from both the persisted audit event and the decoded `act` claim of the minted token and reports whether the two sources agree. The slice builds on `opa-policy-engine` (denials now carry Rego reasons that round-trip into storage) and unblocks `subagent-nesting`, which will exercise depth-2 nesting against this same reconstruction surface.

---

## AUD-1: Persisted audit events round-trip every field of the `CONTRACT.md` §9 shape

The control plane persists every audit event posted by the authz server into a Postgres table whose contents preserve the wire shape.

**Acceptance criteria:**
- The control plane accepts audit events at `POST /audit/events` whose body matches the shape in `CONTRACT.md` §9 and persists each accepted event as exactly one row in an `audit_events` table.
- Every required field defined in `CONTRACT.md` §9 (`schema_version`, `event_id`, `occurred_at`, `outcome`, `issuer`, `subject`, `actor`, `existing_chain`, `audience`, `scope_requested`) is recoverable from the persisted row with the same value the authz server posted.
- Every conditional field defined in `CONTRACT.md` §9 (`resulting_chain`, `scope_granted`, `policy_reason`, `token_jti`, `token_exp`) is recoverable from the persisted row when it was present in the posted event and is absent or null when it was absent or null in the posted event.
- An event whose `event_id` is already present is not persisted a second time and does not produce a new row, satisfying the at-least-once delivery contract in `CONTRACT.md` §9.
- An event whose body does not match the shape in `CONTRACT.md` §9 is rejected and no row is written.

---

## AUD-2: Persisted delegation edges record one row per `(parent_actor, child_actor)` pair within an exchange

The control plane persists the delegation structure of every successfully minted exchange as a set of edge rows that serve as the canonical source for chain reconstruction.

**Acceptance criteria:**
- For every audit event with `outcome` equal to `"minted"`, the control plane writes one row to a `delegation_edges` table for each adjacent `(parent_actor, child_actor)` pair implied by the event's `resulting_chain` per `CONTRACT.md` §9.
- The set of edge rows written for a given `event_id` is sufficient to reproduce that event's `resulting_chain` in its original order without consulting any other source.
- The edge rows written for a given `event_id` reference that `event_id` and are retrievable by it.
- For an audit event whose `resulting_chain` contains only a single actor (a first-hop exchange with no prior actors), no `delegation_edges` rows are written and the `audit_events` row alone is sufficient to reconstruct the chain.
- For an audit event with `outcome` equal to `"denied"`, no `delegation_edges` rows are written, consistent with the absence of `resulting_chain` on denials per `CONTRACT.md` §9.

---

## AUD-3: The mint path never blocks on audit emission

The authz server's response to the token-exchange caller is decoupled from the success, latency, or availability of the control plane's audit ingest endpoint.

**Acceptance criteria:**
- A successful token-exchange returns the response defined in `CONTRACT.md` §8 to the caller regardless of whether the control plane has acknowledged the corresponding audit event.
- A denied token-exchange returns the error response defined in `CONTRACT.md` §7 to the caller regardless of whether the control plane has acknowledged the corresponding audit event.
- The wall-clock latency of any successful or denied token-exchange response is not measurably affected by the control plane being unreachable, slow to respond, or returning an error status to `POST /audit/events`.
- There is no code path in the authz server in which a failure to deliver an audit event to the control plane causes the token-exchange response to fail, change status, or change body shape.

---

## AUD-4: Authz buffers audit events locally and retries until the control plane accepts them

The authz server treats audit delivery as durable, at-least-once work that survives temporary control-plane outages and a single authz restart.

**Acceptance criteria:**
- An audit event for which `POST /audit/events` fails (connection refused, timeout, non-success status) is retained in a local buffer and re-attempted until the control plane accepts it, satisfying the at-least-once contract in `CONTRACT.md` §9.
- After the control plane becomes reachable again following an outage, every buffered audit event for every exchange decided during the outage is eventually delivered and persisted per `CONTRACT.md` §9.
- An audit event that the authz server has buffered but not yet delivered survives a single restart of the authz process and is delivered after the restart.
- The same audit event delivered more than once by the retry mechanism does not produce duplicate `audit_events` rows or duplicate `delegation_edges` rows, per AUD-1 and AUD-2.
- A buffered audit event whose delivery has not yet succeeded does not block the mint path of any subsequent token-exchange, per AUD-3.

---

## AUD-5: The `audit_events` row and its `delegation_edges` rows are written in a single transaction

The control plane never exposes a partially written audit record to a chain-reconstruction query.

**Acceptance criteria:**
- For any successfully persisted audit event, the `audit_events` row and every `delegation_edges` row implied by that event per AUD-2 either all exist or none exist.
- A failure during persistence of the `delegation_edges` rows results in no `audit_events` row being persisted for that `event_id`, and a subsequent retry of the same event per AUD-4 can complete the persistence.
- A `GET /audit/chain/{event_id}` request issued concurrently with the persistence of that `event_id` either returns the chain as if the event were already fully persisted (per `CONTRACT.md` §10) or returns the response shape used for an unknown `event_id`, but never returns a chain reconstructed from a partial row set.

---

## AUD-6: `GET /audit/chain/{event_id}` returns the response shape in `CONTRACT.md` §10

The control plane exposes a chain-reconstruction endpoint whose response matches the wire shape published in the contract.

**Acceptance criteria:**
- `GET /audit/chain/{event_id}` on the control plane returns HTTP 200 with a JSON body matching the shape in `CONTRACT.md` §10, including `event_id`, `subject`, `actors`, `current_actor`, `reconstructed_from`, `consistent`, `audience`, and `scope`, for any `event_id` that has been persisted via AUD-1.
- The `actors` array in the response is ordered with the current actor first followed by prior actors in delegation order, per `CONTRACT.md` §10.
- The `current_actor` field of the response equals the first element of the `actors` array, per `CONTRACT.md` §10.
- A `GET /audit/chain/{event_id}` request for an `event_id` that has never been persisted does not return a chain reconstructed from any other source.

---

## AUD-7: Chain reconstruction uses both the persisted audit event and the decoded token `act` claim and reports consistency

The chain endpoint cross-checks the canonical edge-derived reconstruction against the `act` claim of the minted token whenever both are available.

**Acceptance criteria:**
- When both the persisted audit event (and its `delegation_edges` rows per AUD-2) and the minted token's decoded `act` claim per `CONTRACT.md` §6 are available for an `event_id`, the response's `reconstructed_from` array per `CONTRACT.md` §10 contains both `"audit_event"` and `"token_act_claim"`.
- When both sources yield the same ordered participant list, `consistent` in the response is `true` per `CONTRACT.md` §10.
- When the two sources yield different ordered participant lists, `consistent` in the response is `false`, the response's `actors` array reflects the `audit_event` source as the authoritative one per `CONTRACT.md` §10, and the response includes a `discrepancy` field describing the mismatch per `CONTRACT.md` §10.
- The HTTP status code of a response with `consistent` equal to `false` is still 200, per `CONTRACT.md` §10.
- The `act` claim used in the cross-check is decoded according to the nesting rule in `CONTRACT.md` §6.1 (inside-out reading), so a depth-2 `act` chain yields the same ordered participant list as its corresponding `resulting_chain` per `CONTRACT.md` §9.

---

## AUD-8: Denied exchanges are persisted and produce a sensible chain response

Denial events are first-class records: they are persisted with full context and the chain endpoint returns a defined shape for them.

**Acceptance criteria:**
- An audit event with `outcome` equal to `"denied"` per `CONTRACT.md` §9 is persisted to `audit_events` with the same field-by-field round-trip guarantees as a minted event per AUD-1, including a non-null `policy_reason` and null `scope_granted`, `token_jti`, and `token_exp` per `CONTRACT.md` §9.
- `GET /audit/chain/{event_id}` for a denied event returns HTTP 200 with a body matching `CONTRACT.md` §10, in which `actors` reflects the actors known at the time of denial (the actor that attempted the exchange and any prior actors from `existing_chain` per `CONTRACT.md` §9).
- The response for a denied event has `reconstructed_from` containing only `"audit_event"`, since no minted token exists to provide an `act` claim, and `consistent` is `true` per `CONTRACT.md` §10.
- The denial reason persisted per AUD-1 is recoverable for a denied event through the chain endpoint or through direct query against the persisted row, so that callers can distinguish "denied" from "minted" without re-running the policy gate.

---

## AUD-9: Smoke check covers chain reconstruction and the consistency cross-check

The cumulative smoke check is extended by exactly one block that exercises the AUD capabilities end-to-end.

**Acceptance criteria:**
- The smoke check performs a token-exchange that results in a `minted` outcome and, after the audit event is delivered, calls `GET /audit/chain/{event_id}` for the resulting `event_id` and asserts the response shape matches `CONTRACT.md` §10.
- The smoke check asserts that the `actors` array returned by the endpoint, in order, equals the `resulting_chain` of the corresponding audit event per `CONTRACT.md` §9 and §10.
- The smoke check asserts that the `reconstructed_from` array of the response contains both `"audit_event"` and `"token_act_claim"` and that `consistent` is `true`, per `CONTRACT.md` §10.
- The smoke check asserts that the `current_actor` of the response equals the SPIFFE ID carried in the `act.sub` of the minted task token per `CONTRACT.md` §6.1 and §10.
- All prior smoke-check blocks from `token-exchange-core`, `spire-workload-identity`, `vault-spiffe-auth`, and `opa-policy-engine` continue to pass unchanged.

---

## Safety acceptance criteria

The following constraints are lifted verbatim from `CLAUDE.md` "Safety constraints" and apply to this slice without modification:

- **Fail closed.** Missing `actor_token`, unknown agent, unknown scope grammar, unparseable token, unreachable policy engine, expired anything → deny. There is no permissive mode and no fallback that grants access.
- **All credentials short-lived.** TTL ceilings in `DESIGN.md` §4 (user JWT ≤ 15 min, task token ≤ 5 min, JWT-SVID ≤ 5 min, Vault DB lease ≤ 5 min). No code path may extend these.
- **The `act` chain in minted tokens must always nest, never overwrite.** A test asserting this against `CONTRACT.md` §6 ships in Slice 1 and is never deleted.

The following slice-specific operational and security guarantees also apply without modification:

- **The mint path never blocks on audit.** No code path in the authz server allows audit delivery (success, failure, latency, or buffer state) to alter the wall-clock latency, status code, or body of a token-exchange response per `CONTRACT.md` §7 and §8.
- **No audit event is dropped under control-plane restart or transient outage.** Every event for every decided exchange is persisted exactly once given enough time, satisfying the at-least-once contract in `CONTRACT.md` §9 and surviving a single authz restart per AUD-4.
- **`consistent: false` is a security signal.** Per `CONTRACT.md` §10, a response in which the audit-event-derived chain and the token-`act`-derived chain disagree is treated as a security signal by callers; the endpoint surfaces the mismatch through the `discrepancy` field and the authoritative `actors` array reflects the `audit_event` source.

---

## Out of scope

The following work is explicitly excluded from this slice and is owned by a named later slice or is out of scope for the MVP:

- **Depth-2 `act`-chain nesting demonstration (sub-agent delegation)** — owned by `subagent-nesting`. This slice's chain reconstruction handles depth-2 `act` chains correctly per `CONTRACT.md` §6.1 and §10, but the demonstration of an actual depth-2 exchange (a sub-agent delegating onward) happens in `subagent-nesting`.
- **Audit retention and TTL policies.** Per `DESIGN.md` §4, audit event retention is indefinite in the MVP; truncation, archival, and TTL-based eviction of `audit_events` or `delegation_edges` rows are not implemented here.
- **Access control and authentication on the control-plane endpoints.** `POST /audit/events` and `GET /audit/chain/{event_id}` do not authenticate or authorize their callers in this slice; that hardening is out of scope for the MVP.
- **Active token revocation driven by audit signals.** The MVP relies on TTLs per `DESIGN.md` §4; no slice ships an active revocation path, and a `consistent: false` response does not trigger any revocation in this slice.
- **A user-facing audit query UI.** Only the `GET /audit/chain/{event_id}` JSON endpoint per `CONTRACT.md` §10 is in scope; no human-oriented dashboard or browsable log surface ships here.
- **Cross-trust-domain audit federation.** Multi-trust-domain federation is out of scope for the MVP per `CLAUDE.md` "Out of scope for the MVP", and the chain endpoint reconstructs chains only within the single `bonafide.local` trust domain.
- **A third reconstruction source (SPIRE registration metadata).** `CONTRACT.md` §10 leaves room for later slices to add a third source to `reconstructed_from`; this slice ships only `"audit_event"` and `"token_act_claim"`.

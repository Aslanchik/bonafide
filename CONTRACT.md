# bonafide Wire Contract

## Overview

This document is the authoritative reference for every byte that crosses a service boundary in bonafide: the token-exchange request and response, the JWT claims of every token type bonafide issues, the `act`-chain nesting rule, the SPIFFE ID grammar, the scope grammar, and the audit event shape. Every slice's `design.md` cites anchors from this document; consumers (the resource SDK, the calendar app, third-party integrators) read this document and nothing else to integrate.

Scope: wire formats and shared schemas only. Service topology and runtime behavior live in `DESIGN.md`; per-slice tasks live in `specs/<slice>/`.

---

## Standards pinned

| Reference | Version / anchor | Used by |
|---|---|---|
| **OAuth 2.0 Token Exchange** | RFC 8693 | The exchange request, response, and `act` claim |
| **JSON Web Token** | RFC 7519 | All tokens minted by bonafide |
| **OpenID Connect Core 1.0** | OIDC Core 1.0 | Discovery, JWKS, the eventual user login flow |
| **SPIFFE ID** | spiffe-id v1.0 | Every workload identity in the system |
| **SPIFFE JWT-SVID** | jwt-svid v1.0 | The `actor_token` from M2 onwards |

The product never invents a protocol where one of these applies. Where the standards are silent (the policy gate input, the audit event shape, the scope grammar), this document picks one explicit shape and freezes it.

---

## 1. Trust domain and SPIFFE ID format

**Trust domain (MVP):** `bonafide.local`. Single, non-federated.

**SPIFFE ID grammar:**

```
spiffe://bonafide.local/{role}/{name}
```

`{role}` ∈ `{ human, agent, service }`. `{name}` is a slug matching `[a-z0-9-]+`.

**Examples:**

```
spiffe://bonafide.local/human/alice@example.com
spiffe://bonafide.local/agent/planner
spiffe://bonafide.local/agent/calendar-tool
spiffe://bonafide.local/service/authz
spiffe://bonafide.local/service/calendar
```

Humans are SPIFFE-shaped for uniformity, but the human identity is never SPIRE-issued — it is constructed by the authz server when it mints the user JWT (Slice 1) or later by the OIDC login flow (post-MVP). Only `agent/*` and `service/*` SPIFFE IDs are issued by SPIRE.

---

## 2. Scope grammar

The policy gate accepts only scopes that match this grammar. Anything else is rejected with `error=invalid_scope` (Slice 1) or denied by Rego with `reason="unknown_scope"` (Slice 4+).

```
scope := <resource> ":" <verb> ":" <qualifier>

<resource> := [a-z][a-z0-9-]*
<verb>     := "read" | "write" | "admin"
<qualifier>:= <any-printable-no-colon-no-whitespace>
```

The `<qualifier>` is opaque to the protocol — its meaning is owned by the resource (e.g. for the calendar app it is the principal whose calendar is being accessed). The policy gate matches scopes character-for-character; there is no glob, no wildcard, no scope hierarchy.

**Examples (the only forms accepted by the MVP):**

```
calendar:read:alice@example.com
calendar:write:alice@example.com
calendar:admin:*
```

`*` is allowed only inside `<qualifier>`, never as `<resource>` or `<verb>`.

---

## 3. JWT claim shape — common

Every token bonafide mints (user JWT, task token) and every JWT-SVID bonafide consumes carries the following common claims. Per-token-type additions are in §4 and §5.

| Claim | Type | Required | Notes |
|---|---|---|---|
| `iss` | string | required | Authz: `https://authz.bonafide.local`; SVID: SPIRE Server's issuer URI |
| `sub` | string | required | A SPIFFE ID per §1 |
| `aud` | string \| string[] | required | Resource URL or audience name |
| `iat` | number | required | Unix seconds at mint time |
| `exp` | number | required | Unix seconds; per the TTL budget in `DESIGN.md` §4 |
| `jti` | string | required | UUIDv4; the audit `event_id` for task tokens |
| `nbf` | number | optional | Not before; defaults to `iat` if absent |

**Signature:** Ed25519 for everything bonafide mints. JWT-SVIDs are signed with whatever SPIRE issues (typically ECDSA P-256 or Ed25519, configurable at the server).

**`alg` header:** `EdDSA` for bonafide-minted tokens. Resource SDKs must reject `alg=none` regardless of JWKS contents.

---

## 4. User JWT (subject_token in the exchange)

Issued by the authz server's pre-signed CLI in Slice 1 and by the OIDC login flow post-MVP. Identifies the human.

| Claim | Required | Value |
|---|---|---|
| `iss` | required | `https://authz.bonafide.local` |
| `sub` | required | `spiffe://bonafide.local/human/{email}` |
| `aud` | required | `https://authz.bonafide.local` (the token's only legitimate target is the exchange endpoint) |
| `iat`, `exp`, `jti` | required | per §3; `exp - iat ≤ 900` (15 min ceiling per `DESIGN.md`) |
| `email` | optional | The user's email address, mirrored from `sub` for human-readability |

The user JWT must **not** carry an `act` claim. If it does, the exchange handler rejects the request with `error=invalid_request`, `error_description="subject_token must not carry act on first hop"`.

---

## 5. Task token (RFC 8693 mint output)

The product of a successful token-exchange. Bears the delegation chain. This is the only token format the resource SDK validates.

| Claim | Required | Value |
|---|---|---|
| `iss` | required | `https://authz.bonafide.local` |
| `sub` | required | **Unchanged from the subject_token's `sub`.** Always the human. The exchange handler is forbidden from mutating `sub`. |
| `aud` | required | The `audience` parameter from the exchange request; the URL of the target resource |
| `iat`, `exp`, `jti` | required | per §3; `exp - iat ≤ 300` (5 min ceiling) |
| `scope` | required | The `scope` granted by the policy gate. Single value matching §2. |
| `act` | required | The actor claim. Definition follows in §6. |
| `client_id` | optional | The SPIFFE ID of the agent that called the exchange endpoint; redundant with `act.sub` but kept for trace tooling |

The task token is always a JWT. The exchange endpoint must set `requested_token_type=urn:ietf:params:oauth:token-type:jwt` to force JWT (opaque is not acceptable for this product — the `act` claim is the entire point).

---

## 6. The `act` claim — definition and nesting rule

The `act` (actor) claim names the current actor and, by nesting, the entire signed delegation chain. This is the single most important schema in bonafide.

**Single-actor form (M1, no sub-agent):**

```json
{
  "sub": "spiffe://bonafide.local/human/alice@example.com",
  "act": {
    "sub": "spiffe://bonafide.local/agent/planner"
  }
}
```

Read: *alice is being acted for by the planner agent.*

**Nested form (M5, sub-agent delegation):**

```json
{
  "sub": "spiffe://bonafide.local/human/alice@example.com",
  "act": {
    "sub": "spiffe://bonafide.local/agent/tool",
    "act": {
      "sub": "spiffe://bonafide.local/agent/planner"
    }
  }
}
```

Read inside-out: *alice → delegated to planner → which delegated to tool. The current actor is tool.*

### 6.1 The nesting rule (normative)

When the exchange handler mints a new task token:

1. The new token's `sub` is exactly the subject_token's `sub`. **Never mutated.**
2. The new token's `act.sub` is the SPIFFE ID of the agent presenting the `actor_token`.
3. **If the subject_token carries an `act` claim**, the new token's `act.act` is set to the subject_token's `act` (the entire previous `act` subtree).
4. **If the subject_token does not carry an `act` claim**, the new token's `act` has no `act` field.

The chain depth is unbounded by RFC 8693 but is capped by policy (configurable, default `max_chain_depth = 4`) in Slice 4 onwards. Exceeding the cap is a `reason="chain_too_deep"` denial.

### 6.2 The resource-side authorization rule (normative)

A resource server authorizes a request against the token's `sub` and the *outermost* `act.sub`. Inner `act` entries are evidence and audit material, never authorization input.

Concretely: the resource's policy is a function of `(sub, current_actor, scope)` where `current_actor = token.act.sub`. The chain `[token.act.act.sub, token.act.act.act.sub, …]` does not affect access decisions; it only enters audit logs.

### 6.3 The impersonation guard (normative)

If a resource SDK receives a token whose `sub` does not equal the `sub` of the subject_token it was minted from (a violation impossible if the exchange handler obeys the nesting rule, but observable if a malicious party mints a token with a different code path), the resource SDK **rejects the request with HTTP 401** and logs an `impersonation_guard_triggered` event. The resource SDK enforces this by trusting only the authz JWKS and refusing tokens that lack the chain shape required by §6.1.

The exchange handler must include a unit test that asserts `mint(subject_token=user_jwt).sub == user_jwt.sub` for every `subject_token` shape it accepts.

---

## 7. Token-exchange request

`POST /token` on the authz server. `Content-Type: application/x-www-form-urlencoded`. Per RFC 8693 §2.1.

| Parameter | Required | Value |
|---|---|---|
| `grant_type` | required | `urn:ietf:params:oauth:grant-type:token-exchange` |
| `subject_token` | required | The JWT representing the human (the user JWT) |
| `subject_token_type` | required | `urn:ietf:params:oauth:token-type:jwt` |
| `actor_token` | required | The JWT-SVID representing the calling agent. Required from M2 onward. M1 accepts a self-signed JWT signed by an authz-trusted dev key. |
| `actor_token_type` | required | `urn:ietf:params:oauth:token-type:jwt` |
| `requested_token_type` | required | `urn:ietf:params:oauth:token-type:jwt` (mandatory — opaque tokens are not accepted) |
| `audience` | required | The target resource URL, e.g. `https://calendar.bonafide.local` |
| `scope` | required | A single scope per §2 |
| `resource` | optional | Mirrors `audience` when set; the policy gate ignores it if both are present |

Missing or mismatched `subject_token_type`/`actor_token_type`, missing `actor_token`, malformed JWTs, unknown scope grammar, audience not registered for the agent, expired tokens, and policy denials all return HTTP 400 with a JSON body conforming to RFC 6749 §5.2:

```json
{
  "error": "invalid_grant" | "invalid_request" | "invalid_scope" | "access_denied",
  "error_description": "<a short, non-PII description>"
}
```

The handler **never** responds with HTTP 5xx for a malformed request. 5xx is reserved for genuine server errors (DB down, key file unreadable).

---

## 8. Token-exchange response

Success: HTTP 200, `Content-Type: application/json`, body per RFC 8693 §2.2.

```json
{
  "access_token": "<signed JWT — the task token>",
  "issued_token_type": "urn:ietf:params:oauth:token-type:jwt",
  "token_type": "Bearer",
  "expires_in": 300,
  "scope": "calendar:read:alice@example.com"
}
```

The `access_token` is the task token defined in §5 — a JWT carrying the `act` claim of §6. `expires_in` matches the difference `exp - iat`; it is always ≤ 300 per the TTL budget.

The response never contains a refresh token. Per RFC 8693 and zitadel/oidc behaviour, refresh tokens are not supported with the token-exchange grant.

---

## 9. Audit event shape

Every successful exchange emits exactly one audit event. Every denied exchange also emits exactly one audit event (with `outcome="denied"` and a `reason`). The authz server POSTs these to the control plane at `POST /audit/events`, with at-least-once delivery and a local buffer for control-plane outages. The mint path never blocks on audit emission.

```json
{
  "schema_version": "1",
  "event_id": "01HX5Z2VQ8F9N0K2P4R6Y7T1S3",
  "occurred_at": "2026-05-31T18:42:11.482Z",
  "outcome": "minted" | "denied",
  "issuer": "https://authz.bonafide.local",
  "subject": "spiffe://bonafide.local/human/alice@example.com",
  "actor": "spiffe://bonafide.local/agent/planner",
  "existing_chain": [
    "spiffe://bonafide.local/agent/planner-parent"
  ],
  "resulting_chain": [
    "spiffe://bonafide.local/agent/planner",
    "spiffe://bonafide.local/agent/planner-parent"
  ],
  "audience": "https://calendar.bonafide.local",
  "scope_requested": "calendar:read:alice@example.com",
  "scope_granted": "calendar:read:alice@example.com",
  "policy_reason": null,
  "token_jti": "01HX5Z2VQ8F9N0K2P4R6Y7T1S3",
  "token_exp": "2026-05-31T18:47:11.482Z"
}
```

Field definitions:

| Field | Type | Required | Notes |
|---|---|---|---|
| `schema_version` | string | required | Bumped on any breaking change to this shape |
| `event_id` | string (ULID) | required | Same value as the minted token's `jti` |
| `occurred_at` | string (RFC 3339, UTC) | required | When the exchange was decided |
| `outcome` | `"minted"` \| `"denied"` | required | Denial events carry `policy_reason` and no `token_*` fields |
| `issuer` | string | required | Authz's `iss`; redundant but kept for log portability |
| `subject` | string (SPIFFE ID) | required | The `sub` of the subject_token |
| `actor` | string (SPIFFE ID) | required | The SPIFFE ID of the actor_token |
| `existing_chain` | string[] (SPIFFE IDs) | required | Inner actors from the subject_token's `act` chain, outermost-first; empty for first-hop exchanges |
| `resulting_chain` | string[] (SPIFFE IDs) | conditional | The full chain after this hop, current-actor-first. Present only on `minted`. |
| `audience` | string | required | The `audience` of the request |
| `scope_requested` | string | required | The `scope` requested |
| `scope_granted` | string \| null | conditional | The `scope` actually granted; `null` on denial |
| `policy_reason` | string \| null | conditional | The policy gate's denial reason (Slice 4+ uses Rego's `reason`); `null` on `minted` |
| `token_jti` | string \| null | conditional | The minted token's `jti`; `null` on denial |
| `token_exp` | string (RFC 3339) \| null | conditional | The minted token's `exp`; `null` on denial |

The `resulting_chain` is the canonical reconstruction source for §10. The control plane cross-checks `resulting_chain` against the decoded `act` of the minted token when both are available (Slice 5).

---

## 10. Delegation-chain reconstruction (control plane)

`GET /audit/chain/{event_id}` on the control plane returns the ordered participant list for a past exchange.

```json
{
  "event_id": "01HX5Z2VQ8F9N0K2P4R6Y7T1S3",
  "subject": "spiffe://bonafide.local/human/alice@example.com",
  "actors": [
    "spiffe://bonafide.local/agent/planner",
    "spiffe://bonafide.local/agent/planner-parent"
  ],
  "current_actor": "spiffe://bonafide.local/agent/planner",
  "reconstructed_from": ["audit_event", "token_act_claim"],
  "consistent": true,
  "audience": "https://calendar.bonafide.local",
  "scope": "calendar:read:alice@example.com"
}
```

`actors` is `[current_actor, ...prior_actors]`. `reconstructed_from` lists every source the control plane was able to use; `consistent` is `true` iff every source produced the same chain. Slice 5 requires both `audit_event` and `token_act_claim` to be checked when the token is in scope; later slices may add SPIRE registration metadata as a third source.

If `consistent` is `false`, the response status is still 200 but `actors` reflects the `audit_event` source (authoritative for audit) and the response carries a `discrepancy` field describing the mismatch. Callers MUST treat a `false` value as a security signal.

---

## 11. Health and discovery endpoints

The shape and content of `/healthz` and the OIDC discovery document (`/.well-known/openid-configuration`) are standard; no bonafide-specific extensions. The JWKS at `/.well-known/jwks.json` carries only Ed25519 keys. Keys rotated out of service remain in the JWKS until their `exp` (the latest token they could have signed) has passed.

---

## 12. Changing this contract

Any change to a wire format defined here is a breaking change unless it strictly relaxes a constraint already published (e.g. adding a new optional field, accepting a new value of an already-defined enum without removing old ones). Breaking changes bump `schema_version` on audit events and add a new endpoint version (e.g. `/v2/token`) rather than mutating the existing one. Slice `design.md`s that propose a wire change must update this document in the same PR.

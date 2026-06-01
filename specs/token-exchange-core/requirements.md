# token-exchange-core: Requirements

## Overview

This slice establishes the foundational, end-to-end delegation path in bonafide: a human (represented by a pre-signed user JWT) hands a scope to an agent, which exchanges that JWT at the authz server for a short-lived task token carrying a signed `act` claim, and then calls a downstream calendar resource that validates the token and reads the human's identity plus the outermost actor from the chain. Every later slice replaces one stubbed component (SPIRE-issued SVIDs, dynamic Vault leases, Rego policy, Postgres audit, depth-2 nesting) with its production form, but the wire shapes, the act-chain construction, the impersonation guard, the JWKS, the fail-closed policy gate, and the TTL ceilings are real in this slice. Nothing is built before it; it unblocks every subsequent slice.

---

## TEC-1: RFC 8693 token-exchange endpoint

The authz server exposes an OAuth 2.0 token endpoint that implements the RFC 8693 token-exchange grant and mints a JWT task token in response to a valid request.

**Acceptance criteria:**
- A `POST` to the authz token endpoint with `grant_type=urn:ietf:params:oauth:grant-type:token-exchange` and the parameter set defined in `CONTRACT.md` §7 (including `subject_token`, `subject_token_type`, `actor_token`, `actor_token_type`, `requested_token_type`, `audience`, `scope`) returns HTTP 200 with a JSON body conforming to `CONTRACT.md` §8.
- The response's `issued_token_type` equals `urn:ietf:params:oauth:token-type:jwt` and the response's `access_token` is a JWT (never opaque), per `CONTRACT.md` §8.
- The response's `token_type` is `Bearer`, `expires_in` equals the difference between the minted token's `exp` and `iat`, and `expires_in` is less than or equal to 300, per `CONTRACT.md` §§5, 8.
- The response's `scope` equals the scope granted by the policy gate and conforms to the grammar in `CONTRACT.md` §2.
- The response never contains a refresh token, per `CONTRACT.md` §8.
- A request missing `actor_token`, missing `subject_token_type`, missing `actor_token_type`, missing `requested_token_type`, missing `audience`, or missing `scope` returns HTTP 400 with a JSON body of the form `{ "error": ..., "error_description": ... }` per `CONTRACT.md` §7.
- A request with `requested_token_type` not equal to `urn:ietf:params:oauth:token-type:jwt` returns HTTP 400 with `error=invalid_request`, per `CONTRACT.md` §§7, 8.
- A request with a malformed JWT in `subject_token` or `actor_token`, an expired `subject_token` or `actor_token`, an unknown scope grammar, or an unregistered audience returns HTTP 400 with the appropriate `error` value drawn from `{invalid_grant, invalid_request, invalid_scope, access_denied}`, per `CONTRACT.md` §7.
- The endpoint never returns HTTP 5xx for a malformed request; HTTP 5xx is reserved for genuine server errors, per `CONTRACT.md` §7.

---

## TEC-2: User JWT minting CLI

A command-line utility mints the pre-signed user JWT that stands in for an OIDC-authenticated human throughout this slice.

**Acceptance criteria:**
- The CLI produces a JWT whose `iss` equals `https://authz.bonafide.local`, whose `sub` matches the form `spiffe://bonafide.local/human/{email}`, and whose `aud` equals `https://authz.bonafide.local`, per `CONTRACT.md` §§1, 4.
- The CLI produces a JWT whose `iat`, `exp`, and `jti` are present and whose `exp - iat` is less than or equal to 900, per `CONTRACT.md` §4 and the TTL budget in `DESIGN.md` §4.
- The CLI produces a JWT whose header `alg` is `EdDSA` and whose signature verifies against the authz JWKS, per `CONTRACT.md` §3.
- The CLI never emits a JWT carrying an `act` claim; the token-exchange endpoint rejects any user JWT carrying `act` with `error=invalid_request` and `error_description="subject_token must not carry act on first hop"`, per `CONTRACT.md` §4.

---

## TEC-3: Task token shape and act-chain construction at depth 1

The exchange handler mints a task token whose claims conform to `CONTRACT.md` §5 and whose `act` claim is constructed according to the §6.1 nesting rule.

**Acceptance criteria:**
- The minted task token's `sub` equals the subject_token's `sub` byte-for-byte; the handler never mutates `sub`, per `CONTRACT.md` §§5, 6.1.
- The minted task token's `act.sub` equals the SPIFFE ID encoded in the `actor_token`'s `sub`, per `CONTRACT.md` §6.1.
- When the subject_token carries no `act` claim (the only shape accepted by this slice), the minted task token's `act` contains exactly the field `sub` and no nested `act` field, per `CONTRACT.md` §6.1.
- The minted task token's `iss` equals `https://authz.bonafide.local`, its `aud` equals the `audience` parameter from the request, and its `scope` is a single value matching `CONTRACT.md` §2.
- The minted task token's `iat`, `exp`, and `jti` are present; `exp - iat` is less than or equal to 300; and `jti` is a UUIDv4, per `CONTRACT.md` §§3, 5.
- The minted task token's header `alg` is `EdDSA` and the signature verifies against the authz JWKS, per `CONTRACT.md` §3.
- A test asserts that `mint(subject_token=user_jwt).sub == user_jwt.sub` for every accepted subject_token shape, per `CONTRACT.md` §6.3.
- A test asserts that the `act` claim of the minted token nests (rather than overwrites) the subject_token's prior `act` subtree for every accepted subject_token shape, per `CONTRACT.md` §6.1.

---

## TEC-4: JWKS endpoint

The authz server publishes its JWT signing keys via a JWKS endpoint that the resource SDK and any third-party verifier can consume.

**Acceptance criteria:**
- A `GET` to the authz JWKS endpoint returns HTTP 200 with a JSON document containing only Ed25519 keys, per `CONTRACT.md` §11.
- The JWKS document contains every key whose signed tokens may still be within their `exp`, per `CONTRACT.md` §11.
- The signature on any task token minted by TEC-3 verifies against a key present in the JWKS document.
- A `GET` to the OIDC discovery endpoint returns a document that advertises the JWKS endpoint URL, per `CONTRACT.md` §11.

---

## TEC-5: In-memory policy gate (fail closed)

The exchange handler consults an in-process policy gate before minting; the gate is the only authority for whether a `(subject, actor, audience, scope)` tuple may be granted.

**Acceptance criteria:**
- A request whose `(subject, actor, audience, scope)` tuple matches a configured allow entry causes the handler to mint a task token whose `scope` claim equals the requested scope.
- A request whose tuple does not match any configured allow entry causes the handler to return HTTP 400 with `error=access_denied`, per `CONTRACT.md` §7.
- A request whose `scope` does not conform to the grammar in `CONTRACT.md` §2 causes the handler to return HTTP 400 with `error=invalid_scope`, per `CONTRACT.md` §§2, 7.
- A request whose `actor_token` is missing, malformed, expired, or signed by an untrusted key causes the handler to return HTTP 400 with the appropriate error code; no task token is minted.
- A request whose `audience` is not registered as a permitted target for the actor causes the handler to return HTTP 400 with `error=access_denied`, per `CONTRACT.md` §7.
- The policy gate has no permissive mode and no fallback path; any failure to evaluate the tuple is a denial.

---

## TEC-6: File-backed audit emitter

Every exchange decision — mint or denial — produces exactly one audit event whose shape conforms to `CONTRACT.md` §9.

**Acceptance criteria:**
- A successful exchange emits exactly one audit event with `outcome="minted"`, with `event_id` equal to the minted token's `jti`, with `subject` equal to the subject_token's `sub`, with `actor` equal to the actor_token's `sub`, with `existing_chain` equal to an empty array, with `resulting_chain` equal to a single-element array containing the actor's SPIFFE ID, and with `scope_granted`, `token_jti`, and `token_exp` populated, per `CONTRACT.md` §9.
- A denied exchange emits exactly one audit event with `outcome="denied"`, with `policy_reason` populated, with `scope_granted` set to `null`, and with `token_jti` and `token_exp` set to `null`, per `CONTRACT.md` §9.
- Every emitted audit event contains `schema_version`, `event_id`, `occurred_at` (RFC 3339 UTC), `issuer`, `subject`, `actor`, `existing_chain`, `audience`, and `scope_requested`, per `CONTRACT.md` §9.
- The mint path of the exchange handler does not block on audit emission; an audit emission failure does not change the HTTP response of the exchange endpoint.
- Audit events are durable across a restart of the authz process to the extent permitted by a file-backed sink (events emitted before the restart remain readable after it).

---

## TEC-7: Vault KV stub for resource credentials

A Vault instance, operating in development mode, exposes a static value via its KV secrets engine that the agent SDK reads and forwards to the calendar resource in lieu of a dynamic credential.

**Acceptance criteria:**
- The agent SDK reads the static value from the configured Vault KV path and uses it in the call to the calendar resource.
- No long-lived secret used to authenticate the agent SDK to Vault is committed to source, baked into a container image, or written to an environment file used at runtime; the SDK obtains its Vault token through a mechanism that does not require a static secret in source or image.
- A failure to read the configured KV path causes the agent SDK to abort the request before calling the calendar resource; no fallback credential path exists.

---

## TEC-8: Calendar resource application

A demo calendar application validates the incoming task token and serves a response that demonstrates the resource-side authorization rule.

**Acceptance criteria:**
- A request bearing a task token whose signature verifies against the authz JWKS, whose `iss` equals `https://authz.bonafide.local`, whose `aud` equals the calendar resource's URL, and whose `exp` has not passed receives an HTTP 200 response, per `CONTRACT.md` §§3, 5, 11.
- A request bearing a token whose signature does not verify, whose `iss` does not match, whose `aud` does not match, whose `exp` has passed, or whose header `alg` equals `none` receives an HTTP 401 response and is not served, per `CONTRACT.md` §3.
- The HTTP 200 response body reveals the value of the token's `sub` (the human) and the value of the outermost `act.sub` (the current actor) and reveals no other entries of the `act` chain as authorization input, per `CONTRACT.md` §6.2.
- Authorization decisions performed by the calendar application are a function of `(sub, current_actor, scope)` only; inner `act` entries are not used as authorization input, per `CONTRACT.md` §6.2.

---

## TEC-9: Resource SDK middleware and impersonation guard

The resource SDK provides a middleware that validates the task token, exposes the actor chain to the application, and unconditionally rejects tokens that violate the impersonation guard.

**Acceptance criteria:**
- The middleware fetches and caches the authz JWKS, validates the bearer token's signature, `iss`, `aud`, and `exp`, and rejects any token whose header `alg` is `none` regardless of JWKS contents, per `CONTRACT.md` §3.
- The middleware decodes the token's `act` claim and exposes the chain — current actor first — to the resource application, per `CONTRACT.md` §§6, 6.2.
- The middleware rejects with HTTP 401 any token whose `sub` does not equal the `sub` of the subject_token it was minted from, per `CONTRACT.md` §6.3.
- The middleware rejects with HTTP 401 any token whose shape does not match the chain structure required by `CONTRACT.md` §6.1.
- A rejection caused by the impersonation guard is recorded as an `impersonation_guard_triggered` event, per `CONTRACT.md` §6.3.
- The middleware applies `exp` strictly with no leeway, per `DESIGN.md` §4.

---

## TEC-10: Agent SDK pipeline

The agent SDK exposes a single client surface that drives the full pipeline from a presented user JWT to a served resource response.

**Acceptance criteria:**
- Given a user JWT, a target scope, and a target audience, the SDK performs the token-exchange against the authz endpoint with the parameter set required by `CONTRACT.md` §7 and obtains the resulting task token from the response body, per `CONTRACT.md` §8.
- The SDK presents an actor_token at the exchange endpoint; in this slice the actor_token is a self-signed JWT signed by a key the authz server trusts and whose `sub` is a SPIFFE ID conforming to `CONTRACT.md` §1.
- The SDK calls the calendar resource with the task token as a bearer credential and with the value read from the Vault KV stub forwarded as required by the resource.
- The SDK never reads a credential used for authentication from a path on disk that is part of the source tree or container image; all credentials in the pipeline are obtained at runtime.
- The SDK never re-uses any token, lease, or credential past its `exp`; expiry is enforced strictly with no leeway, per `DESIGN.md` §4.
- A failure at any pipeline step (exchange denial, Vault read failure, resource non-2xx) causes the SDK to abort the request and surface the failure; no permissive fallback exists.

---

## TEC-11: One-command bring-up and cumulative smoke harness

A single command brings the slice's containerized topology up from a clean state; a cumulative smoke harness asserts the end-to-end flow.

**Acceptance criteria:**
- A single command brings up the authz, control, calendar, agent, postgres, and vault containers listed for this slice in `DESIGN.md` §5 and performs idempotent post-up wiring so that the smoke harness can succeed against the resulting topology.
- The smoke harness's first block, executed against the brought-up topology, mints a user JWT via the CLI of TEC-2, exchanges it for a task token via the endpoint of TEC-1, calls the calendar resource via the agent SDK of TEC-10, and asserts that the calendar response reflects the human's `sub` and the agent's outermost `act.sub` as defined by `CONTRACT.md` §6.2.
- The smoke harness's first block asserts that an exchange request missing `actor_token` is rejected per `CONTRACT.md` §7 and that no task token is minted in that case.
- The smoke harness's first block asserts that a task token whose `sub` has been tampered with is rejected by the resource SDK with HTTP 401 per `CONTRACT.md` §6.3.
- The smoke harness's first block passes against a freshly brought-up topology with no manual steps between bring-up and harness execution.

---

## Safety acceptance criteria

The following constraints are lifted verbatim from `CLAUDE.md` §"Safety constraints" and are non-negotiable for this slice. Implementations that violate any one of these fail the slice regardless of other criteria.

- **All credentials short-lived.** TTL ceilings in `DESIGN.md` §4 (user JWT ≤ 15 min, task token ≤ 5 min, JWT-SVID ≤ 5 min, Vault DB lease ≤ 5 min). No code path may extend these.
- **No static long-lived secrets** in source, env files, or container images outside SPIRE and Vault. The agent SDK never reads a credential from disk.
- **Fail closed.** Missing `actor_token`, unknown agent, unknown scope grammar, unparseable token, unreachable policy engine, expired anything → deny. There is no permissive mode and no fallback that grants access.
- **The `act` chain in minted tokens must always nest, never overwrite.** A test asserting this against `CONTRACT.md` §6 ships in Slice 1 and is never deleted.
- **The impersonation guard is unconditional.** A resource SDK rejects with HTTP 401 any token whose `sub` does not match the subject_token's `sub`. See `CONTRACT.md` §6.3.
- **Authorization decisions use top-level `sub` + outermost `act.sub` only.** Inner `act` entries are evidence and audit material, never authorization input. Encoded in the resource SDK; documented in `CONTRACT.md` §6.2.

---

## Out of scope

The following capabilities are deliberately not part of this slice and are owned by named later slices:

- **SPIRE-issued JWT-SVIDs and X.509-SVIDs for workload identity** — owned by `spire-workload-identity`. In this slice the agent's `actor_token` is a self-signed JWT signed by a dev key the authz server trusts; SPIFFE IDs appear only as configuration strings.
- **Vault SPIFFE auth method and dynamic Postgres credentials via the database secrets engine** — owned by `vault-spiffe-auth`. In this slice Vault is used in development mode and serves a static value from the KV engine.
- **OPA Rego policy engine and a Rego input schema for the policy gate** — owned by `opa-policy-engine`. In this slice the policy gate is an in-process Go map.
- **Postgres-backed audit persistence and the `GET /audit/chain/{event_id}` reconstruction endpoint** — owned by `audit-persistence`. In this slice audit events are emitted to a file-backed sink and chain reconstruction is not exposed.
- **Depth-2 and deeper `act` nesting (sub-agent delegation)** — owned by `subagent-nesting`. In this slice the act chain is constructed at depth 1 only; the nesting rule is implemented but exercised only against subject_tokens that carry no `act` claim.
- **A real human OIDC login flow** — out of scope for the MVP. In this slice the human identity is represented by a pre-signed user JWT minted by the CLI.
- **JWKS key rotation, multi-trust-domain federation, active token revocation, behavioral monitoring, multi-tenancy, high availability, and CI/deployment automation** — out of scope for the MVP per `CLAUDE.md` §"Out of scope for the MVP".

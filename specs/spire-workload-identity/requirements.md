# spire-workload-identity: Requirements

## Overview

This slice introduces SPIRE Server and SPIRE Agent into the local-development topology, registers the three bonafide workloads (`agent`, `calendar`, `authz`) against a single trust domain, and replaces the dev-signed `actor_token` stub from `token-exchange-core` with a real SPIRE-issued JWT-SVID fetched over the Workload API. After this slice, every workload that participates in the token-exchange flow holds a SPIFFE identity whose authenticity can be verified end-to-end via SPIRE's JWT bundles, and the authz server's validation path for `actor_token` is no longer a dev-only shortcut. The slice unblocks `vault-spiffe-auth`, which will use the same JWT-SVID to authenticate to Vault.

---

## SWI-1: SPIRE Server and Agent in compose under the `bonafide.local` trust domain

The local-dev container set runs a single-trust-domain SPIRE deployment that issues SPIFFE identities conforming to the project SPIFFE ID grammar.

**Acceptance criteria:**
- The compose stack starts a `spire-server` container and a `spire-agent` container, both configured with trust domain `bonafide.local`.
- Every SPIFFE ID issued by SPIRE in this slice matches the grammar `spiffe://bonafide.local/{role}/{name}` where `{role}` ∈ `{ agent, service }` and `{name}` matches `[a-z0-9-]+`, per `CONTRACT.md` §1.
- No human identities are issued by SPIRE; the human SPIFFE ID continues to be constructed by the authz server when minting the user JWT, per `CONTRACT.md` §1.
- The SPIRE Server's signing CA is the single MVP root of trust; no other CA is configured as an upstream authority in this slice.
- A failed SPIRE Server or SPIRE Agent process causes the next token-exchange attempt that depends on a JWT-SVID to fail closed.

---

## SWI-2: Workload registration entries for `agent`, `calendar`, and `authz`

The SPIRE Server holds registration entries that issue exactly one SPIFFE identity per bonafide workload container.

**Acceptance criteria:**
- A registration entry exists that issues `spiffe://bonafide.local/agent/{name}` to the agent workload, per `CONTRACT.md` §1.
- A registration entry exists that issues `spiffe://bonafide.local/service/calendar` to the calendar workload, per `CONTRACT.md` §1.
- A registration entry exists that issues `spiffe://bonafide.local/service/authz` to the authz workload, per `CONTRACT.md` §1.
- Each registration entry is bound to its workload by selectors that pin both the container image and the Unix UID under which the workload runs.
- A workload that does not match the selectors of any registration entry receives no SVID and any code path requiring a JWT-SVID for that workload fails closed.
- Removing a workload's registration entry from SPIRE Server causes the next attempt by that workload to fetch a JWT-SVID via the Workload API to fail.

---

## SWI-3: Agent fetches a JWT-SVID via the Workload API and uses it as `actor_token`

The agent workload obtains its actor identity from SPIRE at exchange time and presents it as the `actor_token` parameter on the token-exchange request.

**Acceptance criteria:**
- Before every token-exchange request, the agent fetches a JWT-SVID from the Workload API socket mounted into its container.
- The fetched JWT-SVID is sent as the `actor_token` parameter of the token-exchange request, with `actor_token_type` set to `urn:ietf:params:oauth:token-type:jwt`, per `CONTRACT.md` §7.
- The JWT-SVID's `sub` claim equals the SPIFFE ID issued to the agent workload by its registration entry, per `CONTRACT.md` §1 and `CONTRACT.md` §3.
- The agent does not read any actor credential from disk, from an environment variable, or from a container image layer; the Workload API socket is the only source.
- If the Workload API is unreachable or returns no SVID for the agent's selectors, no token-exchange request is sent and the operation fails closed.

---

## SWI-4: Authz validates `actor_token` as a SPIRE-issued JWT-SVID via `FetchJWTBundles`

The authz server stops accepting the dev-signed `actor_token` from `token-exchange-core` and validates `actor_token` exclusively as a JWT-SVID using JWT bundles fetched from its own Workload API socket.

**Acceptance criteria:**
- The authz server fetches JWT bundles for the `bonafide.local` trust domain from its own Workload API socket and uses those bundles as the sole source of trust for `actor_token` signature verification.
- An `actor_token` whose `iss` does not equal the SPIRE Server's issuer URI for the `bonafide.local` trust domain is rejected with HTTP 400 and `error=invalid_request`, per `CONTRACT.md` §3 and `CONTRACT.md` §7.
- An `actor_token` whose signature does not verify against any current JWT bundle for `bonafide.local` is rejected with HTTP 400 and `error=invalid_request`, per `CONTRACT.md` §7.
- An `actor_token` whose `exp` is in the past is rejected with HTTP 400 and `error=invalid_request`, per `CONTRACT.md` §3 and `CONTRACT.md` §7.
- An `actor_token` whose `sub` is not a SPIFFE ID matching `spiffe://bonafide.local/{role}/{name}` per `CONTRACT.md` §1 is rejected with HTTP 400 and `error=invalid_request`.
- The dev-signed `actor_token` code path used in `token-exchange-core` is no longer accepted by the authz server after this slice.
- Failure to fetch JWT bundles from the Workload API causes every token-exchange request to fail closed; there is no fallback that accepts `actor_token` without bundle-based verification.

---

## SWI-5: Agent never caches a JWT-SVID past `exp`

The agent's actor-token lifecycle obeys the project's TTL budget without any local extension.

**Acceptance criteria:**
- The agent does not reuse a previously fetched JWT-SVID after the wall-clock time has reached its `exp`, per `CONTRACT.md` §3 and the TTL budget in `DESIGN.md` §4.
- The lifetime `exp - iat` of any JWT-SVID the agent presents as `actor_token` is at most 300 seconds (5 minutes), per the TTL budget in `DESIGN.md` §4.
- There is no agent code path that extends, refreshes in place, or otherwise prolongs a JWT-SVID beyond its issued `exp`.
- A token-exchange attempted with an expired JWT-SVID is rejected by the authz server, per `CONTRACT.md` §7, and the agent surfaces the failure rather than retrying with the same expired SVID.

---

## SWI-6: Calendar workload holds a SPIFFE identity via the Workload API

The calendar (resource) workload obtains a SPIFFE identity through SPIRE, even though no consumer in this slice requires it to present that identity at the wire.

**Acceptance criteria:**
- The calendar container mounts the Workload API socket and successfully fetches an SVID for `spiffe://bonafide.local/service/calendar`, per `CONTRACT.md` §1.
- The calendar workload does not read its identity from disk, environment, or image layers; the Workload API is the only source.
- Removing the calendar registration entry causes the calendar workload to receive no SVID on its next Workload API call.

---

## SWI-7: SPIRE OIDC discovery provider exposed

The SPIRE OIDC discovery provider is exposed at a known endpoint inside the local-dev topology so that the `vault-spiffe-auth` fallback path is reachable without a topology change.

**Acceptance criteria:**
- A SPIRE OIDC discovery document is served at a known endpoint inside the compose network, advertising the SPIRE Server's issuer URI for trust domain `bonafide.local`.
- A SPIRE JWKS is served at the discovery document's advertised JWKS URI and contains the current SPIRE JWT signing keys.
- The issuer URI advertised by the discovery document is the same string that appears in the `iss` claim of every JWT-SVID consumed in SWI-4, per `CONTRACT.md` §3.
- No bonafide workload consumes the discovery endpoint in this slice; its presence is verified only by serving the document and JWKS.

---

## SWI-8: Smoke check covers actor identity, issuer, and registration removal

The cumulative smoke check is extended by exactly one block that exercises the SWI capabilities end-to-end.

**Acceptance criteria:**
- The smoke check performs a token-exchange in which the agent's `actor_token` is a JWT-SVID fetched via the Workload API, and the exchange returns HTTP 200 with a task token per `CONTRACT.md` §8.
- The smoke check asserts that the `iss` claim of the `actor_token` equals the SPIRE Server's issuer URI for trust domain `bonafide.local`, per `CONTRACT.md` §3 and `CONTRACT.md` §7.
- The smoke check asserts that the `sub` claim of the `actor_token` is a SPIFFE ID matching `spiffe://bonafide.local/agent/{name}`, per `CONTRACT.md` §1.
- The smoke check, after removing the agent's workload registration entry from SPIRE Server, asserts that the next token-exchange attempt by the agent fails closed (no task token is returned).
- The smoke check asserts that the minted task token's `act.sub`, when an exchange does succeed, equals the SPIFFE ID carried in the `actor_token`'s `sub`, per `CONTRACT.md` §6.1.
- All prior smoke-check blocks from `token-exchange-core` continue to pass unchanged.

---

## Safety acceptance criteria

The following constraints are lifted verbatim from `CLAUDE.md` "Safety constraints" and apply to this slice without modification:

- **All credentials short-lived.** TTL ceilings in `DESIGN.md` §4 (user JWT ≤ 15 min, task token ≤ 5 min, JWT-SVID ≤ 5 min, Vault DB lease ≤ 5 min). No code path may extend these.
- **No static long-lived secrets** in source, env files, or container images outside SPIRE and Vault. The agent SDK never reads a credential from disk.
- **Fail closed.** Missing `actor_token`, unknown agent, unknown scope grammar, unparseable token, unreachable policy engine, expired anything → deny. There is no permissive mode and no fallback that grants access.
- **The `act` chain in minted tokens must always nest, never overwrite.** A test asserting this against `CONTRACT.md` §6 ships in Slice 1 and is never deleted.

---

## Out of scope

The following work is explicitly excluded from this slice and is owned by a named later slice:

- **Vault SPIFFE auth method and dynamic Postgres credentials** — owned by `vault-spiffe-auth`. This slice produces the JWT-SVID that `vault-spiffe-auth` will consume, but no Vault auth wiring lands here.
- **OPA Rego policy engine** — owned by `opa-policy-engine`. The policy gate remains the `token-exchange-core` implementation in this slice.
- **Postgres-backed audit log and delegation-chain reconstruction** — owned by `audit-persistence`. Audit emission shape is unchanged from `token-exchange-core`.
- **Depth-2 `act`-chain nesting (sub-agent delegation)** — owned by `subagent-nesting`. The single-actor `act` form from `CONTRACT.md` §6 remains the only shape minted in this slice.
- **Resource-side mutual-TLS using the calendar's SPIFFE identity at the wire.** The calendar holds a SPIFFE identity (SWI-6) but does not present it to clients via TLS in this slice.
- **Choice of Workload API client library or transport.** This slice does not decide between `pyspiffe` and a raw gRPC Workload API client; that decision is made in `specs/spire-workload-identity/design.md`.
- **Agent-SDK endpoint discovery mechanism** — listed as an open decision in `DESIGN.md` §6 and resolved in `specs/spire-workload-identity/design.md`, not here.

# Design

System architecture for bonafide. This document describes the runtime shape: the services, what they own, how they communicate, and the trust topology that ties them together. Wire-format details (exact JWT claims, exchange request shape, scope grammar, audit event shape) live in `CONTRACT.md`; SDD slice documents (`specs/<slice>/design.md`) cover how each slice realises a piece of this picture.

---

## 1. Component map

```
┌────────────────────┐                                 ┌──────────────────────┐
│  apps/demo-human   │  user JWT                       │   apps/demo-agent    │
│  (CLI; Slice 1+)   │ ──────────────────────────────► │   (Python; SDK)      │
└────────────────────┘                                 │                      │
                                                       │  1. fetch SVID       │
                                                       │  2. token-exchange   │
                                                       │  3. fetch Vault lease│
                                                       │  4. call resource    │
                                                       └──────────────────────┘
                                                                  │
        ┌───────────────────────┐                                 │
        │   spire-server +      │  Workload API (uds)             │
        │   spire-agent (M2+)   │ ◄──── JWT-SVID / X509-SVID ─────┤
        └───────────────────────┘                                 │
                                                                  │ (1, 3)
                                                  ┌───────────────┴────┐
                                                  │                    │
                                                  ▼                    ▼
                              ┌─────────────────────────┐  ┌────────────────────┐
                              │  services/authz (Go)    │  │   hashicorp vault  │
                              │  zitadel/oidc /pkg/op   │  │   1.21 (SPIFFE     │
                              │  - OIDC provider        │  │   auth method)     │
                              │  - RFC 8693 endpoint    │  │   - KV (M1)        │
                              │  - policy gate (OPA M4) │  │   - db engine (M3) │
                              │  - act-chain builder    │  │   - audit log      │
                              │  - JWKS                 │  └────────────────────┘
                              │  - audit emitter ───────┐
                              └─────────────────────────┘
                                                        │  audit events
                                                        ▼
                              ┌─────────────────────────────────────────┐
                              │  services/control (Python, FastAPI)     │
                              │  - agent registry                       │
                              │  - policy CRUD (write-through to disk)  │
                              │  - audit ingest + chain reconstruction  │
                              │  - postgres backed (M5)                 │
                              └─────────────────────────────────────────┘

                              ┌─────────────────────────────────────────┐
                              │  apps/demo-calendar (Python, FastAPI)   │
                              │  - sdks/resource-py middleware          │
                              │  - validates task token via JWKS        │
                              │  - reads postgres row using agent-      │
                              │    supplied dynamic credential          │
                              └─────────────────────────────────────────┘

                              ┌─────────────────────────────────────────┐
                              │  postgres 16                            │
                              │  - calendar fixture (all slices)        │
                              │  - audit_events / delegation_edges (M5) │
                              └─────────────────────────────────────────┘
```

The arrows in the diagram are the only inter-service traffic. There is no back-channel.

---

## 2. Services

### 2.1 `services/authz` — data plane (Go)

Owns the hot path. One binary. Built on `github.com/zitadel/oidc/v3` (`/pkg/op`).

**Endpoints:**
- `GET /.well-known/openid-configuration` — OIDC discovery
- `GET /.well-known/jwks.json` — JWT signing key set, consumed by the resource SDK and (in the fallback Vault path) by Vault JWT auth
- `POST /token` — the OAuth token endpoint, including the RFC 8693 token-exchange grant
- `POST /authorize` (M6 optional) — authorization code flow for the eventual real human login
- `GET /healthz`

**Internal packages** (illustrative; the actual package layout is fixed in `specs/token-exchange-core/design.md`):
- `internal/op` — zitadel/oidc OP storage and configuration
- `internal/exchange` — token-exchange handler; the `act-chain` builder lives here and is the single most-important file in the codebase
- `internal/policy` — policy gate interface; Slice 1 implementation is a hard-coded Go map, Slice 4 swaps it for embedded OPA Rego
- `internal/keys` — JWT signing key management; Ed25519, on-disk in MVP, JWKS rotation post-MVP
- `internal/audit` — structured audit emitter; HTTP POST to control plane with at-least-once retry; never blocks the mint path

**Mode of failure:** fail closed. The exchange handler refuses any request it cannot fully validate. Audit emission failures do not block the response (mints are emitted to a local buffer and retried) but they do raise a non-fatal log line.

### 2.2 `services/control` — control plane (Python, FastAPI)

Owns the cold path: registration, policy authorship, audit ingest, audit query.

**Endpoints:**
- `POST /agents` / `GET /agents/{id}` — agent registry
- `GET /policies` / `PUT /policies/{name}` — policy CRUD (write-through to `policies/*.rego` on disk in M1–M4; Postgres-backed agent metadata from M5)
- `POST /audit/events` — audit ingest from authz
- `GET /audit/chain/{event_id}` — reconstruct the delegation chain for a given exchange event (M5+)
- `GET /healthz`

**Storage:** in-memory from M1; Postgres-backed for agents + audit from M5.

The control plane is not in the request path. The authz server caches policy and agent metadata in memory and reloads on a signal (SIGHUP or a control-plane webhook — design decision deferred to Slice 4).

### 2.3 SDKs

**`sdks/agent-py` — Python agent SDK.**
One ergonomic client class that owns the full pipeline:

```python
client = BonafideAgent(
    authz_url="https://authz.bonafide.local",
    vault_addr="https://vault.bonafide.local",
    spiffe_socket="/run/spire/sockets/agent.sock",  # M2+
)
task_token = client.exchange(subject_jwt, scope="calendar:read:alice@example.com", audience="https://calendar.bonafide.local")
db_creds = client.fetch_lease("database/creds/calendar_reader")  # M3+
response = client.call("https://calendar.bonafide.local/events", task_token, db_creds=db_creds)
```

The SDK abstracts away which Vault auth method is in use; the slice that adds SPIRE (M2) and the slice that adds Vault SPIFFE auth (M3) both swap implementations without changing the surface.

**`sdks/resource-py` — Python resource-server middleware.**
A FastAPI dependency that:
1. Fetches and caches JWKS from the authz server.
2. Validates the bearer token's signature, `iss`, `aud`, `exp`.
3. Decodes the `act` claim and exposes a typed `ActorChain` on `request.state.actor_chain`.
4. Refuses tokens whose `sub` does not equal the subject_token's original `sub` (the impersonation guard, defined in `CONTRACT.md`).

### 2.4 Off-the-shelf

| Component | Version | Role |
|---|---|---|
| SPIRE Server + Agent | latest stable | Single trust domain `bonafide.local`. Issues JWT-SVIDs to workloads. `x509pop` node attestor in dev. |
| Vault | 1.21+ | Secrets backend. KV (M1), DB secrets engine for Postgres (M3). SPIFFE auth method (M3); fallback is JWT auth + SPIRE OIDC discovery. |
| Postgres | 16 | Calendar fixture (M1+); audit + delegation edges tables (M5). |

---

## 3. Trust topology

**Single trust domain:** `spiffe://bonafide.local`. Federation is out of scope for the MVP.

**Three issuers:**
1. **authz** — issues user JWTs (Slice 1) and task tokens (RFC 8693 exchange output). Signing key is Ed25519, on-disk in M1, exposed via JWKS.
2. **SPIRE Server** — issues SVIDs (JWT and X.509) to workloads. Workload identities are SPIFFE URIs `spiffe://bonafide.local/{role}/{name}`.
3. **Vault** — issues credentials (KV values, DB leases). Authenticates callers via its SPIFFE auth method (M3); the caller's SPIFFE ID is the audit subject in Vault.

**No issuer trusts another's signature implicitly.** The authz server validates `actor_token` JWT-SVIDs by calling SPIRE's `FetchJWTBundles` on its own workload socket; the resource SDK validates task tokens via the authz JWKS; Vault validates incoming SPIFFE identities through its configured trust roots.

**Single root of trust in MVP:** SPIRE's CA. (Post-MVP optional: chain SPIRE's intermediate under Vault PKI as UpstreamAuthority — chains X.509 trust, not JWT.)

---

## 4. TTL budget

The MVP relies on TTLs in lieu of active revocation. These are ceilings, not defaults. Slices may shorten them.

| Token / lease | Max lifetime |
|---|---|
| User JWT (M1 pre-signed; M6 OIDC login) | **15 min** |
| Task token (RFC 8693 mint output) | **5 min** |
| Agent JWT-SVID (SPIRE-issued) | **5 min** (SPIRE default; SDK never caches past `exp`) |
| Vault DB lease (`database/creds/*`) | **5 min** with no renewal |
| JWKS signing key validity | **24 h** in MVP; rotation post-MVP |
| Audit event retention | Indefinite in MVP; truncation policy out of scope |

The resource SDK respects `exp` strictly with no leeway. The agent SDK never re-uses any of the above past expiry; it always refetches.

---

## 5. Local development topology

One `docker-compose.yml` brings the whole system up. Reach end-state via `./scripts/bootstrap.sh` which composes the stack and performs idempotent post-up wiring (SPIRE workload registrations, Vault policy and engine bootstrap, Postgres seed verification). `./scripts/smoke.sh` is the cumulative end-to-end acceptance harness.

Containers (the set grows by slice):

| Container | Slice introduced | Notes |
|---|---|---|
| `authz` | TEC | Local Go build; mounts `policies/`, exposes 8080 |
| `control` | TEC | Local Python build; exposes 8090 |
| `calendar` | TEC | Local Python build; exposes 9000; mounts SPIRE workload socket from SWI onwards |
| `agent` | TEC | Local Python build; one-shot CLI; mounts workload socket from SWI |
| `postgres` | TEC | Calendar fixture row; audit tables added in AUD |
| `vault` | TEC | Dev mode with `-dev-root-token-id=devroot`; engines progressively enabled by `deploy/vault/bootstrap.sh` |
| `spire-server` | SWI | Trust domain `bonafide.local`; disk-keyed CA |
| `spire-agent` | SWI | `x509pop` node attestation; workload socket shared via named volume |

There is no separate control-plane DB; control plane uses the same Postgres instance in a different schema from M5.

---

## 6. Open decisions (deferred to slice `design.md`)

These are deliberately *not* decided here. They are listed so each is owned by a known slice.

| Decision | Owning slice |
|---|---|
| Exact Go package layout inside `services/authz` | TEC |
| Rego input schema for the policy gate | OPE |
| Agent-SDK endpoint discovery (env-driven vs. SPIRE federation registry) | SWI |
| Audit event Postgres schema (single table vs. events + edges) | AUD |
| JWKS rotation cadence and signal mechanism | (post-MVP) |
| Resource SDK error handling for stale JWKS | TEC |
| Token-exchange handler concurrency and rate-limit shape | TEC |

---

## 7. What this document does *not* cover

- **Wire format details** — token claims, exchange request/response, scope grammar, audit event JSON: see `CONTRACT.md`.
- **Per-slice implementation plans** — package layout, function signatures, task ordering: see `specs/<slice>/`.
- **Build / deploy tooling beyond local docker-compose** — out of scope for the MVP.
- **High availability / clustering, multi-tenancy, key rotation** — out of scope for the MVP.

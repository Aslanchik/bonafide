# token-exchange-core: Design

## Overview

The Go authz server is a single binary built on `github.com/zitadel/oidc/v3/pkg/op`, exposing the OIDC discovery document, the JWKS, and the OAuth token endpoint. The token endpoint accepts the RFC 8693 token-exchange grant; on a valid exchange it calls a small policy gate (an in-memory Go map in this slice), runs the act-chain builder, mints a JWT task token signed with Ed25519, and emits a structured audit event to a local file. The Python agent SDK drives the full pipeline (read a stub Vault KV → mint a self-signed actor_token → exchange → call the calendar) as one ergonomic client. The Python resource SDK is a FastAPI dependency that validates the task token against the authz JWKS, enforces the impersonation guard, and exposes the decoded act chain on `request.state`. The demo calendar app uses the resource SDK and reads a calendar fixture from Postgres using a static connection string supplied to it via the agent (the connection string is what the Vault KV stub holds in this slice; VSA replaces it with a dynamic credential). Everything brings up with `docker compose up` plus a small idempotent post-up script.

The dev-only shortcuts taken in this slice:

- The agent's `actor_token` is signed by a per-workload Ed25519 key on disk (mounted into the container). The authz server is configured with a static `{SPIFFE_ID → public_key_PEM}` map. In SWI this map is replaced by `FetchJWTBundles` against the SPIRE Workload API.
- The user JWT is minted by a CLI, not by an OIDC login flow.
- Vault runs in dev mode; the KV holds a static value.
- The policy gate is a hard-coded Go map; OPE replaces it with embedded Rego.
- Audit events go to an append-only file; AUD replaces this with Postgres.

The wire formats (CONTRACT.md §§5–9) are the **same** as every later slice — no slice ever changes them.

---

## Stack (locked for this slice)

### Go data plane — `services/authz`

| Concern | Choice | Why |
|---|---|---|
| Go version | latest stable (≥ 1.23) | |
| OIDC OP | `github.com/zitadel/oidc/v3` | RFC 8693 grant handler in `/pkg/op` |
| JWT signing | `github.com/go-jose/go-jose/v4` | Ed25519 + JWKS marshalling; zitadel/oidc uses it transitively |
| UUIDs | `github.com/google/uuid` | UUIDv4 for `jti` |
| HTTP server | `net/http` + `github.com/go-chi/chi/v5` for routing | chi keeps routing readable without pulling in a framework |
| Logging | `log/slog` (stdlib) | Structured logs; no extra dep |
| Testing | stdlib `testing` + `github.com/stretchr/testify` for assertions | Table-driven `act_chain_test.go` is the canonical test |
| YAML (M1 only) | `gopkg.in/yaml.v3` | Loading `actor-trust.yaml` and `policy.yaml` stubs in TEC. Already pulled in transitively by testify; promoted to direct in T-07. Deleted with the stubs in SWI / OPE. |

### Python control plane / SDKs / apps

| Concern | Choice | Why |
|---|---|---|
| Python | 3.12 | Per CLAUDE.md |
| Web | FastAPI + uvicorn | For control plane, calendar app, and a tiny health surface on demo-agent |
| Schemas | Pydantic v2 | |
| Settings | `pydantic-settings` | Env-based config |
| JWT | **`PyJWT` ≥ 2.10** with the `cryptography` extra | EdDSA sign + verify; consumes a JWKS dict via `PyJWK`. `python-jose` was the original choice but does not implement EdDSA — see `agent-notes.md` 2026-06-04. |
| HTTP client | `httpx` | Async-capable; used by agent SDK |
| Calendar DB | `asyncpg` | Async Postgres driver; calendar app reads one row per request |
| CLI | `typer` | The demo-human CLI |
| Tests | pytest + pytest-asyncio + httpx TestClient | |

Package managers: Go uses `go mod`; Python uses `uv` (one `pyproject.toml` per Python project — control, agent-py, resource-py, demo-agent, demo-calendar, demo-human all separate).

No frameworks beyond the above. No background workers. No Redis. No message queue. The slice is one HTTP service plus three Python processes plus Vault dev plus Postgres.

---

## Repo structure (this slice fills these in)

```
bonafide/
├── docker-compose.yml
├── deploy/
│   ├── spire-stub/                       # M1 only — per-agent dev key pairs; deleted by SWI
│   │   ├── agent-planner.key.pem
│   │   └── agent-planner.crt.pem
│   ├── vault/bootstrap.sh                # enable KV v2, write the calendar connection string
│   ├── postgres/init.sql                 # calendar fixture row
│   └── authz/
│       ├── signing.key                   # Ed25519 private key (dev-only, ignored by git)
│       └── actor-trust.yaml              # {SPIFFE_ID → public_key_PEM} map (M1 only)
├── services/
│   └── authz/
│       ├── go.mod / go.sum
│       ├── cmd/authz/main.go
│       └── internal/
│           ├── config/                   # env-driven Settings
│           ├── op/                       # zitadel/oidc OP wiring + custom Storage
│           ├── exchange/                 # RFC 8693 handler
│           │   ├── handler.go
│           │   ├── act_chain.go          # THE canonical nesting function
│           │   └── act_chain_test.go     # table-driven against CONTRACT.md §6.1
│           ├── policy/                   # in-memory policy.Gate interface + map impl
│           ├── keys/                     # Ed25519 signing key + JWKS publication
│           ├── trust/                    # actor_token issuer trust (M1 stub; SWI replaces)
│           ├── audit/                    # file-backed audit emitter
│           └── httputil/                 # chi router, error helpers, CORS for the demo
├── sdks/
│   ├── agent-py/
│   │   ├── pyproject.toml
│   │   └── bonafide_agent/
│   │       ├── __init__.py
│   │       ├── client.py                 # BonafideAgent
│   │       ├── identity.py               # stub: read the per-workload Ed25519 key & sign
│   │       └── errors.py
│   └── resource-py/
│       ├── pyproject.toml
│       └── bonafide_resource/
│           ├── __init__.py
│           ├── middleware.py             # FastAPI dependency
│           ├── jwks.py                   # JWKS fetch + cache
│           ├── chain.py                  # ActorChain type + decode
│           └── errors.py
├── apps/
│   ├── demo-human/
│   │   ├── pyproject.toml
│   │   └── demo_human/__main__.py        # typer CLI that mints + prints the user JWT
│   ├── demo-agent/
│   │   ├── pyproject.toml
│   │   └── demo_agent/__main__.py        # one-shot CLI that drives BonafideAgent
│   └── demo-calendar/
│       ├── pyproject.toml
│       ├── demo_calendar/main.py         # FastAPI app
│       └── tests/
└── scripts/
    ├── bootstrap.sh                      # docker compose up + post-up wiring + key generation
    └── smoke.sh                          # the TEC block; later slices append
```

---

## Configuration

Each service reads its config from environment variables. No flags, no config files (apart from `actor-trust.yaml` which is structured data, not config).

### `services/authz` (Go)

| Env var | Default | Meaning |
|---|---|---|
| `BONAFIDE_AUTHZ_LISTEN` | `:8080` | HTTP listen address |
| `BONAFIDE_AUTHZ_ISSUER` | `https://authz.bonafide.local` | OIDC `iss` value; same string used in minted tokens. Canonical per CONTRACT.md §§4, 5 — independent of the dev HTTP listener URL (see agent-notes.md 2026-06-04). |
| `BONAFIDE_AUTHZ_SIGNING_KEY_PATH` | `/etc/authz/signing.key` | Ed25519 PEM private key |
| `BONAFIDE_AUTHZ_ACTOR_TRUST_PATH` | `/etc/authz/actor-trust.yaml` | `{spiffe_id: pubkey_pem}` map (M1 stub) |
| `BONAFIDE_AUTHZ_POLICY_PATH` | `/etc/authz/policy.yaml` | In-memory policy table (M1 stub) |
| `BONAFIDE_AUTHZ_AUDIT_PATH` | `/var/log/bonafide/audit.log` | Append-only NDJSON |
| `BONAFIDE_AUTHZ_TASK_TOKEN_TTL_SECONDS` | `300` | Hard ceiling per DESIGN.md §4; enforced even if policy returned a longer value |

### `apps/demo-human` (Python CLI)

| Env var | Default | Meaning |
|---|---|---|
| `BONAFIDE_DEMO_HUMAN_SIGNING_KEY_PATH` | `/etc/authz/signing.key` | Same Ed25519 key the authz signs with (so the user JWT verifies against the same JWKS) |
| `BONAFIDE_AUTHZ_ISSUER` | `https://authz.bonafide.local` | Goes into `iss` (CONTRACT.md §4). Separate from `BONAFIDE_AUTHZ_TOKEN_URL` below — the issuer is a wire identifier, not a transport URL. |
| `BONAFIDE_USER_JWT_TTL_SECONDS` | `900` | 15-min ceiling per DESIGN.md §4 |

### `apps/demo-agent` and `sdks/agent-py`

| Env var | Default | Meaning |
|---|---|---|
| `BONAFIDE_AUTHZ_TOKEN_URL` | `http://authz.bonafide.local:8080/token` | RFC 8693 endpoint |
| `BONAFIDE_AGENT_SPIFFE_ID` | (required) | e.g. `spiffe://bonafide.local/agent/planner` |
| `BONAFIDE_AGENT_KEY_PATH` | (required) | per-agent Ed25519 private key |
| `BONAFIDE_AGENT_KID` | (required) | key ID used in actor_token JOSE header (matches authz's trust map) |
| `BONAFIDE_VAULT_ADDR` | `http://vault:8200` | |
| `BONAFIDE_VAULT_TOKEN` | `devroot` | M1 only; VSA replaces with SPIFFE auth |
| `BONAFIDE_VAULT_KV_PATH` | `secret/data/calendar/connection` | KV v2 path holding the stub credential |
| `BONAFIDE_CALENDAR_URL` | `http://calendar.bonafide.local:9000` | |
| `BONAFIDE_SCOPE` | `calendar:read:alice@example.com` | The scope demo-agent will request |

### `apps/demo-calendar` and `sdks/resource-py`

| Env var | Default | Meaning |
|---|---|---|
| `BONAFIDE_CALENDAR_LISTEN` | `0.0.0.0:9000` | |
| `BONAFIDE_AUTHZ_ISSUER` | `https://authz.bonafide.local` | Used for `iss` validation (CONTRACT.md §5). Transport URL is `BONAFIDE_AUTHZ_JWKS_URL` below. |
| `BONAFIDE_AUTHZ_JWKS_URL` | `${ISSUER}/.well-known/jwks.json` | |
| `BONAFIDE_RESOURCE_AUDIENCE` | `http://calendar.bonafide.local:9000` | Expected `aud` |
| `BONAFIDE_CALENDAR_DSN_HEADER` | `X-Bonafide-Connection` | Header the agent uses to forward the Vault stub value to calendar |
| `POSTGRES_HOST` | `postgres` | Connection details for the calendar fixture |
| `POSTGRES_DB` | `calendar` | |

`actor-trust.yaml` (M1 stub, deleted in SWI):

```yaml
# Per-agent dev public keys the authz server will accept as actor_token signers.
# Replaced by SPIRE Workload API + FetchJWTBundles in spire-workload-identity.
trusts:
  - spiffe_id: spiffe://bonafide.local/agent/planner
    kid: planner-dev-key-1
    public_key_pem: |
      -----BEGIN PUBLIC KEY-----
      ...
      -----END PUBLIC KEY-----
```

`policy.yaml` (M1 stub, deleted in OPE):

```yaml
# In-memory allow table. Tuples not present here are denied (fail-closed).
# Each row says: "{agent} acting for {subject_prefix} is allowed to request {scope} for {audience}."
allow:
  - actor: spiffe://bonafide.local/agent/planner
    subject_prefix: spiffe://bonafide.local/human/
    scope: calendar:read:alice@example.com
    audience: http://calendar.bonafide.local:9000
```

---

## Go data plane

### Package layout (resolves DESIGN.md §6 "Internal package layout")

```
services/authz/internal/
├── config/        — Settings struct, env loading
├── op/            — zitadel/oidc Storage impl, JWKS hookup
├── exchange/      — POST /token handler + act-chain builder (the heart)
├── policy/        — Gate interface + map impl; OPE swaps this
├── trust/         — actor_token issuer trust (M1 stub); SWI replaces with WorkloadAPI client
├── keys/          — signing key load + JWKS publication
├── audit/         — emitter interface + file impl; AUD swaps for HTTP emitter
└── httputil/      — chi router, RFC 6749 error helper
```

The `exchange` package depends only on `policy.Gate`, `trust.IssuerTrust`, `keys.Signer`, `audit.Emitter` (all interfaces). Every later slice swaps implementations behind these interfaces; the package itself is not touched after this slice — except for one append to `act_chain_test.go` in SAN.

### Key types

```go
// services/authz/internal/exchange/types.go

// TaskTokenClaims is the body of a minted task token (CONTRACT.md §5).
type TaskTokenClaims struct {
    Iss     string   `json:"iss"`
    Sub     string   `json:"sub"`           // SPIFFE ID; never mutated across exchange
    Aud     string   `json:"aud"`
    Iat     int64    `json:"iat"`
    Exp     int64    `json:"exp"`
    Jti     string   `json:"jti"`           // UUIDv4; same value as the audit event_id
    Scope   string   `json:"scope"`
    Act     *Act     `json:"act"`           // CONTRACT.md §6
    ClientID string  `json:"client_id,omitempty"` // SPIFFE ID of the calling agent
}

// Act is the act claim — recursive type implementing CONTRACT.md §6.1's nesting rule.
type Act struct {
    Sub string `json:"sub"`
    Act *Act   `json:"act,omitempty"`
}

// UserJWTClaims is the body of a subject_token a user CLI mints (CONTRACT.md §4).
type UserJWTClaims struct {
    Iss   string `json:"iss"`
    Sub   string `json:"sub"`               // spiffe://bonafide.local/human/{email}
    Aud   string `json:"aud"`               // the issuer's own URL
    Iat   int64  `json:"iat"`
    Exp   int64  `json:"exp"`
    Jti   string `json:"jti"`
    Email string `json:"email,omitempty"`
}

// ActorTokenClaims is the body of an actor_token presented at the exchange (M1 stub form).
// SWI replaces this with whatever shape FetchJWTBundles validates.
type ActorTokenClaims struct {
    Iss string `json:"iss"`
    Sub string `json:"sub"`                 // SPIFFE ID per CONTRACT.md §1
    Aud string `json:"aud"`                 // authz issuer URL
    Iat int64  `json:"iat"`
    Exp int64  `json:"exp"`
}

// PolicyInput is what the policy gate sees on every request.
// OPE-2 freezes this shape; OPE-1 swaps the implementation but keeps the input contract.
type PolicyInput struct {
    Subject       string
    SubjectClaims UserJWTClaims
    Actor         string
    ActorClaims   ActorTokenClaims
    Scope         string
    Audience      string
    ExistingChain []string  // outermost-first; empty when subject has no act
}

type PolicyDecision struct {
    Allowed    bool
    ScopeGrant string
    Reason     string  // populated only when Allowed is false
}
```

### The act-chain builder (the canonical function — CONTRACT.md §6.1)

```go
// services/authz/internal/exchange/act_chain.go
//
// BuildAct implements CONTRACT.md §6.1's nesting rule. It is the single most
// important function in the codebase. Any change here requires a parallel
// update to the table-driven tests in act_chain_test.go.
package exchange

// BuildAct returns the act claim for a newly minted task token.
//
//   currentActor: SPIFFE ID of the agent presenting actor_token on THIS exchange.
//   subjectAct:   the act claim carried by the subject_token, or nil if absent.
//
// Per §6.1:
//   - new.sub  = currentActor
//   - new.act  = subjectAct  (set the entire prior subtree, do not flatten it)
//
// The subject_token's sub is mutated elsewhere — never here. This function
// never reads or modifies the subject's identity.
func BuildAct(currentActor string, subjectAct *Act) *Act {
    return &Act{
        Sub: currentActor,
        Act: cloneAct(subjectAct), // defensive copy
    }
}

func cloneAct(a *Act) *Act {
    if a == nil {
        return nil
    }
    return &Act{Sub: a.Sub, Act: cloneAct(a.Act)}
}

// ChainDepth returns the number of actors in the chain (the new mint hop counts
// as 1; subjectAct contributes its own depth). Used by OPE-5's depth cap.
func ChainDepth(subjectAct *Act) int {
    depth := 1
    for a := subjectAct; a != nil; a = a.Act {
        depth++
    }
    return depth
}

// FlattenChain returns the chain as an ordered slice [current, prior1, prior2, ...]
// — current actor first. Used by audit emission (CONTRACT.md §9 resulting_chain).
func FlattenChain(act *Act) []string {
    out := []string{}
    for a := act; a != nil; a = a.Act {
        out = append(out, a.Sub)
    }
    return out
}
```

### Test plan for `act_chain_test.go` (the heart of every later slice's correctness)

Table-driven, with cases drawn from CONTRACT.md §6.1 examples:

| Case | subject act | currentActor | Expected new act | Notes |
|---|---|---|---|---|
| first hop, no prior | `nil` | `planner` | `{sub: planner}` (no nested act) | TEC happy path |
| depth-2 nest | `{sub: planner}` | `tool` | `{sub: tool, act: {sub: planner}}` | SAN happy path; tested at TEC time |
| depth-3 nest | `{sub: tool, act: {sub: planner}}` | `tool2` | `{sub: tool2, act: {sub: tool, act: {sub: planner}}}` | Asserts unbounded recursion |
| `FlattenChain` depth-2 | `{sub: tool, act: {sub: planner}}` | n/a | `[tool, planner]` | Audit-shape check |
| `ChainDepth` cap | `{sub: tool, act: {sub: planner}}` | n/a | `3` | OPE depth check |
| `BuildAct` defensive copy | input act mutated post-call | n/a | new act unaffected | Catches accidental aliasing |

The test must NEVER be deleted; SAN extends it with the depth-2 demo cases against real SPIFFE IDs.

### Exchange handler flow

```
POST /token (form-encoded)
  │
  ├── parse RFC 8693 params → return 400 invalid_request on missing/bad ones    (TEC-1)
  ├── enforce requested_token_type == "...:token-type:jwt"                       (TEC-1, CONTRACT.md §8)
  ├── decode subject_token + verify signature against authz signing key (it minted it) → 400 invalid_grant on failure
  ├── decode actor_token + verify signature using trust.IssuerTrust.Verify(...) → 400 invalid_request on failure  (M1 stub; SWI uses FetchJWTBundles)
  ├── reject if subject_token carries `act` (CONTRACT.md §4)                     (TEC-2)
  ├── extract existing_chain from subject_token's act (per §6.1, FlattenChain skipping the head)  ← always [] in TEC
  ├── policy.Gate.Decide(PolicyInput{...}) → if !Allowed, emit denied audit event, return 400 access_denied + reason  (TEC-5)
  ├── BuildAct(currentActor=actor_token.sub, subjectAct=subject_token.act)       (TEC-3)
  ├── construct TaskTokenClaims with sub = subject_token.sub (UNCHANGED)         (TEC-3, impersonation guard)
  ├── cap exp at iat + min(policy.ScopeGrant.TTL, BONAFIDE_AUTHZ_TASK_TOKEN_TTL_SECONDS)
  ├── sign with keys.Signer (Ed25519, alg=EdDSA), kid set in JOSE header        (TEC-3, CONTRACT.md §3)
  ├── emit minted audit event to audit.Emitter (file-backed)                    (TEC-5, CONTRACT.md §9)
  └── return 200 { access_token, issued_token_type, token_type, expires_in, scope }  (TEC-1, CONTRACT.md §8)
```

The handler **never** returns 5xx on a malformed request. 5xx is reserved for actual server-side faults (signing key unreadable, audit file unwritable, panic). Audit emission is **non-blocking**: write to a goroutine-fed buffered channel; emitter drains to file. A buffered-channel overflow logs at WARN and the request still returns 200.

### Concurrency / rate limit (resolves DESIGN.md §6)

- The handler is request-stateful but globally stateless beyond reading `policy.Gate`, `trust.IssuerTrust`, and signing-key state. All three are loaded at startup and (in this slice) never mutated.
- No rate limit in TEC. The MVP is single-process, single-tenant, and runs only against the demo workloads.
- Logged metrics per request (slog): `event=token_exchange outcome={minted,denied} subject=<sub> actor=<actor> scope=<scope> aud=<aud> duration_ms=<n>`.

### JWKS publication

`keys.Signer` loads one Ed25519 private key at startup. JWKS endpoint serves the corresponding public key with `kid` derived from the SHA-256 of the public key encoded as base64url (first 12 chars). On startup, if the signing key file is missing, the server exits non-zero (per the "fail closed" safety constraint).

### File-backed audit emitter (TEC-5)

NDJSON; one line per CONTRACT.md §9 event. Append-only `os.OpenFile(path, O_APPEND|O_CREATE|O_WRONLY, 0o600)`. Buffered channel size 256; a producer that finds the channel full logs `event=audit_buffer_full` at WARN but does not drop the event — it blocks for up to 100 ms, then drops with `event=audit_buffer_dropped` at ERROR. Drops are visible in metrics; the request still returns successfully.

The emitter is an interface (`audit.Emitter`); AUD swaps the file impl for an HTTP-to-control-plane impl behind it. The exchange handler does not change.

---

## Python agent SDK — `sdks/agent-py`

### The `BonafideAgent` client

```python
# sdks/agent-py/bonafide_agent/client.py
from dataclasses import dataclass

@dataclass(frozen=True)
class TaskToken:
    access_token: str          # the JWT
    expires_at: int            # absolute unix seconds; SDK uses this, never re-decodes
    scope: str

class BonafideAgent:
    """One-shot pipeline: mint actor_token, exchange, fetch Vault stub, call resource."""

    def __init__(self, *, authz_token_url: str, spiffe_id: str,
                 key_path: str, kid: str,
                 vault_addr: str, vault_token: str, vault_kv_path: str):
        ...

    def exchange(self, *, subject_token: str, scope: str, audience: str) -> TaskToken:
        actor_token = self._sign_actor_token(audience=authz_token_url)
        resp = httpx.post(self._token_url, data={
            "grant_type":            "urn:ietf:params:oauth:grant-type:token-exchange",
            "subject_token":         subject_token,
            "subject_token_type":    "urn:ietf:params:oauth:token-type:jwt",
            "actor_token":           actor_token,
            "actor_token_type":      "urn:ietf:params:oauth:token-type:jwt",
            "requested_token_type":  "urn:ietf:params:oauth:token-type:jwt",
            "audience":              audience,
            "scope":                 scope,
        }, timeout=5.0)
        resp.raise_for_status()
        body = resp.json()
        return TaskToken(
            access_token=body["access_token"],
            expires_at=_now() + body["expires_in"],
            scope=body["scope"],
        )

    def fetch_connection(self) -> str:
        """Read the static Vault KV stub value. Replaced by fetch_lease() in VSA."""
        return self._vault.read(self._kv_path)["data"]["data"]["connection"]

    def call(self, *, url: str, token: TaskToken, connection: str | None) -> httpx.Response:
        headers = {"authorization": f"Bearer {token.access_token}"}
        if connection is not None:
            headers["x-bonafide-connection"] = connection
        return httpx.get(url, headers=headers, timeout=5.0)
```

The SDK never caches `TaskToken` past its `expires_at` (per the TTL acceptance criterion in TEC-10). `_sign_actor_token` reads the per-workload Ed25519 key from `key_path` once at construction time, holds it in memory, and signs a fresh actor_token JWT per exchange (1-minute TTL, `kid` in JOSE header). The actor_token's `aud` is the authz token URL so the authz server's audience check works.

### `identity.py`

```python
# sdks/agent-py/bonafide_agent/identity.py
def sign_actor_token(*, key_path: str, kid: str, spiffe_id: str,
                     issuer_audience: str) -> str:
    """Sign a short-lived actor_token JWT with the agent's per-workload Ed25519 key.

    M1 stub: SWI replaces this with a Workload-API JWT-SVID fetch.
    """
    now = int(time.time())
    claims = {
        "iss": spiffe_id,            # the agent is its own issuer in M1
        "sub": spiffe_id,
        "aud": issuer_audience,
        "iat": now,
        "exp": now + 60,             # 1 min — actor_tokens live only across one exchange
        "jti": str(uuid.uuid4()),
    }
    private_key = _load_ed25519_private_key(key_path)  # cryptography Ed25519PrivateKey
    return jwt.encode(claims, private_key, algorithm="EdDSA",
                      headers={"kid": kid})            # PyJWT — alg goes in headers automatically
```

---

## Python resource SDK — `sdks/resource-py`

### Middleware

```python
# sdks/resource-py/bonafide_resource/middleware.py
from fastapi import Depends, HTTPException, Request

@dataclass(frozen=True)
class ActorChain:
    subject: str               # the human SPIFFE ID
    current_actor: str         # outermost act.sub — the only thing the resource authorizes against
    prior_actors: tuple[str, ...]  # inner act entries; evidence only

    @property
    def all_actors(self) -> tuple[str, ...]:
        return (self.current_actor, *self.prior_actors)

class TokenValidator:
    """Validate task tokens against the authz JWKS. Construct one per app."""

    def __init__(self, *, issuer: str, jwks_url: str, audience: str):
        self._issuer = issuer
        self._audience = audience
        self._jwks = JWKSCache(jwks_url)

    async def __call__(self, request: Request) -> ActorChain:
        token = _bearer(request)
        if token is None:
            raise HTTPException(401, "missing bearer token")

        header = jwt.get_unverified_header(token)
        if header.get("alg") in (None, "none"):
            raise HTTPException(401, "alg=none rejected")

        key = await self._jwks.get_key(header["kid"])    # PyJWK with .key attr
        try:
            claims = jwt.decode(
                token, key,
                algorithms=["EdDSA"],
                issuer=self._issuer,
                audience=self._audience,
                leeway=0,                                # CONTRACT.md §3 / DESIGN.md §4: strict exp
            )
        except jwt.PyJWTError as e:
            raise HTTPException(401, f"token rejected: {e}")

        chain = self._extract_chain(claims)         # impersonation guard runs inside
        request.state.actor_chain = chain
        return chain

    def _extract_chain(self, claims: dict) -> ActorChain:
        sub = claims["sub"]
        act = claims.get("act")
        if act is None or "sub" not in act:
            raise HTTPException(401, "task token missing act claim")
        # Impersonation guard (CONTRACT.md §6.3): sub must look like a human SPIFFE ID.
        # The resource SDK does NOT trust the subject_token directly here — instead it asserts
        # the SHAPE of the chain: the token's sub MUST be a human SPIFFE ID, and the act chain
        # MUST nest if it nests at all (no malformed acts).
        if not sub.startswith("spiffe://bonafide.local/human/"):
            self._impersonation_alarm(reason="non_human_sub", sub=sub)
            raise HTTPException(401, "impersonation guard: sub is not a human SPIFFE ID")
        prior: list[str] = []
        cursor = act.get("act")
        while cursor is not None:
            if "sub" not in cursor:
                self._impersonation_alarm(reason="malformed_inner_act")
                raise HTTPException(401, "impersonation guard: malformed inner act")
            prior.append(cursor["sub"])
            cursor = cursor.get("act")
        return ActorChain(subject=sub, current_actor=act["sub"], prior_actors=tuple(prior))
```

The middleware is registered as a FastAPI dependency; the calendar app declares it on its protected route and reads `chain.subject` and `chain.current_actor` to make its authorization decision (CONTRACT.md §6.2). Prior actors are exposed for logging/response-body purposes only.

### JWKS cache (resolves DESIGN.md §6 "Resource SDK error handling for stale JWKS")

```python
class JWKSCache:
    """Fetch JWKS, cache by kid. On cache miss, refresh — but at most once per
    REFRESH_INTERVAL seconds (rate limit to prevent JWKS-storming the authz server)."""

    REFRESH_INTERVAL = 60.0  # seconds
    FETCH_TIMEOUT = 3.0

    async def get_key(self, kid: str):
        if kid in self._keys:
            return self._keys[kid]
        # Cache miss: maybe refresh.
        async with self._refresh_lock:
            if kid in self._keys:                # double-check after lock
                return self._keys[kid]
            now = monotonic()
            if now - self._last_fetch < self.REFRESH_INTERVAL:
                raise HTTPException(401, "unknown kid; refresh rate-limited")
            await self._fetch()
            self._last_fetch = now
        if kid not in self._keys:
            raise HTTPException(401, f"unknown kid: {kid}")
        return self._keys[kid]
```

Rate limit prevents a misbehaving client (or attacker) from forcing JWKS fetches at arbitrary rates. The 60-second window is long enough to absorb any honest rotation (post-MVP) and short enough that real key rotation propagates in under a minute.

---

## Apps

### `apps/demo-human` (CLI)

Single-command: `python -m demo_human --email alice@example.com [--ttl 900]`. Prints the JWT to stdout. Signs with the same Ed25519 key the authz server signs with (so verification against the authz JWKS just works). `sub` is `spiffe://bonafide.local/human/<email>`. `aud` is the authz issuer URL (per CONTRACT.md §4). No `act` claim is ever included — the CLI refuses if asked.

### `apps/demo-agent` (CLI)

`python -m demo_agent --user-jwt $(python -m demo_human --email alice@example.com)`. Drives the `BonafideAgent` pipeline:

1. `agent.exchange(subject_token=user_jwt, scope="calendar:read:alice@example.com", audience="http://calendar.bonafide.local:9000")`
2. `connection = agent.fetch_connection()`
3. `agent.call(url="http://calendar.bonafide.local:9000/events", token=task_token, connection=connection)`
4. Prints the calendar response JSON.

Fails loudly at any step; no retries, no fallback.

### `apps/demo-calendar` (FastAPI)

One protected route `GET /events`. The route depends on `TokenValidator()` which populates `request.state.actor_chain`. The handler reads `chain.subject` and `chain.current_actor`, asserts the scope is `calendar:read:<email>` matching the subject's email portion, opens a transient Postgres connection using the DSN from the `X-Bonafide-Connection` header (the Vault KV stub value, supplied by the agent), reads one row from the `calendar_events` table for that email, and returns:

```json
{
  "acting_for": "spiffe://bonafide.local/human/alice@example.com",
  "acted_by":   "spiffe://bonafide.local/agent/planner",
  "evidence_chain": [],
  "events": [
    {"id": 1, "title": "Standup", "starts_at": "2026-06-02T09:00:00Z"}
  ]
}
```

`evidence_chain` is empty in TEC (depth 1, no prior actors); SAN populates it.

The DSN-from-header approach is dev-only and explicit; VSA replaces it with a Vault-dynamic credential.

---

## Postgres calendar fixture

`deploy/postgres/init.sql`:

```sql
CREATE TABLE calendar_events (
    id          SERIAL PRIMARY KEY,
    owner_email TEXT NOT NULL,
    title       TEXT NOT NULL,
    starts_at   TIMESTAMPTZ NOT NULL
);
INSERT INTO calendar_events (owner_email, title, starts_at) VALUES
    ('alice@example.com', 'Standup', '2026-06-02T09:00:00Z'),
    ('alice@example.com', 'Lunch with Bob', '2026-06-02T12:30:00Z');

-- The Postgres role the calendar app will connect as in TEC (static).
-- VSA replaces this with a Vault-issued ephemeral role.
CREATE ROLE calendar_reader LOGIN PASSWORD 'calendar-dev-password';
GRANT CONNECT ON DATABASE calendar TO calendar_reader;
GRANT USAGE ON SCHEMA public TO calendar_reader;
GRANT SELECT ON calendar_events TO calendar_reader;
```

The KV stub holds the connection string `postgresql://calendar_reader:calendar-dev-password@postgres:5432/calendar`. The agent reads it and forwards it to the calendar; the calendar uses it for the duration of the request, then drops it.

---

## Vault dev-mode + KV stub

`deploy/vault/bootstrap.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail
export VAULT_ADDR=http://vault:8200
export VAULT_TOKEN=devroot

# Enable KV v2 at secret/ (Vault dev mode already does this; idempotent).
vault secrets enable -path=secret kv-v2 2>/dev/null || true

# Write the calendar connection stub.
vault kv put secret/calendar/connection \
    connection="postgresql://calendar_reader:calendar-dev-password@postgres:5432/calendar"

echo "Vault bootstrap complete."
```

Vault container runs with `-dev -dev-root-token-id=devroot`. M1 only — VSA enables the SPIFFE auth method and database secrets engine, deletes the static KV path, and updates the agent SDK to use leases instead.

---

## Docker compose

```yaml
# docker-compose.yml (sketch; full file lands in T-NN from tasks.md)
services:
  authz:
    build: ./services/authz
    environment: { BONAFIDE_AUTHZ_ISSUER: http://authz.bonafide.local:8080, ... }
    volumes:
      - ./deploy/authz:/etc/authz:ro
      - audit:/var/log/bonafide
    ports: ["8080:8080"]

  postgres:
    image: postgres:16
    environment: { POSTGRES_DB: calendar, POSTGRES_USER: postgres, POSTGRES_PASSWORD: postgres }
    volumes:
      - ./deploy/postgres/init.sql:/docker-entrypoint-initdb.d/init.sql:ro
    ports: ["5432:5432"]

  vault:
    image: hashicorp/vault:1.21
    cap_add: [IPC_LOCK]
    environment: { VAULT_DEV_ROOT_TOKEN_ID: devroot, VAULT_DEV_LISTEN_ADDRESS: 0.0.0.0:8200 }
    ports: ["8200:8200"]

  calendar:
    build: ./apps/demo-calendar
    environment: { BONAFIDE_AUTHZ_ISSUER: http://authz.bonafide.local:8080, ... }
    ports: ["9000:9000"]
    depends_on: [authz, postgres]

  # agent is a one-shot CLI, run on demand by smoke.sh; not always-on.

volumes:
  audit: {}
```

The hostnames `authz.bonafide.local`, `calendar.bonafide.local` resolve via docker's internal DNS; on the host they're added to `/etc/hosts` by `bootstrap.sh` (idempotent) or accessed via published ports. The smoke script always uses container hostnames inside docker, host ports outside.

---

## Bootstrap script

`scripts/bootstrap.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

# 1. Generate dev keys if missing.
if [[ ! -f deploy/authz/signing.key ]]; then
    openssl genpkey -algorithm Ed25519 -out deploy/authz/signing.key
fi
if [[ ! -f deploy/spire-stub/agent-planner.key.pem ]]; then
    mkdir -p deploy/spire-stub
    openssl genpkey -algorithm Ed25519 -out deploy/spire-stub/agent-planner.key.pem
    openssl pkey -in deploy/spire-stub/agent-planner.key.pem -pubout \
        -out deploy/spire-stub/agent-planner.pub.pem
fi

# 2. Update actor-trust.yaml with the agent's public key (idempotent).
python scripts/_update_actor_trust.py \
    --map deploy/authz/actor-trust.yaml \
    --spiffe-id spiffe://bonafide.local/agent/planner \
    --kid planner-dev-key-1 \
    --pub deploy/spire-stub/agent-planner.pub.pem

# 3. Bring the stack up.
docker compose up -d --wait

# 4. Bootstrap Vault.
docker compose exec -T vault sh < deploy/vault/bootstrap.sh

# 5. Tell the user it's ready.
echo "bonafide ready. Try: ./scripts/smoke.sh"
```

The script is idempotent — running it twice does nothing harmful and produces the same end state.

---

## Smoke harness (first block — TEC-11)

`scripts/smoke.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

#--- TEC block -----------------------------------------------------------------
echo "[smoke:TEC] minting user JWT..."
USER_JWT=$(docker compose run --rm demo-human \
    python -m demo_human --email alice@example.com)

echo "[smoke:TEC] exchanging for task token + calling calendar..."
RESP=$(docker compose run --rm demo-agent \
    python -m demo_agent --user-jwt "$USER_JWT")

echo "$RESP" | jq -e '
    .acting_for == "spiffe://bonafide.local/human/alice@example.com"
    and .acted_by == "spiffe://bonafide.local/agent/planner"
    and (.events | length) > 0
' > /dev/null

echo "[smoke:TEC] checking impersonation guard..."
# Pass a hand-tampered token whose sub has been replaced; resource must 401.
TAMPERED=$(python scripts/_tamper_sub.py --token "$USER_JWT" --new-sub "spiffe://bonafide.local/human/eve@evil.test")
test "$(docker compose run --rm -e USER_JWT="$TAMPERED" demo-agent \
    python -m demo_agent --raw --user-jwt "$TAMPERED" \
    | head -n 1)" = "HTTP/1.1 401" || { echo "guard failed"; exit 1; }

echo "[smoke:TEC] checking missing actor_token rejection..."
# Direct curl against /token without actor_token; authz must 400 invalid_request.
test "$(curl -s -o /dev/null -w '%{http_code}' \
    -X POST http://authz.bonafide.local:8080/token \
    --data-urlencode "grant_type=urn:ietf:params:oauth:grant-type:token-exchange" \
    --data-urlencode "subject_token=$USER_JWT" \
    --data-urlencode "subject_token_type=urn:ietf:params:oauth:token-type:jwt" \
    --data-urlencode "requested_token_type=urn:ietf:params:oauth:token-type:jwt" \
    --data-urlencode "audience=http://calendar.bonafide.local:9000" \
    --data-urlencode "scope=calendar:read:alice@example.com")" = "400"

echo "[smoke:TEC] OK"
#--- end TEC block --------------------------------------------------------------

# SWI block appended here by spire-workload-identity slice
# VSA block, OPE block, AUD block, SAN block ditto
```

The TEC block asserts:
- Calendar response carries the correct `acting_for` (human) and `acted_by` (planner agent).
- A tampered-`sub` token is 401ed by the impersonation guard.
- An exchange request missing `actor_token` is 400ed.

Future slices append a block; the block above never changes.

---

## Open decisions resolved here

(Each addresses an item from DESIGN.md §6 or a question this slice raises.)

- **Internal Go package layout for `services/authz`.** Eight internal packages as above. `exchange` is the only package that imports both `policy` and `trust`; everything else is leaves.
- **Resource SDK error handling for stale JWKS.** On a cache miss, refresh — but at most once per 60 seconds. A miss outside that window returns `401 unknown kid`. This trades a small loss of availability under unexpected key rotation for a hard cap on JWKS-storming the authz server. JWKS rotation cadence is post-MVP, so 60 s is conservative.
- **Token-exchange handler concurrency / rate-limit shape.** No rate limit in MVP. The handler is request-stateful but reads only startup-loaded state; concurrent requests are independent. Logging via `log/slog`; one structured event per request.
- **Dev `actor_token` issuer trust (M1 stub).** Per-agent Ed25519 keypair on disk; the authz server loads `{spiffe_id: {kid, public_key}}` from `actor-trust.yaml` at startup and verifies actor_tokens against this map. Deleted by SWI in favour of `FetchJWTBundles`.
- **JWT signing key.** Single Ed25519 key (`deploy/authz/signing.key`). The kid published in the JWKS is the first 12 chars of `base64url(sha256(public_key))`. Same key signs the user JWT (TEC-2) and the task token (TEC-3) — both have `iss=authz`, so this is fine.
- **Calendar uses Postgres connection string in a request header.** Dev-only convention. Header name: `X-Bonafide-Connection`. VSA replaces with a leased credential and may keep the header (different semantics) or change to per-token claims; that decision is VSA's.
- **Audit emitter buffer.** 256-event buffered channel with a 100 ms backpressure window before dropping. Drops are logged at ERROR and visible in metrics. AUD's HTTP-based emitter keeps the same buffer + retry shape.
- **CLI tools live in `apps/`, not in `tools/` or `bin/`.** Same pattern as go-phish's `cmd/`. `demo-human`, `demo-agent`, `demo-calendar` are all "apps you can run."
- **JWT library: `PyJWT` ≥ 2.10 with `[cryptography]` (Python) + `go-jose/v4` (Go).** Both implement EdDSA at the JWS layer (PyJWT since 2.6; go-jose since v2). The earlier pin to `python-jose[cryptography]` was wrong — that library never gained EdDSA, and the discovery happened mid-T-14 (see `agent-notes.md` 2026-06-04). PyJWT's `PyJWK` helper consumes a JWKS dict directly, which the resource SDK relies on in T-20.
- **No leeway on `exp`.** Per CONTRACT.md §3 and DESIGN.md §4: strict expiry. Tested explicitly in the resource SDK.

---

## Files created (lands in T-NN, owned by tasks.md)

| File | Purpose |
|---|---|
| `services/authz/go.mod` + `cmd/authz/main.go` | Binary entrypoint |
| `services/authz/internal/config/config.go` | Settings |
| `services/authz/internal/op/storage.go` | zitadel/oidc Storage impl (Storage methods: SignatureAlgorithms, KeySet, TokenRequestByRefreshToken, etc.) |
| `services/authz/internal/exchange/handler.go` | RFC 8693 handler |
| `services/authz/internal/exchange/act_chain.go` + `act_chain_test.go` | The canonical nesting function and its tests |
| `services/authz/internal/exchange/types.go` | TaskTokenClaims, Act, etc. |
| `services/authz/internal/policy/policy.go` | Gate interface + map impl |
| `services/authz/internal/trust/trust.go` | IssuerTrust interface + YAML-backed impl |
| `services/authz/internal/keys/keys.go` | Signer + JWKS publisher |
| `services/authz/internal/audit/audit.go` + `file.go` | Emitter interface + file impl |
| `services/authz/internal/httputil/router.go` | chi router + OAuth error helper |
| `sdks/agent-py/pyproject.toml` + `bonafide_agent/{client.py, identity.py, errors.py}` | Agent SDK |
| `sdks/resource-py/pyproject.toml` + `bonafide_resource/{middleware.py, jwks.py, chain.py, errors.py}` | Resource SDK |
| `apps/demo-human/{pyproject.toml, demo_human/__main__.py}` | User-JWT CLI |
| `apps/demo-agent/{pyproject.toml, demo_agent/__main__.py}` | Driver CLI |
| `apps/demo-calendar/{pyproject.toml, demo_calendar/main.py, tests/...}` | FastAPI calendar |
| `deploy/postgres/init.sql` | Calendar fixture |
| `deploy/vault/bootstrap.sh` | KV stub |
| `deploy/authz/actor-trust.yaml` + `policy.yaml` | M1 stubs |
| `deploy/spire-stub/` | Per-agent dev keys (gitignored except a README explaining the slice) |
| `docker-compose.yml` | Six containers |
| `scripts/bootstrap.sh` + `scripts/smoke.sh` + `scripts/_update_actor_trust.py` + `scripts/_tamper_sub.py` | Bring-up + smoke |
| `.gitignore` | `deploy/authz/signing.key`, `deploy/spire-stub/*.pem`, audit logs |

---

## Out of scope (this slice only — see requirements.md for the slice-wide list)

- Any of the components a later slice owns (SPIRE, Vault SPIFFE auth, OPA Rego, Postgres audit, depth-2 chain).
- Real Dockerfile optimization (multi-stage, distroless, etc.) — vanilla Dockerfiles only.
- Production-quality logs (we use `log/slog` with simple key=value output; structured-JSON logging is post-MVP).
- Any UI. The calendar app responds in JSON; there is no HTML surface.
- Health checks beyond a trivial `/healthz` returning 200.
- TLS between containers. Compose-internal HTTP is acceptable for the MVP; mTLS is a post-MVP concern.

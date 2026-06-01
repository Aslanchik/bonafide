# vault-spiffe-auth: Design

## Overview

This slice removes the last static-secret path from the demo. The Vault container is reconfigured: KV becomes vestigial (the static `secret/calendar/connection` value is deleted), the SPIFFE auth method is enabled and bound to `spiffe://bonafide.local/agent/*`, and the database secrets engine is enabled against the calendar Postgres. The agent SDK's `fetch_connection()` is renamed `fetch_lease()` and returns a fresh DSN built from a Vault-issued ephemeral Postgres role each call. The calendar app keeps its `X-Bonafide-Connection` header contract: it still receives a DSN per request and uses it for the duration of the request. The Postgres-side change is the deletion of the static `calendar_reader` role (created by `deploy/postgres/init.sql` in TEC) and the introduction of a Vault-managed connection that creates and revokes ephemeral roles on demand.

The slice carries a documented contingency: if Vault's native SPIFFE auth method requires Enterprise at runtime (the InfoQ-vs-HashiCorp ambiguity flagged in the meta-plan), the bootstrap script enables Vault's JWT auth method instead and points it at SPIRE's OIDC discovery (which SWI exposed). Both configurations share an identical Vault role binding and an identical agent SDK surface; switching is a Vault config diff, not a code diff.

Everything else from TEC and SWI — the act-chain builder, the policy gate, the JWKS, the file-backed audit emitter, the resource SDK, the SPIRE topology, the trust-domain identity model — is unchanged.

---

## Stack (additions only)

| Concern | Choice | Why |
|---|---|---|
| Vault | `hashicorp/vault:1.21+` | Native SPIFFE auth method available; same image as TEC dev mode |
| Vault Python client | `hvac` | Idiomatic Python client; supports KV2, database engine, and the SPIFFE auth method's login flow (and JWT auth login for the contingency path) |
| Postgres user for Vault | `vault_manager` | Superuser-of-the-app-DB role Vault uses to CREATE/DROP ephemeral roles; created at compose-up time by an init script |

`hvac`'s SPIFFE auth login takes a JWT-SVID and the configured `role`. The fallback `hvac.auth.jwt.jwt_login(...)` takes the same JWT-SVID and the same role name; the only difference is the auth backend path Vault is configured at.

---

## Repo additions and deletions

```
+ deploy/vault/spiffe-auth.sh             # enables auth/spiffe; binds the agent role; idempotent
+ deploy/vault/database-engine.sh         # enables database/; configures Postgres connection; creates calendar_reader role
+ deploy/vault/jwt-auth-fallback.sh       # the contingency: enables auth/jwt against SPIRE OIDC discovery
+ deploy/postgres/vault-bootstrap.sql     # creates the vault_manager Postgres superuser-of-this-DB

  deploy/vault/bootstrap.sh               # rewritten: chooses native-SPIFFE vs JWT-fallback based on probe; calls the above
- deploy/vault/                            # the M1 KV stub write goes away (replaced by VSA's bootstrap)

  sdks/agent-py/bonafide_agent/client.py  # fetch_connection() -> fetch_lease()
+ sdks/agent-py/bonafide_agent/vault.py   # encapsulates the Vault login + database/creds/<role> call

  apps/demo-calendar/demo_calendar/main.py # accepts the dynamic DSN exactly as before via X-Bonafide-Connection (no change at the wire)
  deploy/postgres/init.sql                 # static calendar_reader role removed; vault_manager role created

  scripts/bootstrap.sh                     # extended: Vault SPIFFE-auth probe + bootstrap dispatch
  scripts/smoke.sh                         # VSA block appended
  docker-compose.yml                       # unchanged service set; Vault config only
```

---

## Vault configuration

### Native SPIFFE auth method (`deploy/vault/spiffe-auth.sh`)

```bash
#!/usr/bin/env bash
set -euo pipefail
export VAULT_ADDR=http://vault:8200
export VAULT_TOKEN=devroot

# 1. Enable the SPIFFE auth method.
vault auth enable spiffe || true   # idempotent

# 2. Configure the SPIFFE auth method against the local SPIRE trust domain.
vault write auth/spiffe/config \
    trust_domain=bonafide.local \
    jwks_url=http://spire-server:8081/keys \
    issuer=https://bonafide.local

# 3. Create the role that binds spiffe://bonafide.local/agent/* to a Vault policy.
vault write auth/spiffe/role/agent \
    bound_spiffe_ids="spiffe://bonafide.local/agent/*" \
    token_policies="calendar-reader" \
    token_ttl=300 \
    token_max_ttl=300 \
    token_no_default_policy=true

# 4. Attach a Vault policy that allows only reading database/creds/calendar_reader.
vault policy write calendar-reader - <<'POL'
path "database/creds/calendar_reader" {
  capabilities = ["read"]
}
POL
```

The role binds `spiffe://bonafide.local/agent/*` per VSA-1; identities under `human/*` or `service/*` cannot match. `bound_spiffe_ids` does not accept globs across role prefixes, so a request from `service/calendar` is structurally unable to mint a Vault token via this role.

The Vault token issued on login is policy-scoped to *only* `database/creds/calendar_reader` and has `token_ttl == token_max_ttl == 300` (5 min). It cannot be renewed.

### Database secrets engine (`deploy/vault/database-engine.sh`)

```bash
#!/usr/bin/env bash
set -euo pipefail
export VAULT_ADDR=http://vault:8200
export VAULT_TOKEN=devroot

vault secrets enable database || true

# Vault uses vault_manager (a Postgres superuser within this DB) to create
# ephemeral CREATE ROLE + GRANT SELECT users on demand.
vault write database/config/calendar \
    plugin_name=postgresql-database-plugin \
    allowed_roles="calendar_reader" \
    connection_url='postgresql://{{username}}:{{password}}@postgres:5432/calendar?sslmode=disable' \
    username="vault_manager" \
    password="vault-manager-dev-password"

# The role that defines what an ephemeral lease looks like.
vault write database/roles/calendar_reader \
    db_name=calendar \
    creation_statements='CREATE ROLE "{{name}}" WITH LOGIN PASSWORD '"'"'{{password}}'"'"' VALID UNTIL '"'"'{{expiration}}'"'"';
                         GRANT CONNECT ON DATABASE calendar TO "{{name}}";
                         GRANT USAGE ON SCHEMA public TO "{{name}}";
                         GRANT SELECT ON calendar_events TO "{{name}}";' \
    revocation_statements='REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM "{{name}}";
                           REVOKE ALL PRIVILEGES ON SCHEMA public FROM "{{name}}";
                           REVOKE CONNECT ON DATABASE calendar FROM "{{name}}";
                           DROP ROLE IF EXISTS "{{name}}";' \
    default_ttl=300 \
    max_ttl=300 \
    renew_statements=''   # explicitly empty: renewal not allowed per VSA-3
```

`vault_manager` is a superuser scoped to the `calendar` database only — it has CREATE ROLE rights but no rights outside this DB. Created at compose-up by `deploy/postgres/vault-bootstrap.sql`:

```sql
CREATE ROLE vault_manager WITH LOGIN PASSWORD 'vault-manager-dev-password';
GRANT ALL PRIVILEGES ON DATABASE calendar TO vault_manager;
ALTER USER vault_manager CREATEROLE;
-- vault_manager can grant SELECT on calendar_events because we GRANT it explicitly:
ALTER TABLE calendar_events OWNER TO vault_manager;
```

The `creation_statements` produces a fresh Postgres role per lease, named `v-token-calendar_reader-<random>-<exp>` (Vault's default). The role has SELECT on `calendar_events` and nothing else; it cannot connect to other databases, it cannot read other tables, and it expires at the same timestamp Vault publishes in the lease's `expiration` field.

### Contingency: JWT auth via SPIRE OIDC discovery (`deploy/vault/jwt-auth-fallback.sh`)

Triggered only when the native SPIFFE auth method is unavailable:

```bash
#!/usr/bin/env bash
set -euo pipefail
export VAULT_ADDR=http://vault:8200
export VAULT_TOKEN=devroot

vault auth enable jwt || true

vault write auth/jwt/config \
    oidc_discovery_url=http://spire-server:8081 \
    bound_issuer=https://bonafide.local

# Identical role binding to spiffe-auth.sh — same bound subject pattern, same policy, same TTL.
vault write auth/jwt/role/agent \
    role_type=jwt \
    bound_subject="spiffe://bonafide.local/agent/*" \
    bound_audiences="vault" \
    user_claim="sub" \
    token_policies="calendar-reader" \
    token_ttl=300 \
    token_max_ttl=300 \
    token_no_default_policy=true
```

Two configuration deltas vs. the native path: the auth method is at `auth/jwt/` instead of `auth/spiffe/`, and the role binding uses `bound_subject` against the SPIFFE ID string (Vault's JWT auth matches against claims; the SPIFFE ID lives in `sub`). The `bound_audiences="vault"` means the agent SDK must request a JWT-SVID with audience `vault` for the fallback path.

### Bootstrap dispatch (`deploy/vault/bootstrap.sh`)

```bash
#!/usr/bin/env bash
set -euo pipefail

# Probe whether native SPIFFE auth is enable-able on this Vault edition.
if vault auth enable spiffe 2>&1 | grep -q "enterprise"; then
    echo "[vault] native SPIFFE auth requires Enterprise; falling back to JWT auth + SPIRE OIDC discovery"
    bash deploy/vault/jwt-auth-fallback.sh
    AUTH_MODE=jwt
else
    bash deploy/vault/spiffe-auth.sh
    AUTH_MODE=spiffe
fi

bash deploy/vault/database-engine.sh

# Record the chosen mode for operator visibility (per VSA-8 requirement).
echo "$AUTH_MODE" > deploy/vault/.auth_mode
```

The `.auth_mode` file is read by the smoke harness so its assertion ("Vault audit log records the SPIFFE ID as the auth source") accounts for the slightly different log shape Vault emits under JWT auth vs. SPIFFE auth.

---

## Agent SDK swap

### `client.py` — surface change

`fetch_connection()` is renamed and re-typed; the rest of the SDK is unchanged.

```python
# sdks/agent-py/bonafide_agent/client.py
@dataclass(frozen=True)
class DBLease:
    dsn: str            # postgresql://<role>:<pw>@postgres:5432/calendar
    expires_at: int     # absolute unix seconds; agent must not reuse past this

class BonafideAgent:
    def __init__(self, *, authz_token_url: str, spiffe_id: str,
                 spiffe_socket: str, vault_addr: str,
                 vault_auth_mode: Literal["spiffe", "jwt"] = "spiffe",
                 vault_role: str = "agent"):
        self._spiffe_socket = spiffe_socket
        self._vault = VaultClient(addr=vault_addr,
                                   auth_mode=vault_auth_mode,
                                   role=vault_role,
                                   spiffe_socket=spiffe_socket)
        ...

    def exchange(self, *, subject_token: str, scope: str, audience: str) -> TaskToken:
        ...  # unchanged from SWI

    def fetch_lease(self) -> DBLease:
        """Authenticate to Vault with a JWT-SVID and fetch a calendar_reader lease.

        Replaces the M1 fetch_connection(). The SDK never caches the lease past
        expires_at; every call to fetch_lease() yields a fresh lease.
        """
        return self._vault.fetch_lease(path="database/creds/calendar_reader")

    def call(self, *, url: str, token: TaskToken, lease: DBLease) -> httpx.Response:
        # Wire compatibility: the dynamic DSN goes into the same X-Bonafide-Connection
        # header the calendar app already reads. The calendar doesn't know it's dynamic.
        headers = {
            "authorization": f"Bearer {token.access_token}",
            "x-bonafide-connection": lease.dsn,
        }
        return httpx.get(url, headers=headers, timeout=5.0)
```

The `vault_token` constructor parameter from TEC is gone; the SDK now authenticates with its SVID. The `vault_kv_path` parameter is gone too. The `vault_addr` remains.

### `vault.py` (new)

```python
# sdks/agent-py/bonafide_agent/vault.py
import time, hvac
from .identity_spire import SpireActorTokenSigner

class VaultClient:
    """Wraps Vault login + lease fetch behind one method.

    Internally chooses between auth/spiffe and auth/jwt based on auth_mode.
    Switching modes is a constructor flag, not a code path through the SDK's
    public surface — callers see DBLease either way.
    """

    LOGIN_LEEWAY_SECONDS = 30  # re-login when the Vault token is within 30s of exp

    def __init__(self, *, addr: str, auth_mode: str, role: str, spiffe_socket: str):
        self._client = hvac.Client(url=addr)
        self._role = role
        self._mode = auth_mode
        # The actor_token used at the exchange has aud=authz; the Vault login JWT
        # has aud="vault" when JWT-auth is in use (Vault's bound_audiences check).
        # The native SPIFFE auth method does not require an audience claim.
        svid_audience = "vault" if auth_mode == "jwt" else "https://authz.bonafide.local"
        self._svid = SpireActorTokenSigner(spiffe_socket=spiffe_socket,
                                            audience=svid_audience)
        self._token_exp = 0
        self._login()

    def _login(self) -> None:
        jwt_svid = self._svid.fetch_actor_token()
        if self._mode == "spiffe":
            resp = self._client.auth.spiffe.login(role=self._role, jwt=jwt_svid)
        elif self._mode == "jwt":
            resp = self._client.auth.jwt.jwt_login(role=self._role, jwt=jwt_svid)
        else:
            raise ValueError(f"unknown auth mode: {self._mode}")
        self._client.token = resp["auth"]["client_token"]
        self._token_exp = int(time.time()) + int(resp["auth"]["lease_duration"])

    def fetch_lease(self, *, path: str) -> DBLease:
        if time.time() > self._token_exp - self.LOGIN_LEEWAY_SECONDS:
            self._login()
        resp = self._client.read(path)
        data = resp["data"]
        dsn = (f"postgresql://{data['username']}:{data['password']}"
               f"@postgres:5432/calendar?sslmode=disable")
        return DBLease(dsn=dsn, expires_at=int(time.time()) + int(resp["lease_duration"]))
```

The SDK's only stateful behavior is the in-process Vault token cache. The cache is dropped on `expires_at`, dropped on process exit, and never written to disk.

---

## Calendar app

No code change required for the request path — the calendar already reads `X-Bonafide-Connection` and uses it to open a Postgres connection. The only semantic change is that the DSN is now ephemeral (5-min lease), so any connection pool the calendar holds onto would outlive the credential. The calendar's connection lifecycle is therefore per-request: open, query, close. No pooling.

The TEC-era invariant ("calendar holds no static DB credential") becomes literally true after `deploy/postgres/init.sql` is updated to remove the `calendar_reader` static role.

```python
# apps/demo-calendar/demo_calendar/main.py — request path (unchanged shape, tightened comment)
@app.get("/events")
async def get_events(chain: ActorChain = Depends(validator),
                     x_bonafide_connection: str = Header(...)):
    # Open a transient connection using the agent-supplied DSN. The DSN is a
    # Vault-issued ephemeral lease (VSA); we connect, query, and close in one
    # request — pooling would outlive the credential.
    conn = await asyncpg.connect(x_bonafide_connection)
    try:
        rows = await conn.fetch(
            "SELECT id, title, starts_at FROM calendar_events WHERE owner_email = $1",
            _email_from(chain.subject),
        )
    finally:
        await conn.close()
    return {
        "acting_for": chain.subject,
        "acted_by":   chain.current_actor,
        "evidence_chain": list(chain.prior_actors),
        "events": [dict(r) for r in rows],
    }
```

If the agent fails to attach `X-Bonafide-Connection` (or attaches an empty value), the FastAPI `Header(...)` declaration causes the request to 422 before the handler runs. No fallback path; no built-in DSN.

---

## Postgres changes

```sql
-- deploy/postgres/init.sql — diff vs. TEC
- CREATE ROLE calendar_reader LOGIN PASSWORD 'calendar-dev-password';
- GRANT CONNECT ON DATABASE calendar TO calendar_reader;
- GRANT USAGE ON SCHEMA public TO calendar_reader;
- GRANT SELECT ON calendar_events TO calendar_reader;
```

The static role is gone. The `calendar_events` table and its seed data remain; the `vault_manager` superuser (created in `vault-bootstrap.sql`) owns the table and grants per-lease.

---

## Smoke harness — VSA block

```bash
#--- VSA block -----------------------------------------------------------------
echo "[smoke:VSA] running depth-1 exchange with Vault-issued DSN..."

USER_JWT=$(docker compose run --rm demo-human python -m demo_human --email alice@example.com)
RESP=$(docker compose run --rm demo-agent python -m demo_agent --user-jwt "$USER_JWT")

# Same assertions as TEC, plus: the DSN used was Vault-issued.
echo "$RESP" | jq -e '.acting_for == "spiffe://bonafide.local/human/alice@example.com"' > /dev/null
echo "$RESP" | jq -e '(.events | length) > 0' > /dev/null

echo "[smoke:VSA] verifying Vault audit log names the agent's SPIFFE ID..."
# Vault's audit log is enabled at compose-up; tail it for the most recent login event.
LAST_LOGIN=$(docker compose exec -T vault \
    cat /vault/audit.log | jq -r 'select(.type=="response" and .request.path|endswith("login")) | .auth.metadata' | tail -n 1)

# Native-SPIFFE path emits the SPIFFE ID under .auth.metadata.spiffe_id;
# JWT-fallback path emits it as the .auth.metadata.user_claim. Read deploy/vault/.auth_mode to pick.
AUTH_MODE=$(cat deploy/vault/.auth_mode)
if [[ "$AUTH_MODE" == "spiffe" ]]; then
    echo "$LAST_LOGIN" | jq -e '.spiffe_id == "spiffe://bonafide.local/agent/planner"' > /dev/null
else
    echo "$LAST_LOGIN" | jq -e '.user_claim == "spiffe://bonafide.local/agent/planner"' > /dev/null
fi

echo "[smoke:VSA] removing planner SPIRE registration -> next Vault call must fail closed..."
docker compose exec -T spire-server spire-server entry delete \
    -spiffeID spiffe://bonafide.local/agent/planner
sleep 2

OUT=$(docker compose run --rm demo-agent python -m demo_agent --user-jwt "$USER_JWT" 2>&1 || true)
echo "$OUT" | grep -qi "permission denied\|invalid jwt\|no svid"

# Restore for subsequent runs.
./deploy/spire/registrations.sh

echo "[smoke:VSA] OK"
#--- end VSA block --------------------------------------------------------------
```

The VSA block depends on prior TEC and SWI blocks having passed; both still run before it.

---

## Open decisions resolved here

- **Vault auth mode detection.** Probe by attempting `vault auth enable spiffe` once and reading stderr; "enterprise" in the error message switches to the JWT fallback. Recorded in `deploy/vault/.auth_mode` for the smoke harness. Idempotent: probing twice does not re-enable nor disable the auth method.
- **JWT-SVID audience under JWT-auth fallback.** `vault`. The Vault role declares `bound_audiences=["vault"]`, and the agent SDK's `VaultClient` requests an SVID with that audience when in fallback mode. The exchange flow's actor_token continues to use `audience=authz_url`; the two SVIDs are independent.
- **Vault token TTL: 5 min, non-renewable.** Matches the DB lease ceiling. The SDK re-logs in when the Vault token is within 30 s of expiry; re-login fetches a fresh SVID. The Vault token is dropped on process exit and never persisted.
- **DSN delivery: same `X-Bonafide-Connection` header as TEC.** The calendar's request contract doesn't change. The change is *what's in the header* — a dynamic DSN now. This avoids touching the resource SDK or the resource app's interface, both of which would be churn for zero security gain.
- **Postgres connection lifecycle in calendar: per-request open/close, no pool.** Pooling outlives credentials. At demo scale the open cost is unmeasurable.
- **`vault_manager` is a per-database superuser, not a global one.** It owns the calendar DB and `calendar_events`, has CREATEROLE inside that DB, and can connect from nowhere except via the Vault DB-engine connection URL. It cannot escalate to other databases.
- **No KV usage after VSA.** The `secret/calendar/connection` path is unwritten by `bootstrap.sh` after this slice. Existing KV value in a docker volume left over from a TEC checkout is deleted by an idempotent first-run line in the new bootstrap script.
- **The contingency switch is bootstrap-time, not runtime.** Once Vault is configured with one auth method, switching requires a Vault restart + reconfigure. The SDK accepts the mode at construction; it does not detect or auto-fail-over at request time.

---

## Files created / modified / deleted

| File | Change |
|---|---|
| `deploy/vault/spiffe-auth.sh` | New |
| `deploy/vault/jwt-auth-fallback.sh` | New |
| `deploy/vault/database-engine.sh` | New |
| `deploy/vault/bootstrap.sh` | Rewritten: probe + dispatch |
| `deploy/postgres/init.sql` | Static `calendar_reader` role removed |
| `deploy/postgres/vault-bootstrap.sql` | New: `vault_manager` superuser of `calendar` DB |
| `sdks/agent-py/bonafide_agent/vault.py` | New |
| `sdks/agent-py/bonafide_agent/client.py` | `fetch_connection()` → `fetch_lease()`; constructor takes `vault_auth_mode` |
| `apps/demo-agent/demo_agent/__main__.py` | Updated to call `fetch_lease()` and pass `DBLease` to `call()` |
| `apps/demo-calendar/demo_calendar/main.py` | Per-request open/close; tightened comment about ephemeral DSN |
| `docker-compose.yml` | Vault audit-log volume added; vault depends_on adjusted |
| `scripts/bootstrap.sh` | Vault probe step + SPIRE-dependent ordering |
| `scripts/smoke.sh` | VSA block appended |

---

## Out of scope for this slice (see requirements.md for the slice-wide list)

- OPA Rego — OPE owns the policy swap.
- Postgres-backed audit — AUD owns it; the file emitter still runs in this slice.
- Sub-agent demo — SAN owns it; only the planner agent has a SPIRE registration today.
- Vault PKI under SPIRE UpstreamAuthority — post-MVP polish.
- Active lease revocation. TTL expiry is the only revocation in this slice.
- Vault HA / multi-cluster / token-broker patterns. Single dev container.

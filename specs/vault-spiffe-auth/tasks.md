# vault-spiffe-auth: Tasks

Tasks are ordered. Each must be complete and verifiable before the next begins. If a task surfaces a spec problem, stop — update the relevant spec, re-review, then continue. One commit per task.

Status: `[ ]` todo · `[x]` done · `[~]` in progress

---

## T-01: [ ] Remove TEC's static `calendar_reader` from Postgres init and add Vault audit-log volume

**Satisfies:** preparation for VSA-2, VSA-5; the safety constraint **"No static long-lived secrets in source, env files, or container images outside SPIRE and Vault"**

- Edit `deploy/postgres/init.sql` to remove the four lines from TEC that created and granted the static `calendar_reader` Postgres role (`CREATE ROLE calendar_reader LOGIN PASSWORD 'calendar-dev-password';`, the two `GRANT` statements, and `GRANT SELECT ON calendar_events TO calendar_reader;`), per `design.md` "Postgres changes" and the deletion list in "Files created / modified / deleted".
- Leave the `calendar_events` table and its seed rows untouched (still needed by VSA-9 smoke, and the table will be reassigned to `vault_manager` in T-02).
- Edit `docker-compose.yml` to add a named volume (e.g. `vault-audit`) mounted at `/vault/audit` inside the `vault` container so audit-log emission can be enabled in T-04 / T-05. No other compose service set changes.

**Verified when:** `grep -c 'calendar_reader' deploy/postgres/init.sql` returns `0` (the static role is gone) while `grep -c 'calendar_events' deploy/postgres/init.sql` is unchanged from TEC. `docker compose config | grep -A2 'vault-audit'` shows the named volume mounted at `/vault/audit` on the `vault` service.

---

## T-02: [ ] Create `deploy/postgres/vault-bootstrap.sql` (the `vault_manager` per-database superuser)

**Satisfies:** scaffolding for VSA-2 (Vault must own a Postgres principal that can CREATE ROLE inside the calendar DB)

- Create `deploy/postgres/vault-bootstrap.sql` containing exactly the SQL from `design.md` "Database secrets engine":
  - `CREATE ROLE vault_manager WITH LOGIN PASSWORD 'vault-manager-dev-password';`
  - `GRANT ALL PRIVILEGES ON DATABASE calendar TO vault_manager;`
  - `ALTER USER vault_manager CREATEROLE;`
  - `ALTER TABLE calendar_events OWNER TO vault_manager;`
- Mount this script at `/docker-entrypoint-initdb.d/02-vault-bootstrap.sql` in the `postgres` service of `docker-compose.yml` so it runs after `init.sql` (Postgres runs init scripts in lexical order).
- Do not grant `vault_manager` access to any other database, schema, or table. Its scope is the `calendar` database only, per `design.md` "Database secrets engine" ("a Postgres superuser within this DB").

**Verified when:** Bringing the Postgres container up with both init scripts mounted, then running `psql -h postgres -U vault_manager -d calendar -c '\du vault_manager'` succeeds (the role exists, can log in, has `CREATEROLE`); `psql -h postgres -U vault_manager -d postgres -c 'SELECT 1'` fails with a permission error (the role is not a superuser globally); `psql -h postgres -U vault_manager -d calendar -c '\d calendar_events'` shows `vault_manager` as the table owner.

---

## T-03: [ ] Author `deploy/vault/spiffe-auth.sh` (native SPIFFE auth method)

**Satisfies:** VSA-1, VSA-3 (Vault role TTL ceiling and non-renewal), CONTRACT.md §1, and the safety constraint **"All credentials short-lived ... No code path may extend these"**

- Create `deploy/vault/spiffe-auth.sh` exactly per `design.md` "Native SPIFFE auth method (`deploy/vault/spiffe-auth.sh`)":
  - `set -euo pipefail`; `VAULT_ADDR=http://vault:8200`; `VAULT_TOKEN=devroot`.
  - `vault auth enable spiffe || true` (idempotent).
  - `vault write auth/spiffe/config trust_domain=bonafide.local jwks_url=http://spire-server:8081/keys issuer=https://bonafide.local`.
  - `vault write auth/spiffe/role/agent bound_spiffe_ids="spiffe://bonafide.local/agent/*" token_policies="calendar-reader" token_ttl=300 token_max_ttl=300 token_no_default_policy=true` — this binds the role to only `spiffe://bonafide.local/agent/{name}` (VSA-1 acceptance criterion: "A login attempt presenting any SPIFFE identity whose URI does not match `spiffe://bonafide.local/agent/{name}` — including `human/*`, `service/*`, and identities outside the `bonafide.local` trust domain — is rejected"), and pins TTL = max_ttl = 300 s with no default policy (VSA-3 acceptance criterion: lease lifetime ≤ 300, not renewable).
  - Heredoc `vault policy write calendar-reader` granting only `path "database/creds/calendar_reader" { capabilities = ["read"] }`.
- Make the script executable.

**Verified when:** A grep `grep -E 'bound_spiffe_ids="spiffe://bonafide.local/agent/\*"' deploy/vault/spiffe-auth.sh` returns one match; `grep -E 'token_ttl=300' deploy/vault/spiffe-auth.sh` returns one match; `grep -E 'token_max_ttl=300' deploy/vault/spiffe-auth.sh` returns one match; `grep 'database/creds/calendar_reader' deploy/vault/spiffe-auth.sh` returns one match; running the script twice in succession against a dev-mode Vault exits zero both times (idempotent) and `vault read auth/spiffe/role/agent` shows the documented field values.

---

## T-04: [ ] Author `deploy/vault/database-engine.sh` (database secrets engine + `calendar_reader` role)

**Satisfies:** VSA-2, VSA-3, and the safety constraint **"All credentials short-lived"**

- Create `deploy/vault/database-engine.sh` exactly per `design.md` "Database secrets engine":
  - `set -euo pipefail`; `VAULT_ADDR=http://vault:8200`; `VAULT_TOKEN=devroot`.
  - `vault secrets enable database || true` (idempotent).
  - `vault write database/config/calendar plugin_name=postgresql-database-plugin allowed_roles="calendar_reader" connection_url='postgresql://{{username}}:{{password}}@postgres:5432/calendar?sslmode=disable' username="vault_manager" password="vault-manager-dev-password"` (matches the role provisioned in T-02).
  - `vault write database/roles/calendar_reader db_name=calendar creation_statements='...' revocation_statements='...' default_ttl=300 max_ttl=300 renew_statements=''` with the exact SQL from `design.md` "Database secrets engine" (CREATE ROLE with LOGIN PASSWORD VALID UNTIL `{{expiration}}`, GRANT CONNECT + USAGE + SELECT on `calendar_events`; revoke + DROP ROLE on revocation). `default_ttl=max_ttl=300` enforces VSA-3's ≤ 300 s ceiling; `renew_statements=''` explicitly forbids renewal per VSA-3 acceptance criterion "renewal attempts are denied".
- Make the script executable.

**Verified when:** `grep -E 'allowed_roles="calendar_reader"' deploy/vault/database-engine.sh` returns one match; `grep -E 'default_ttl=300' deploy/vault/database-engine.sh` and `grep -E 'max_ttl=300' deploy/vault/database-engine.sh` each return one match; `grep -E "renew_statements=''" deploy/vault/database-engine.sh` returns one match. Running the script against a dev-mode Vault (with the Postgres container up and `vault_manager` provisioned per T-02) exits zero, and `vault read database/roles/calendar_reader` shows `default_ttl=5m`, `max_ttl=5m`, and an empty `renew_statements`.

---

## T-05: [ ] Author `deploy/vault/jwt-auth-fallback.sh` (contingency: JWT auth via SPIRE OIDC discovery)

**Satisfies:** VSA-8, VSA-1 (identity binding under the fallback), VSA-3 (TTL ceiling under the fallback)

- Create `deploy/vault/jwt-auth-fallback.sh` exactly per `design.md` "Contingency: JWT auth via SPIRE OIDC discovery":
  - `set -euo pipefail`; `VAULT_ADDR=http://vault:8200`; `VAULT_TOKEN=devroot`.
  - `vault auth enable jwt || true` (idempotent).
  - `vault write auth/jwt/config oidc_discovery_url=http://spire-server:8081 bound_issuer=https://bonafide.local` — the discovery URL exposed by SWI-7.
  - `vault write auth/jwt/role/agent role_type=jwt bound_subject="spiffe://bonafide.local/agent/*" bound_audiences="vault" user_claim="sub" token_policies="calendar-reader" token_ttl=300 token_max_ttl=300 token_no_default_policy=true` — identical binding to the native role (VSA-8 acceptance criterion: "The contingency configuration enforces the same binding, TTL ceiling ... and audit-log attribution as the primary configuration; the trust domain and identity grammar do not change"). `bound_audiences="vault"` mandates the agent SDK fetch its SVID with `audience=vault` in fallback mode (resolved in `design.md` "Open decisions resolved here").
- Make the script executable.

**Verified when:** `grep -E 'bound_subject="spiffe://bonafide.local/agent/\*"' deploy/vault/jwt-auth-fallback.sh` returns one match; `grep -E 'bound_audiences="vault"' deploy/vault/jwt-auth-fallback.sh` returns one match; `grep -E 'token_ttl=300' deploy/vault/jwt-auth-fallback.sh` and `grep -E 'token_max_ttl=300' deploy/vault/jwt-auth-fallback.sh` each return one match; `grep 'oidc_discovery_url=http://spire-server:8081' deploy/vault/jwt-auth-fallback.sh` returns one match. Running the script against a dev-mode Vault with SPIRE up exits zero; `vault read auth/jwt/role/agent` shows the documented field values.

---

## T-06: [ ] Rewrite `deploy/vault/bootstrap.sh` as probe + dispatch and enable Vault audit logging

**Satisfies:** VSA-1, VSA-6 (audit log enabled), VSA-8 (mode detection + operator-readable choice), and the deletion of the TEC KV stub per `design.md` "No KV usage after VSA"

- Rewrite `deploy/vault/bootstrap.sh` exactly per `design.md` "Bootstrap dispatch (`deploy/vault/bootstrap.sh`)":
  - `set -euo pipefail`; `VAULT_ADDR=http://vault:8200`; `VAULT_TOKEN=devroot`.
  - Enable the audit device at `/vault/audit/audit.log` with `vault audit enable file file_path=/vault/audit/audit.log` (idempotent: `|| true`). The path matches the named volume from T-01.
  - Idempotent first-run cleanup of any leftover TEC KV value: `vault kv delete secret/calendar/connection 2>/dev/null || true` (per `design.md` "Open decisions resolved here" → "Existing KV value in a docker volume left over from a TEC checkout is deleted by an idempotent first-run line in the new bootstrap script").
  - Probe step: `if vault auth enable spiffe 2>&1 | grep -q "enterprise"; then ... AUTH_MODE=jwt; else bash deploy/vault/spiffe-auth.sh; AUTH_MODE=spiffe; fi`. On Enterprise rejection, run `bash deploy/vault/jwt-auth-fallback.sh` instead.
  - After the auth method dispatch, run `bash deploy/vault/database-engine.sh` unconditionally.
  - Record the chosen mode in operator-readable form: `echo "$AUTH_MODE" > deploy/vault/.auth_mode` (VSA-8 acceptance criterion: "The choice of configuration is recorded in operator-readable form so that the audit log entries ... and the registration-revocation behaviour ... can be interpreted unambiguously").
- Add `deploy/vault/.auth_mode` to `.gitignore` (per-checkout artifact, not source).
- The script must be idempotent: re-running on a fully-bootstrapped Vault must exit zero without re-enabling auth methods, without re-writing the audit device, and without changing `.auth_mode` unless the probe outcome changed.

**Verified when:** Two consecutive runs against a fresh dev-mode Vault both exit zero. After the first run: (a) `cat deploy/vault/.auth_mode` outputs either `spiffe` or `jwt`; (b) `vault audit list` includes `file/` mounted at `/vault/audit/audit.log`; (c) `vault kv get secret/calendar/connection` returns "No value found" (the TEC stub is gone); (d) `vault read database/roles/calendar_reader` returns the role (database engine was bootstrapped). A grep `grep -E '"enterprise"' deploy/vault/bootstrap.sh` returns one match (the probe substring).

---

## T-07: [ ] Implement `sdks/agent-py/bonafide_agent/vault.py` (`VaultClient` with mode-agnostic surface)

**Satisfies:** VSA-4, VSA-8 (identical SDK surface under both modes), and the safety constraints **"No static long-lived secrets ... The agent SDK never reads a credential from disk"** and **"Fail closed ... expired anything → deny"**

- Create `sdks/agent-py/bonafide_agent/vault.py` exactly per `design.md` "`vault.py` (new)":
  - Module exports `VaultClient` and a `DBLease` dataclass (frozen: `dsn: str`, `expires_at: int`). The `DBLease` definition may live in `client.py` per `design.md` "client.py — surface change"; if so, `vault.py` imports it. Either layout is acceptable; pick one.
  - `VaultClient(*, addr: str, auth_mode: str, role: str, spiffe_socket: str)` constructor:
    - Validates `auth_mode in ("spiffe", "jwt")`; any other value raises `ValueError`.
    - SVID audience is `"vault"` in both modes. The JWT-auth fallback requires `aud=vault` via `bound_audiences` (T-05). The native SPIFFE auth method does not enforce an audience claim on the presented SVID, but using the same `vault` audience for both modes (a) keeps the SDK surface mode-agnostic, (b) lets operators inspect the SVID's intent from the claim alone, and (c) means switching modes via `deploy/vault/.auth_mode` never requires re-issuing or re-targeting the SVID. Document this in a code comment citing `design.md` "Open decisions resolved here".
    - Constructs `SpireActorTokenSigner(spiffe_socket=spiffe_socket, audience=svid_audience)` (the class introduced in SWI for the actor_token path is reused here per `design.md` "vault.py (new)").
    - Calls `_login()` eagerly so a construction-time failure surfaces immediately.
  - `_login()`:
    - `jwt_svid = self._svid.fetch_actor_token()` — fresh SVID, never cached past `exp` (matches SWI-5 behaviour reused here).
    - If `auth_mode == "spiffe"`, `self._client.auth.spiffe.login(role=self._role, jwt=jwt_svid)`.
    - Elif `auth_mode == "jwt"`, `self._client.auth.jwt.jwt_login(role=self._role, jwt=jwt_svid)`.
    - Stores the returned Vault client token and `self._token_exp = int(time.time()) + int(resp["auth"]["lease_duration"])`.
  - `fetch_lease(*, path: str) -> DBLease`:
    - Refreshes the Vault login when `time.time() > self._token_exp - 30` (the `LOGIN_LEEWAY_SECONDS = 30` constant from `design.md`).
    - `resp = self._client.read(path)`; constructs `dsn = f"postgresql://{username}:{password}@postgres:5432/calendar?sslmode=disable"`.
    - Returns `DBLease(dsn=dsn, expires_at=int(time.time()) + int(resp["lease_duration"]))`.
  - The class never reads any credential from disk other than the SVID-related material the SPIRE Workload API socket provides (per `CLAUDE.md` "No static long-lived secrets" and `design.md` "client.py — surface change" — `vault_token` constructor parameter from TEC is gone).
  - On any of: SVID fetch failure, Vault login non-2xx, Vault lease read non-2xx — `VaultClient` raises `VaultReadError` (the existing error class from TEC's `sdks/agent-py/bonafide_agent/errors.py`). There is no fallback path; no cached/stale lease is ever returned (VSA-4 acceptance criterion: "no permissive fallback exists"; VSA-7 acceptance criterion: "the agent SDK ... does not return a stale credential, a cached credential, or a credential obtained by any alternative authentication path").
- Do not add a new error class; reuse `VaultReadError` from TEC.

**Verified when:** `pytest sdks/agent-py` includes tests (using a mocked `hvac.Client` and a stubbed `SpireActorTokenSigner`) that assert: (a) `auth_mode="spiffe"` calls `client.auth.spiffe.login(role=..., jwt=...)`; (b) `auth_mode="jwt"` calls `client.auth.jwt.jwt_login(role=..., jwt=...)`; (c) `auth_mode="garbage"` raises `ValueError`; (d) a `DBLease` returned from `fetch_lease` has `expires_at == int(time.time()) + lease_duration` (within a 2s tolerance); (e) any login-call exception is reraised as `VaultReadError`; (f) any `client.read(path)` exception is reraised as `VaultReadError`; (g) when `_token_exp - now < 30` at the start of `fetch_lease`, `_login()` is called again; (h) `grep -RE "open\\(.*credential" sdks/agent-py/bonafide_agent/vault.py` returns no matches (no credential is read from disk).

---

## T-08: [ ] Rewire `sdks/agent-py/bonafide_agent/client.py` — `fetch_connection` becomes `fetch_lease`

**Satisfies:** VSA-4, VSA-8 (SDK surface invariance), and the safety constraints **"The SDK never re-uses any token, lease, or credential past its `exp`"** (TEC-10 / SWI-5 carried forward) and **"No static long-lived secrets"**

- Edit `sdks/agent-py/bonafide_agent/client.py` per `design.md` "`client.py` — surface change":
  - Add `@dataclass(frozen=True) class DBLease` with fields `dsn: str` and `expires_at: int` if not defined in `vault.py` per T-07. The chosen home must be a single one to avoid duplication.
  - Remove `vault_token` and `vault_kv_path` constructor parameters; add `vault_auth_mode: Literal["spiffe", "jwt"] = "spiffe"` and keep `vault_addr: str` and `vault_role: str = "agent"`.
  - In `__init__`, construct `self._vault = VaultClient(addr=vault_addr, auth_mode=vault_auth_mode, role=vault_role, spiffe_socket=spiffe_socket)`.
  - Delete the body of `fetch_connection`. Add `fetch_lease(self) -> DBLease` that returns `self._vault.fetch_lease(path="database/creds/calendar_reader")`. The path is hard-coded per `design.md` "`client.py` — surface change" (`fetch_lease()` always targets `database/creds/calendar_reader`).
  - Update `call(...)` signature to accept `lease: DBLease` instead of `connection: str`. Inside `call`, set the request header `x-bonafide-connection: lease.dsn` (the calendar's wire contract is unchanged per `design.md` "`client.py` — surface change" → "The calendar doesn't know it's dynamic"). Also assert `time.time() < lease.expires_at` before invoking the resource; if not, raise `ExchangeError("db lease expired")` (matches TEC-10 / SWI-5 "no leeway"). No new error class.
  - `exchange()` is unchanged from SWI.
- Update `sdks/agent-py/bonafide_agent/__init__.py` to export `DBLease` alongside `BonafideAgent` and `TaskToken`.
- Delete any lingering `fetch_connection` references in the package (`grep -R fetch_connection sdks/agent-py/` must return no matches after this task).

**Verified when:** `pytest sdks/agent-py` includes tests asserting: (a) constructing `BonafideAgent` with `vault_token="..."` raises `TypeError` (the parameter is gone); (b) `BonafideAgent(..., vault_auth_mode="spiffe")` and `BonafideAgent(..., vault_auth_mode="jwt")` both construct successfully; (c) `agent.fetch_lease()` calls `VaultClient.fetch_lease(path="database/creds/calendar_reader")` exactly; (d) `agent.call(url=..., token=..., lease=DBLease(...))` sets `x-bonafide-connection` to `lease.dsn`; (e) `agent.call(..., lease=DBLease(dsn=..., expires_at=now-1))` raises `ExchangeError` and sends no HTTP request; (f) `grep -R fetch_connection sdks/agent-py/` returns no matches; (g) `grep -RE 'vault_token|vault_kv_path' sdks/agent-py/` returns no matches.

---

## T-09: [ ] Update `apps/demo-agent` driver to call `fetch_lease` and pass `DBLease` to `call`

**Satisfies:** VSA-4 (end-to-end), VSA-8 (operator selects `vault_auth_mode` via env), and the end-to-end pipeline of VSA-9

- Edit `apps/demo-agent/demo_agent/__main__.py` per `design.md` "Files created / modified / deleted" and the implicit pipeline shape:
  - Add a new env var `BONAFIDE_VAULT_AUTH_MODE` (default `spiffe`; allowed values `spiffe`, `jwt`). The driver reads `deploy/vault/.auth_mode` only via this env var being set by the smoke harness or the operator; the driver does not read the file directly (keeps the SDK surface boundary clean — `design.md` "The contingency switch is bootstrap-time, not runtime").
  - Construct `BonafideAgent(..., vault_auth_mode=os.environ.get("BONAFIDE_VAULT_AUTH_MODE", "spiffe"), vault_addr=BONAFIDE_VAULT_ADDR)`. Drop any `vault_token=` and `vault_kv_path=` arguments from the call site (they were removed in T-08).
  - Pipeline becomes:
    1. `token = agent.exchange(subject_token=user_jwt, scope=BONAFIDE_SCOPE, audience=BONAFIDE_CALENDAR_URL)`
    2. `lease = agent.fetch_lease()`
    3. `response = agent.call(url=f"{BONAFIDE_CALENDAR_URL}/events", token=token, lease=lease)`
    4. Print `response.json()` (or full status+body with `--raw`, preserved from TEC).
  - Preserve the `--print-actor-token-and-exit` flag from SWI (still used by the SWI smoke block); no new flags required by this slice's smoke (VSA-9 inspects the audit log, not the driver's output).
  - Any exception aborts non-zero (TEC-10 / SWI-5 behaviour carried forward; no fallback per VSA-4).

**Verified when:** Against a brought-up topology, `BONAFIDE_VAULT_AUTH_MODE=spiffe python -m demo_agent --user-jwt "$USER_JWT"` exits zero and prints a JSON body with `events` non-empty. `grep -RE 'vault_token|vault_kv_path|fetch_connection' apps/demo-agent/` returns no matches. A run with `BONAFIDE_VAULT_AUTH_MODE=garbage` exits non-zero before any HTTP call to the calendar (the constructor raises `ValueError` per T-07).

---

## T-10: [ ] Tighten `apps/demo-calendar` to per-request open/close (no pooling) and confirm no static credential

**Satisfies:** VSA-5, and the safety constraint **"No static long-lived secrets in source, env files, or container images outside SPIRE and Vault"**

- Edit `apps/demo-calendar/demo_calendar/main.py` to match `design.md` "Calendar app":
  - The protected route opens a transient `asyncpg.connect(x_bonafide_connection)` per request, executes the SELECT, then closes the connection in a `finally` block. No connection pool, no module-level engine, no app-state pool (per `design.md` "Calendar app" → "any connection pool the calendar holds onto would outlive the credential. The calendar's connection lifecycle is therefore per-request: open, query, close").
  - The `X-Bonafide-Connection` header continues to be a required `Header(...)` dependency; FastAPI returns 422 if absent (per VSA-5 acceptance criterion "A request that arrives without an accompanying valid database credential is rejected; the calendar app does not open a database connection on its own behalf").
  - Tighten the in-source comment near the connect call to reference the ephemeral nature of the DSN per `design.md` "Calendar app" code excerpt ("DSN is a Vault-issued ephemeral lease (VSA); we connect, query, and close in one request — pooling would outlive the credential.").
  - Remove any remaining default DSN, `BONAFIDE_CALENDAR_DSN` env var, or hard-coded connection string from the calendar app source, its `pyproject.toml`, its Dockerfile, its docker-compose env list, and any in-image config file. If none existed in TEC, this is a no-op verified by the grep below.
- Edit `apps/demo-calendar/Dockerfile` (if it sets any DB-credential env var) and the `calendar` service block in `docker-compose.yml` (if it passes one) to remove all DB credentials. The calendar's environment may contain `BONAFIDE_AUTHZ_ISSUER`, `BONAFIDE_AUTHZ_JWKS_URL`, `BONAFIDE_RESOURCE_AUDIENCE` and similar non-secret config — and nothing else database-related.

**Verified when:** `grep -RE 'postgres|password|calendar_reader|vault_manager' apps/demo-calendar/ deploy/` filtered to the calendar service's containers, image source, and compose block returns only matches in (a) comments referencing the dynamic DSN and (b) the Vault deploy scripts under `deploy/vault/` and `deploy/postgres/vault-bootstrap.sql`. Specifically `docker compose config --services | xargs -I{} docker compose config --format json | jq '.services.calendar.environment // []'` shows no entry containing a Postgres password or DSN. Booting the calendar container with no `X-Bonafide-Connection` header and invoking `GET /events` returns HTTP 422 (the FastAPI `Header(...)` rejection). A grep `grep -n 'create_pool\\|create_async_engine\\|asyncpg.create_pool' apps/demo-calendar/demo_calendar/main.py` returns no matches.

---

## T-11: [ ] Extend `scripts/bootstrap.sh` to bootstrap Vault after SPIRE is up

**Satisfies:** VSA-1 through VSA-6 (bring-up wiring), VSA-8 (probe at bootstrap), VSA-9 (smoke prerequisite)

- Edit `scripts/bootstrap.sh` per `design.md` "Files created / modified / deleted":
  - After the SWI step "Create registration entries" and before any smoke-verify call, add: `docker compose exec -T vault bash /deploy/vault/bootstrap.sh` (or the equivalent invocation given the compose volume mount path). The bootstrap script (rewritten in T-06) runs the probe, dispatches to either `spiffe-auth.sh` or `jwt-auth-fallback.sh`, runs `database-engine.sh`, and writes `deploy/vault/.auth_mode`.
  - Ensure ordering: `postgres` healthy → `vault` healthy → `spire-server` healthy → SPIRE registration entries created → `deploy/vault/bootstrap.sh` invoked. `vault` must be reachable before the script runs, and `spire-server` must be reachable because the SPIFFE auth method's `jwks_url=http://spire-server:8081/keys` (T-03) is validated when the role is created. Add `depends_on` clauses or explicit `--wait` calls as required.
  - Ensure `deploy/postgres/vault-bootstrap.sql` is in the Postgres init-scripts mount (T-02 already handles this, but the bootstrap script must verify the file is present and fail fast with a clear log line if missing — fail-closed on bring-up).
  - The bootstrap script remains idempotent: re-running on a fully-bootstrapped stack must exit zero, must not duplicate Vault auth methods, must not duplicate Postgres roles (existing CREATE ROLE statements run by Postgres init only on first volume creation; this is the postgres image's default).
- Add `deploy/vault/.auth_mode` to `.gitignore` if not already added in T-06.

**Verified when:** From a clean checkout, `./scripts/bootstrap.sh` exits zero; `cat deploy/vault/.auth_mode` outputs `spiffe` or `jwt`; `docker compose exec -T vault vault read database/roles/calendar_reader` returns the role (database engine is configured). A second `./scripts/bootstrap.sh` invocation also exits zero with no error lines emitted by Vault about already-enabled auth methods or engines. `docker compose exec -T postgres psql -U vault_manager -d calendar -c '\\d calendar_events'` shows `vault_manager` as table owner.

---

## T-12: [ ] Append VSA block to `scripts/smoke.sh`

**Satisfies:** VSA-9 (end-to-end), and (by exercise) VSA-4, VSA-5, VSA-6, VSA-7, VSA-8

- Edit `scripts/smoke.sh` to append the VSA block exactly per `design.md` "Smoke harness — VSA block", placed immediately after the SWI block and bracketed by `#--- VSA block ---` / `#--- end VSA block ---` markers per the TEC convention.
- The block exercises **both** auth modes via a `for mode in spiffe jwt` loop so that VSA-9's "passes in both ... configurations" criterion is asserted end-to-end inside the harness rather than by manual operator action. Each iteration:
  1. Writes `$mode` to `deploy/vault/.auth_mode`, then re-runs `docker compose exec -T vault bash /deploy/vault/bootstrap.sh` to reconfigure Vault (the bootstrap script is idempotent and dispatches on `.auth_mode`).
  2. Mints a fresh user JWT (`docker compose run --rm demo-human python -m demo_human --email alice@example.com`).
  3. Runs `docker compose run --rm -e BONAFIDE_VAULT_AUTH_MODE="$mode" demo-agent python -m demo_agent --user-jwt "$USER_JWT"`.
  4. Asserts with `jq -e` that the response satisfies `.acting_for == "spiffe://bonafide.local/human/alice@example.com"` and `(.events | length) > 0` (carries TEC's invariant; this is the proof that the lease worked end-to-end — VSA-4 + VSA-5).
  5. Reads `deploy/vault/.auth_mode` and tails `/vault/audit/audit.log` in the vault container for the most recent successful `login` event. Asserts: under `spiffe` mode, `.auth.metadata.spiffe_id == "spiffe://bonafide.local/agent/planner"` (CONTRACT.md §1); under `jwt` mode, `.auth.metadata.user_claim == "spiffe://bonafide.local/agent/planner"`. This is VSA-6 acceptance criterion: "Every successful credential issuance ... produces a Vault audit log entry that names the calling SPIFFE identity as the authenticated principal."
  6. Deletes the planner's SPIRE registration entry (`docker compose exec -T spire-server spire-server entry delete -spiffeID spiffe://bonafide.local/agent/planner`), sleeps long enough for the workload-api stream to propagate the removal (matches SWI's `sleep 2`), then re-runs the demo-agent and asserts the run fails (non-zero exit) with one of the documented error strings (`"permission denied" | "invalid jwt" | "no svid"` per `design.md` smoke excerpt). This is VSA-7 (fail-closed end-to-end).
  7. Restores the registration with `./deploy/spire/registrations.sh` so subsequent loop iterations and the next slice's bring-up are unaffected.
- After both iterations complete, the block emits `[smoke:VSA] OK` on success.
- The block must not modify the TEC or SWI blocks; only append.

**Verified when:** From a freshly bootstrapped topology, `./scripts/smoke.sh` exits zero and its output contains `[smoke:TEC] OK`, `[smoke:SWI] OK`, and `[smoke:VSA] OK` lines in order. The output also contains the per-iteration log lines `[smoke:VSA] mode=spiffe ...` and `[smoke:VSA] mode=jwt ...` confirming both modes ran. `grep -c 'VSA block' scripts/smoke.sh` returns `2` (start + end markers). Re-running `./scripts/smoke.sh` immediately after a successful run also exits zero (the registration restore in step 7 is sufficient to keep the harness rerunnable; the loop's final iteration leaves `.auth_mode` in a deterministic state).

---

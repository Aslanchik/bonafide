# audit-persistence: Tasks

Tasks are ordered. Each must be complete and verifiable before the next begins. If a task surfaces a spec problem, stop — update the relevant spec, re-review, then continue. One commit per task.

Status: `[ ]` todo · `[x]` done · `[~]` in progress

---

## T-01: [ ] Author Alembic migration `0001_audit.py` for the `control` schema

**Satisfies:** AUD-1, AUD-2, AUD-5, AUD-8, CONTRACT.md §9

- Create `services/control/alembic.ini` and `services/control/alembic/env.py` configured per `design.md` "Postgres schema (Alembic 0001 for the control plane)". The migration tree is owned by the control plane and lives entirely under `services/control/`.
- `env.py` reads the connection URL from `BONAFIDE_CONTROL_DATABASE_URL` (added in T-08) and runs migrations against the Postgres instance introduced in TEC.
- Create `services/control/alembic/versions/0001_audit.py` whose `upgrade()` executes exactly the DDL in `design.md` "Postgres schema":
  - `CREATE SCHEMA IF NOT EXISTS control;`
  - `control.audit_events` table with columns: `event_id TEXT PRIMARY KEY`, `schema_version TEXT NOT NULL`, `occurred_at TIMESTAMPTZ NOT NULL`, `outcome TEXT NOT NULL CHECK (outcome IN ('minted', 'denied'))`, `issuer TEXT NOT NULL`, `subject TEXT NOT NULL`, `actor TEXT NOT NULL`, `audience TEXT NOT NULL`, `scope_requested TEXT NOT NULL`, `scope_granted TEXT`, `policy_reason TEXT`, `token_jti TEXT`, `token_exp TIMESTAMPTZ`, `existing_chain JSONB NOT NULL`, `resulting_chain JSONB`, `raw JSONB NOT NULL`, `received_at TIMESTAMPTZ NOT NULL DEFAULT now()`.
  - Indexes `audit_events_subject`, `audit_events_actor`, `audit_events_occurred` (DESC), `audit_events_outcome` on `control.audit_events`.
  - `control.delegation_edges` table with columns `event_id TEXT NOT NULL REFERENCES control.audit_events(event_id) ON DELETE CASCADE`, `edge_index SMALLINT NOT NULL`, `parent_actor TEXT NOT NULL`, `child_actor TEXT NOT NULL`, primary key `(event_id, edge_index)`.
  - Indexes `delegation_edges_parent`, `delegation_edges_child`.
- `downgrade()` drops the two tables and the `control` schema.
- The migration must encode the `CHECK (outcome IN ('minted', 'denied'))` constraint verbatim (CONTRACT.md §9 enum values).

**Verified when:** Running `alembic upgrade head` against an empty Postgres database creates the `control` schema, both tables, and all six indexes. `psql -c "\dt control.*"` lists exactly `audit_events` and `delegation_edges`. `psql -c "\d control.audit_events"` shows the `outcome` CHECK constraint with the two literal values. `alembic downgrade base` cleanly removes everything.

---

## T-02: [ ] Author `deploy/postgres/control-bootstrap.sql` creating the `control_writer` role

**Satisfies:** AUD-1, AUD-2, AUD-5

- Create `deploy/postgres/control-bootstrap.sql` per `design.md` "Open decisions resolved here" → "Control-plane Postgres role":
  - Creates a Postgres role `control_writer` with a dev-only password documented inline (matches `BONAFIDE_CONTROL_DATABASE_URL` in T-08).
  - Grants `CONNECT` on the existing `calendar` database (the same Postgres instance per `DESIGN.md` §5).
  - Grants `USAGE` on schema `control` and CRUD (`SELECT, INSERT, UPDATE, DELETE`) on `control.audit_events` and `control.delegation_edges`. No grants beyond these two tables — the role must not be able to read `public.calendar_events` (the calendar fixture's schema).
  - All statements idempotent (`DO $$ BEGIN ... EXCEPTION WHEN duplicate_object THEN NULL; END $$;` for the role creation; `GRANT` statements are idempotent by nature).
- Wire this script into Postgres init via `docker-entrypoint-initdb.d/` in T-10 — this task only authors the SQL.

**Verified when:** Running this SQL against a fresh Postgres exits zero, and a second run also exits zero (idempotency). `psql -U control_writer -d calendar -c "SELECT 1 FROM control.audit_events"` succeeds. `psql -U control_writer -d calendar -c "SELECT 1 FROM public.calendar_events"` fails with `permission denied`. Running the SQL after T-01 has been applied confirms `control_writer` can `INSERT INTO control.audit_events (...) VALUES (...);` for a synthetic row and `SELECT ... FROM control.delegation_edges;`.

---

## T-03: [ ] Implement `services/control/app/audit/store.py` (single-transaction persistence + edge derivation)

**Satisfies:** AUD-1, AUD-2, AUD-5, AUD-8, CONTRACT.md §9, and the operational guarantee **"No audit event is dropped under control-plane restart or transient outage"** (idempotency half — the transport retry half is owned by T-13)

- Create `services/control/app/audit/store.py` containing the exact persistence function from `design.md` "Single-transaction persistence":
  - `async def persist_event(conn: asyncpg.Connection, event: dict, raw: bytes) -> None` opens `async with conn.transaction()` and within it: (1) `INSERT INTO control.audit_events (...) VALUES (...) ON CONFLICT (event_id) DO NOTHING RETURNING event_id`; (2) if the insert returned `None` (duplicate), return without writing edges; (3) if the event's `outcome == "minted"` and `len(event["resulting_chain"]) > 1`, write edges via `executemany`. The `raw` parameter is the verbatim wire bytes received at the ingest endpoint; it is written into the `raw` JSONB column unchanged (the parameter type is `bytes` so the call site cannot accidentally serialise a re-parsed dict).
  - A helper `_edges_from_chain(chain: list[str]) -> list[tuple[str, str]]` that, for `[tool, planner, planner-parent]`, returns `[(planner, tool), (planner-parent, planner)]`. The `edge_index` is the position in the returned list (0 = outermost edge, per `design.md` "Edge construction (deterministic)").
  - A helper `_columns(event: dict, raw: bytes) -> tuple` that maps the wire event (CONTRACT.md §9 shape) onto the positional parameter set for the `audit_events` INSERT, converting `occurred_at`/`token_exp` strings to `datetime` (timezone-aware UTC), serialising `existing_chain`/`resulting_chain` to JSON strings for their JSONB columns, and passing `raw` through unchanged for the `raw` JSONB column.
  - The `raw` column is the **verbatim** wire body received at `POST /audit/events` per AUD-1 acceptance criterion ("Every required field defined in CONTRACT.md §9 ... is recoverable from the persisted row with the same value the authz server posted"). The verbatim guarantee is structural: tests assert `json.loads(row["raw"]) == json.loads(posted_bytes)` (round-trip equality), not a byte-for-byte comparison, because Postgres JSONB normalises whitespace.
- Write unit tests at `services/control/tests/audit/test_store.py` using a real Postgres test container (or `testing.postgresql`) per `design.md` toolchain:
  - Minted event with depth-1 `resulting_chain` (one actor): writes one row in `audit_events`, zero rows in `delegation_edges` (AUD-2 acceptance criterion: "For an audit event whose `resulting_chain` contains only a single actor ... no `delegation_edges` rows are written").
  - Minted event with depth-2 `resulting_chain`: writes one row in `audit_events`, exactly one row in `delegation_edges` with `edge_index = 0`, `(parent_actor, child_actor)` matching the deterministic edge example in `design.md`.
  - Minted event with depth-3 `resulting_chain` (`[tool, planner, planner-parent]`): writes two edges with `edge_index ∈ {0, 1}` matching the table in `design.md` "Edge construction".
  - Denied event (`outcome == "denied"`): writes one row with `scope_granted IS NULL`, `policy_reason IS NOT NULL`, `token_jti IS NULL`, `token_exp IS NULL`, `resulting_chain IS NULL`, and zero rows in `delegation_edges` (AUD-2 + AUD-8).
  - Duplicate event (same `event_id` posted twice): first call writes both the event and edges; second call is a no-op (no second row in `audit_events`, no additional rows in `delegation_edges`) per AUD-1 acceptance criterion and the at-least-once contract in CONTRACT.md §9.
  - Transactional failure: a synthetic edge-insert failure (e.g. forcing a constraint violation by monkey-patching `executemany`) leaves zero rows in `audit_events` for that `event_id`, and a retry succeeds (AUD-5 acceptance criterion).

**Verified when:** `pytest services/control/tests/audit/test_store.py -k store -count=1` exits zero with all six cases passing. A grep for `executemany` in `store.py` confirms edges are inserted with a single batched call, and a grep for `async with conn.transaction()` confirms the single-transaction wrapper is present.

---

## T-04: [ ] Implement `services/control/app/audit/ingest.py` (POST /audit/events router)

**Satisfies:** AUD-1, AUD-5, AUD-8, CONTRACT.md §9

- Create `services/control/app/audit/ingest.py` exposing a FastAPI `APIRouter` with `POST /audit/events`:
  - Request body validated against a Pydantic `AuditEventIn` model whose field set is exactly the CONTRACT.md §9 shape (`schema_version`, `event_id`, `occurred_at`, `outcome`, `issuer`, `subject`, `actor`, `existing_chain`, `audience`, `scope_requested`, and the conditional `resulting_chain`, `scope_granted`, `policy_reason`, `token_jti`, `token_exp`).
  - `outcome` is a `Literal["minted", "denied"]`. `existing_chain` is `list[str]`. `resulting_chain` is `list[str] | None`. `occurred_at` and `token_exp` are `datetime` (Pydantic parses RFC 3339).
  - Validation failure (missing required field, wrong type, invalid enum value, malformed RFC 3339) returns HTTP 400 with FastAPI's default validation body. **No row is written.** This realises AUD-1 acceptance criterion: "An event whose body does not match the shape in CONTRACT.md §9 is rejected and no row is written".
  - On successful validation, reads the raw request body via `await request.body()` (FastAPI exposes the original bytes before Pydantic parsed them) and calls `store.persist_event(conn, event=parsed.model_dump(by_alias=True), raw=request_body_bytes)`. The parsed dict drives the typed columns; the raw bytes are written verbatim to the `raw` JSONB column per AUD-1.
  - Response on success is HTTP 202 with empty body. Duplicate `event_id` (no new row written, per T-03) still returns 202 — at-least-once semantics — per CONTRACT.md §9.
  - Persistence failure (DB unreachable, transaction rollback after retry) returns HTTP 500. The authz WAL drain (T-12) will re-POST.
- Mount the router via `app.include_router(audit_router)` in `services/control/app/main.py` (which exists as a stub from TEC T-27; this task extends it).
- Tests at `services/control/tests/audit/test_ingest.py` using FastAPI `TestClient` + a per-test Postgres connection:
  - 202 response for a well-formed minted event; the row exists in `control.audit_events` with `raw` equal to the JSON the client posted.
  - 202 response for a well-formed denied event; row exists with the null fields per AUD-8.
  - 400 response for: missing `event_id`; `outcome` set to `"approved"`; `occurred_at` not RFC 3339; `existing_chain` set to `null`; `resulting_chain` present but `outcome == "denied"` should still validate the shape, but the **persistence layer** writes no edges per T-03.
  - 202 for a duplicate POST of an already-persisted event (idempotency) with no additional row in either table.

**Verified when:** `pytest services/control/tests/audit/test_ingest.py -count=1` exits zero. `curl -X POST -H 'Content-Type: application/json' -d '{ "outcome": "blah" }' http://localhost:8090/audit/events` returns 400. A grep of `ingest.py` confirms it calls `await request.body()` and passes those bytes as the `raw=` keyword argument to `persist_event`, so `audit_events.raw` is the verbatim wire shape (a round-trip `json.loads(row["raw"]) == json.loads(posted_bytes)` assertion in the test proves this).

---

## T-05: [ ] Implement `services/control/app/audit/chain.py` helpers (audit-event-source reconstruction)

**Satisfies:** AUD-6, AUD-8, CONTRACT.md §10

- Create `services/control/app/audit/chain.py` containing the helpers below, but **not yet** the route handler (T-07 wires that on top).
- `_chain_from_event_row(row: asyncpg.Record, edges: list[asyncpg.Record]) -> list[str]`:
  - For a minted event: if `resulting_chain` is present in the row's typed column, return it as the canonical ordered list, current-actor-first per CONTRACT.md §9.
  - For a denied event: return `[row["actor"], *row["existing_chain"]]` per AUD-8 acceptance criterion ("`actors` reflects the actors known at the time of denial").
  - Edges are read from `control.delegation_edges` ordered by `edge_index` ASC and serve as the canonical edge-derived reconstruction. Add an internal consistency check (assert via Python `assert` or raise) that the chain derivable by walking edges (current-actor-first via reversing parent/child orientation) equals `row["resulting_chain"]`; this catches a write/read mismatch in the persistence layer. If they disagree, log ERROR and prefer `resulting_chain` as authoritative (it is the wire field per CONTRACT.md §9).
- `_first_diff(audit_chain: list[str], token_chain: list[str]) -> int | None`:
  - Returns the index of the first position where the two lists differ. If one is a prefix of the other, returns the length of the shorter list. If they are equal, returns `None`.
- Tests at `services/control/tests/audit/test_chain_helpers.py`:
  - `_chain_from_event_row` for a depth-1 minted event returns a single-actor list.
  - `_chain_from_event_row` for a depth-3 minted event returns `[tool, planner, planner-parent]` (current-actor-first per CONTRACT.md §9).
  - `_chain_from_event_row` for a denied event with `existing_chain == ["planner-parent"]` and `actor == "planner"` returns `["planner", "planner-parent"]` per AUD-8.
  - `_first_diff([a, b, c], [a, b, c])` returns `None`; `_first_diff([a, b], [a, c])` returns 1; `_first_diff([a, b], [a, b, c])` returns 2.

**Verified when:** `pytest services/control/tests/audit/test_chain_helpers.py -count=1` exits zero. A grep of `chain.py` confirms there is no code path that reconstructs from edges without cross-checking against `resulting_chain` for minted events.

---

## T-06: [ ] Implement `_chain_from_token` helper (token decode + JWKS validation for chain endpoint)

**Satisfies:** AUD-7, CONTRACT.md §6, §6.1, §10, and the safety constraint **"All credentials short-lived ... No code path may extend these"** (strict `exp` with `leeway=0`)

- Extend `services/control/app/audit/chain.py` with `_chain_from_token(token: str, jwks: JWKSCache) -> list[str]`:
  - Validates the bearer token using the same `JWKSCache` mechanics that the resource SDK uses. Import `JWKSCache` from `bonafide_resource.jwks` (already published in TEC); the control plane depends on `bonafide_resource` as a library for this. If the import shape is awkward in practice, copy the minimal validation logic into a private helper — but the validation must reject `alg=none`, validate `iss == BONAFIDE_AUTHZ_ISSUER`, validate `exp` strictly (`leeway=0`), and verify the EdDSA signature against the JWKS, per CONTRACT.md §3.
  - On any validation failure (signature mismatch, expired, unknown kid, `iss` mismatch, malformed JWT) raises a private `_TokenInvalid` exception whose message captures the failure category. Per `design.md` "Chain reconstruction endpoint": an invalid caller-supplied token is **not silently dropped** — T-07's handler surfaces it via `discrepancy` and sets `consistent = false`.
  - On success, decodes the `act` claim per CONTRACT.md §6.1 and returns the participant list `[act.sub, act.act.sub, act.act.act.sub, ...]` (inside-out reading; current-actor-first per CONTRACT.md §10).
  - The `aud` of the token is **not** validated against any specific audience — the chain endpoint accepts task tokens for any audience (the resource the token was minted for), and audience-validating it here would refuse legitimate cross-checks. Document this with a code comment that cites this design decision.
- Tests at `services/control/tests/audit/test_chain_token.py`:
  - A valid task token with `act.sub == planner` and no nested `act` returns `["planner"]` (depth-1 — AUD-7 acceptance criterion: "a depth-2 `act` chain yields the same ordered participant list as its corresponding `resulting_chain`" — depth-1 is the smaller form of the same rule).
  - A valid task token with depth-2 `act` (`act.sub == tool`, `act.act.sub == planner`) returns `["tool", "planner"]` per CONTRACT.md §6.1.
  - A token with `alg=none` raises `_TokenInvalid`.
  - A token whose `exp` is `now - 1` raises `_TokenInvalid` (strict; `leeway=0`).
  - A token whose `iss` does not match `BONAFIDE_AUTHZ_ISSUER` raises `_TokenInvalid`.
  - A token whose signature does not verify against the JWKS raises `_TokenInvalid`.

**Verified when:** `pytest services/control/tests/audit/test_chain_token.py -count=1` exits zero. A grep of `chain.py` confirms `leeway=0` is passed to the JWT decode call, and that `algorithms=["EdDSA"]` is the only algorithm accepted (no `none`, no HS256 fallback).

---

## T-07: [ ] Implement `GET /audit/chain/{event_id}` route handler + `ChainResponse` model

**Satisfies:** AUD-6, AUD-7, AUD-8, CONTRACT.md §10, and the operational guarantee **"`consistent: false` is a security signal"**

- Extend `services/control/app/audit/chain.py` with the route handler from `design.md` "Chain reconstruction endpoint":
  - `class ChainResponse(BaseModel)` with fields exactly per CONTRACT.md §10: `event_id: str`, `subject: str`, `actors: list[str]`, `current_actor: str`, `reconstructed_from: list[Literal["audit_event", "token_act_claim"]]`, `consistent: bool`, `discrepancy: dict | None`, `audience: str`, `scope: str`.
  - `@router.get("/audit/chain/{event_id}", response_model=ChainResponse)` async handler:
    1. `SELECT * FROM control.audit_events WHERE event_id = $1` — if `None`, raise `HTTPException(404, "event not found")` per AUD-6 acceptance criterion: "A `GET /audit/chain/{event_id}` request for an `event_id` that has never been persisted does not return a chain reconstructed from any other source".
    2. `SELECT edge_index, parent_actor, child_actor FROM control.delegation_edges WHERE event_id = $1 ORDER BY edge_index ASC`.
    3. `audit_chain = _chain_from_event_row(row, edges)`; `reconstructed_from = ["audit_event"]`; `consistent = True`; `discrepancy = None`.
    4. If `Authorization` header starts with `Bearer `, call `_chain_from_token(token, jwks)`:
       - Success: append `"token_act_claim"` to `reconstructed_from`. If `token_chain != audit_chain`, set `consistent = False`, set `discrepancy = {"audit_event": audit_chain, "token_act_claim": token_chain, "first_divergence_index": _first_diff(audit_chain, token_chain)}`. The `actors` field of the response remains `audit_chain` (authoritative per CONTRACT.md §10).
       - `_TokenInvalid`: set `consistent = False`, set `discrepancy = {"token_validation_error": str(e)}`. **Do not** append `"token_act_claim"` to `reconstructed_from` (validation failed, so this source did not contribute). `actors` remains `audit_chain`.
    5. For a denied event (T-05's `_chain_from_event_row` already returns `[actor, *existing_chain]`), the `Authorization` cross-check path is still entered if a token is presented, but in practice there is no minted token for a denial — callers who present a token are cross-checking a denial against an unrelated token. The handler treats it the same way: if the token validates and matches, `consistent = True`; if it doesn't match, `consistent = False`. Per AUD-8 acceptance criterion: "The response for a denied event has `reconstructed_from` containing only `"audit_event"`, since no minted token exists to provide an `act` claim, and `consistent` is `true`" — the common-case denial response (no `Authorization` header) satisfies this directly.
    6. `current_actor = audit_chain[0]` (always non-empty since denial reconstructions include at least the attempting actor).
    7. `scope = row["scope_granted"] or row["scope_requested"]` per `design.md` "Chain reconstruction endpoint".
    8. HTTP status is always **200** when the event exists, regardless of `consistent` (CONTRACT.md §10: "If `consistent` is `false`, the response status is still 200"). 404 is only for unknown `event_id`.
  - The handler accepts an optional `Authorization: Bearer <jwt>` header (FastAPI `Header(default=None)`).
- Tests at `services/control/tests/audit/test_chain.py`:
  - Unknown `event_id` → 404.
  - Minted depth-1 event, no `Authorization` header → 200, `actors == [actor]`, `current_actor == actor`, `reconstructed_from == ["audit_event"]`, `consistent == True`, `discrepancy is None`.
  - Minted depth-2 event, no `Authorization` header → 200, `actors == [tool, planner]`, `current_actor == tool`, `reconstructed_from == ["audit_event"]`, `consistent == True`.
  - Minted depth-2 event, valid `Authorization: Bearer <matching task token>` → 200, `reconstructed_from` sorted is `["audit_event", "token_act_claim"]`, `consistent == True`, `discrepancy is None`. (AUD-7 acceptance criteria 1 & 2.)
  - Minted depth-2 event, valid `Authorization` but the token's `act` chain differs from `resulting_chain` (e.g. a swapped order, forged via a parallel test fixture) → 200, `consistent == False`, `actors == audit_chain` (authoritative), `discrepancy` contains `first_divergence_index`. (AUD-7 acceptance criterion 3 & 4: "the response's `actors` array reflects the `audit_event` source as the authoritative one" and "The HTTP status code of a response with `consistent` equal to `false` is still 200".)
  - Minted event, `Authorization: Bearer <expired token>` → 200, `consistent == False`, `discrepancy` contains a `token_validation_error`, `reconstructed_from == ["audit_event"]` (the token source did not contribute).
  - Denied event, no `Authorization` header → 200, `actors == [actor, *existing_chain]`, `current_actor == actor`, `reconstructed_from == ["audit_event"]`, `consistent == True`, `policy_reason` is recoverable from the persisted row via a separate query (AUD-8 acceptance criterion 4).

**Verified when:** `pytest services/control/tests/audit/test_chain.py -count=1` exits zero with all seven cases passing. A grep of `chain.py` confirms `HTTPException(404, ...)` is the only path that returns a non-200 status from the handler (no 4xx for `consistent == False`). The response model's field list matches CONTRACT.md §10 byte-for-byte: `event_id`, `subject`, `actors`, `current_actor`, `reconstructed_from`, `consistent`, `discrepancy`, `audience`, `scope`.

---

## T-08: [ ] Wire control-plane DB + JWKS dependencies into `services/control/app/main.py`

**Satisfies:** AUD-1, AUD-2, AUD-5, AUD-6, AUD-7

- Modify `services/control/app/main.py` (the stub from TEC) to:
  - Add a lifespan handler that, on startup: (1) opens an `asyncpg.create_pool(BONAFIDE_CONTROL_DATABASE_URL)`; (2) runs `alembic upgrade head` programmatically (via the `alembic.config.Config` API) so the schema is present even on a fresh dev environment; (3) constructs a `JWKSCache(jwks_url=BONAFIDE_AUTHZ_JWKS_URL)` for the chain endpoint's cross-check.
  - Add a `get_db` dependency that acquires a connection from the pool (`async with pool.acquire() as conn: yield conn`) and a `get_jwks` dependency returning the singleton cache.
  - Include the audit ingest router (T-04) and the audit chain router (T-07) into the app.
  - Add config loading: `BONAFIDE_CONTROL_DATABASE_URL` (required, no default), `BONAFIDE_AUTHZ_ISSUER` (required, for `_chain_from_token` validation), `BONAFIDE_AUTHZ_JWKS_URL` (required). Missing required env var → fail to start with a structured log line (fail closed per `CLAUDE.md` "Fail closed").
  - The existing `GET /healthz` route is preserved.
- Update `services/control/pyproject.toml` to depend on `asyncpg`, `alembic`, `pydantic`, `PyJWT[crypto] >= 2.10`, and the local `bonafide_resource` package (for `JWKSCache`). (The JWT library was swapped from `python-jose` to `PyJWT` mid-TEC because python-jose does not implement EdDSA; see `agent-notes.md` 2026-06-04.)
- Tests at `services/control/tests/test_app.py`:
  - App starts when all env vars are set against a test Postgres; `/healthz` returns 200.
  - App refuses to start when `BONAFIDE_CONTROL_DATABASE_URL` is unset (the lifespan handler raises and the app does not bind a listener).
  - The audit router is mounted (a `GET /audit/chain/does-not-exist` returns 404, not 405 or "Not Found" from the default router).

**Verified when:** `pytest services/control/tests/test_app.py -count=1` exits zero. Running `uvicorn services.control.app.main:app` with all env vars set produces a process that responds to `GET /healthz` with 200 and to `POST /audit/events` with a body that does not match CONTRACT.md §9 with 400. Running it with `BONAFIDE_CONTROL_DATABASE_URL` unset exits non-zero before binding.

---

## T-09: [ ] Rewrite `services/control/app/policy/audit_reader.py` to read from Postgres

**Satisfies:** AUD-1 (round-trip), the OPE-introduced `GET /policies/decisions/{event_id}` contract (unchanged wire surface), and the swap noted in `design.md` "OPE denial-trace endpoint — swap"

- Rewrite `services/control/app/policy/audit_reader.py` from its OPE-introduced file-tail implementation to a SQL query:
  - `async def find_event(event_id: str, db: asyncpg.Connection) -> dict | None` returns `row["raw"]` (the verbatim JSON event posted at ingest) or `None` if no row exists.
  - The wire contract of `GET /policies/decisions/{event_id}` (OPE) is **unchanged** — only the implementation changes. The denial-trace endpoint still returns the same shape it returned in OPE.
- Delete any file-tail / `aiofiles` code path that was in the OPE implementation. Per `design.md` "Files created / modified / deleted": **the file-tail code is deleted, no dual-write, no fallback**.
- Update the OPE-introduced denial-trace tests so they exercise the SQL path against a test Postgres (the test fixture inserts a synthetic row into `control.audit_events` and asserts the endpoint returns its `raw` JSON verbatim).

**Verified when:** `pytest services/control/tests/policy/test_audit_reader.py -count=1` exits zero against the new SQL backing. A grep `grep -rn "aiofiles\|tail" services/control/app/policy/` returns zero matches (the file-scan code is gone). `grep -rn "fetchrow" services/control/app/policy/audit_reader.py` confirms the SQL-backed reader is in place. The wire surface of `GET /policies/decisions/{event_id}` is unchanged from OPE (its response body shape is identical to the OPE tasks.md verification step).

---

## T-10: [ ] Update `docker-compose.yml`: add `audit-wal` volume, control DB access, control migration on startup

**Satisfies:** AUD-3, AUD-4, AUD-5

- Modify `docker-compose.yml` to:
  - Add a named volume `audit_wal` mounted on the `authz` container at `/var/lib/bonafide/audit-wal` (the directory `audit/wal.go` opens in T-11). The volume must persist across `docker compose stop authz && docker compose start authz` so the WAL survives a single authz restart per AUD-4 acceptance criterion 3.
  - Set the env var `BONAFIDE_AUTHZ_AUDIT_WAL_DIR=/var/lib/bonafide/audit-wal` on the `authz` container (added in T-13's config update).
  - Set `BONAFIDE_CONTROL_AUDIT_URL=http://control:8090/audit/events` on the `authz` container (consumed by T-13).
  - Remove the TEC-introduced env var `BONAFIDE_AUTHZ_AUDIT_PATH` and remove the audit log file volume from `authz`. Per `design.md` "Audit file removal triggers": **the TEC file-backed emitter is fully removed; no dual-write**.
  - Set `BONAFIDE_CONTROL_DATABASE_URL=postgresql://control_writer:<dev-password>@postgres:5432/calendar` on the `control` container. The password matches `deploy/postgres/control-bootstrap.sql` (T-02).
  - Set `BONAFIDE_AUTHZ_ISSUER` and `BONAFIDE_AUTHZ_JWKS_URL` on the `control` container (consumed by T-08's lifespan).
  - Mount `deploy/postgres/control-bootstrap.sql` into the `postgres` container at `/docker-entrypoint-initdb.d/02-control-bootstrap.sql` so the `control_writer` role is created on fresh bring-up. The TEC `init.sql` keeps its `01-` prefix; the control bootstrap runs second.
  - The `control` container's command runs migrations on startup (already wired in T-08 lifespan). Alternatively, an entrypoint script can run `alembic upgrade head` before launching uvicorn — either is acceptable as long as migrations are idempotent and run before the listener binds.
- The `control` container `depends_on: postgres` with a health condition so the DB is ready before migrations attempt to run.

**Verified when:** `docker compose up -d --wait` from a clean checkout (after `./scripts/bootstrap.sh` produces the keys) succeeds. `docker compose exec postgres psql -U control_writer -d calendar -c "\dt control.*"` lists `audit_events` and `delegation_edges`. `docker compose exec authz ls /var/lib/bonafide/audit-wal` exits zero (directory exists). `docker compose exec authz env | grep BONAFIDE_AUTHZ_AUDIT_PATH` returns nothing (the TEC env var is gone). `docker compose stop authz && docker compose start authz` preserves the contents of `/var/lib/bonafide/audit-wal` (verified by writing a sentinel file before stop and reading it after start).

---

## T-11: [ ] Implement `services/authz/internal/audit/wal.go` (durable buffer)

**Satisfies:** AUD-4, and the operational guarantee **"No audit event is dropped under control-plane restart or transient outage. Every event for every decided exchange is persisted exactly once given enough time, satisfying the at-least-once contract in CONTRACT.md §9 and surviving a single authz restart per AUD-4"**

- Add `github.com/tidwall/wal` to `services/authz/go.mod` (per `CLAUDE.md` "Stack pins" the WAL choice is approved in `design.md`).
- Create `services/authz/internal/audit/wal.go` per `design.md` "`audit/wal.go`":
  - `type WAL struct { log *wal.Log; nextWrite uint64 }`.
  - `OpenWAL(dir string) (*WAL, error)` opens `wal.Log` at `dir`, reads `LastIndex()` to initialise `nextWrite = last + 1`. A missing directory is created with `0o700`.
  - `Append(e Event) error` JSON-marshals the event and writes it at `nextWrite`, then increments. Returns the underlying WAL write error on failure (the WAL handles fsync internally).
  - `ReadFromCursor() (idx uint64, body []byte, ok bool, err error)`: reads the entry at the WAL's first unread index (the index just past the last successfully POSTed event, persisted by the WAL library). Returns `ok = false` when nothing is pending.
  - `AdvanceCursor(to uint64) error`: advances the WAL's read cursor (using `wal.Log.TruncateFront(to + 1)` or equivalent) so the entry at `to` is permanently removed. The cursor must be **durable** across an authz restart per AUD-4 acceptance criterion 3.
  - `Close() error`.
- Tests at `services/authz/internal/audit/wal_test.go`:
  - Open WAL in a tempdir, append three events, `ReadFromCursor` returns the first; `AdvanceCursor(idx1)`; `ReadFromCursor` returns the second; etc.
  - Open WAL, append two events without advancing, close, reopen at the same dir: `ReadFromCursor` returns the first unread event (restart durability per AUD-4 acceptance criterion 3).
  - Append after a restart: subsequent writes use a `nextWrite` that does not collide with the persisted last index.
  - `ReadFromCursor` on an empty WAL returns `ok = false, err = nil`.

**Verified when:** `go test ./internal/audit/... -run TestWAL -count=1` exits zero with all four cases passing. A grep `grep -n 'AdvanceCursor\|ReadFromCursor\|Append' services/authz/internal/audit/wal.go` confirms the three public WAL operations are present. The compiled test binary is removed after the run per `CLAUDE.md` "Delete compiled binaries after build/test".

---

## T-12: [ ] Implement `services/authz/internal/audit/http.go` (HTTP emitter + drain goroutine + retry policy)

**Satisfies:** AUD-3, AUD-4, and the operational guarantee **"The mint path never blocks on audit. No code path in the authz server allows audit delivery (success, failure, latency, or buffer state) to alter the wall-clock latency, status code, or body of a token-exchange response per CONTRACT.md §7 and §8"**

- Create `services/authz/internal/audit/http.go` per `design.md` "`audit/http.go`":
  - `httpEmitter` struct fields: `wal *WAL`, `client *http.Client` with `Timeout: 5 * time.Second`, `target string`, `ch chan struct{}` (buffered 1, wakes the drain goroutine).
  - `NewHTTP(wal *WAL, target string) *httpEmitter` starts the drain goroutine before returning.
  - `Emit(ctx context.Context, ev Event) error`:
    - Synchronous WAL `Append` (durable, microseconds — the WAL library handles batched fsyncs).
    - Non-blocking nudge on `ch` (`select { case e.ch <- struct{}{}: default: }`).
    - Returns `nil` on success. On WAL `Append` failure (disk-full, WAL corruption), logs ERROR with `event=audit_wal_append_failed` and returns the error. **The exchange handler logs this and still returns the success response to the caller** per the AUD-3 acceptance criterion ("There is no code path in the authz server in which a failure to deliver an audit event to the control plane causes the token-exchange response to fail").
  - `drain()` runs forever:
    - `ReadFromCursor()`; if `!ok`, block on `<-e.ch`; loop.
    - POST to `target` with `Content-Type: application/json`, body = raw WAL entry bytes (no re-marshalling).
    - On network error or HTTP 5xx: `slog.Warn("audit_post_retry", ...)`, `time.Sleep(backoff)`, `backoff = backoffNext(backoff)`, **do not** advance the cursor — retry the same event (AUD-4 acceptance criterion 1: "is retained in a local buffer and re-attempted until the control plane accepts it").
    - On HTTP 400: per `design.md` "Open decisions resolved here" → "400 from control plane = drop and advance": `slog.Error("audit_post_400_dropping_event", "idx", idx)` and **advance the cursor** to keep the queue healthy. This is the only path that drops a buffered event, and it is the control plane telling us the schema is wrong — the event will never persist.
    - On HTTP 2xx (including 202): `AdvanceCursor(idx)`, reset backoff to 1 second.
    - On HTTP 3xx or non-200/non-202 2xx that isn't 400: treat as 5xx (retry without advancing). The control plane's documented responses are 202 (ingested), 400 (schema rejection), and 5xx (persistence failure).
  - `backoffNext(d) := min(d * 2, 1 * time.Minute)` per `design.md` "`audit/http.go`".
- Tests at `services/authz/internal/audit/http_test.go`:
  - Happy path: HTTP mock returns 202; `Emit` returns immediately (timed: < 5 ms); after up to 1 second the WAL cursor has advanced past the event.
  - Retry: HTTP mock returns 500 three times then 202; the same event body is POSTed each time (no re-emission from authz's caller), and the cursor advances only after the 202.
  - 400 drop: HTTP mock returns 400; the cursor advances and an ERROR log line is emitted with the WAL index.
  - Non-blocking: register a slow-responding mock (10 second response delay) and call `Emit` 1000 times in a tight loop; assert every call returns in less than 10 ms (the mint path is decoupled from POST latency per AUD-3 acceptance criterion 3).
  - Restart durability: open WAL + emitter; emit two events with the mock unreachable (network error); shut down emitter (`Close`); confirm the WAL still has both events; open a new WAL + emitter with the mock now reachable; assert both events are POSTed.
  - No duplicate at the row level: simulate a flaky network where the mock returns 500 after writing the row (a 500 wrapping a row already persisted via `ON CONFLICT DO NOTHING`); the authz retries; the mock then returns 202 on retry; assert the database has exactly one row for the `event_id`. (The idempotency lives in T-03's `ON CONFLICT DO NOTHING` clause; this test cross-checks the end-to-end behaviour.)

**Verified when:** `go test ./internal/audit/... -run TestHTTPEmitter -count=1` exits zero with all six cases passing. A grep `grep -n 'AdvanceCursor' services/authz/internal/audit/http.go` confirms the cursor is advanced **only** on 2xx and 400 — never on network errors or 5xx. A grep `grep -n 'Timeout' services/authz/internal/audit/http.go` confirms the HTTP client has a finite timeout (no goroutine pinned forever waiting on a half-open TCP connection).

---

## T-13: [ ] Delete `services/authz/internal/audit/file.go`, update `cmd/authz/main.go` wiring, drop `BONAFIDE_AUTHZ_AUDIT_PATH` from config

**Satisfies:** AUD-3, AUD-4, AUD-5, AUD-6, AUD-7, AUD-8, and `design.md` "TEC's file-backed audit emitter is fully removed. No fallback path; no dual-write"

- Delete `services/authz/internal/audit/file.go` and its tests (`file_test.go`). The interface in `audit/audit.go` is unchanged per `design.md`.
- Modify `services/authz/internal/config/config.go` to:
  - Remove `BONAFIDE_AUTHZ_AUDIT_PATH`.
  - Add `BONAFIDE_AUTHZ_AUDIT_WAL_DIR` (required; default `/var/lib/bonafide/audit-wal`).
  - Add `BONAFIDE_CONTROL_AUDIT_URL` (required; default `http://control:8090/audit/events`).
  - Missing values → fail-closed startup error.
- Modify `services/authz/cmd/authz/main.go` per `design.md` "`cmd/authz/main.go` — wiring":
  - Replace the `audit.NewFileEmitter(cfg.AuditPath)` construction with `walLog, _ := audit.OpenWAL(cfg.AuditWalDir); emitter := audit.NewHTTP(walLog, cfg.ControlAuditURL)`.
  - On `OpenWAL` failure: log `audit_wal_open_failed` at ERROR and `os.Exit(1)` (fail closed).
- Confirm no remaining import of `audit.FileEmitter` anywhere in `services/authz/`. A `go build ./...` succeeds with the file deleted.
- The exchange handler in `services/authz/internal/exchange/handler.go` is **unchanged** — the `audit.Emitter` interface and its `Emit(ctx, event)` call site stay identical to TEC. This is the AUD-3 acceptance criterion 4: "There is no code path in the authz server in which a failure to deliver an audit event to the control plane causes the token-exchange response to fail, change status, or change body shape" — preserving the call site verifies that.

**Verified when:** `go build ./...` inside `services/authz` exits zero with no references to `file.go`. A grep `grep -rn 'FileEmitter\|AuditPath\|BONAFIDE_AUTHZ_AUDIT_PATH' services/authz/` returns zero matches. `grep -n 'audit.NewHTTP\|audit.OpenWAL' services/authz/cmd/authz/main.go` confirms the new wiring. Running the binary with all env vars set produces a process that mints a task token whose audit event lands in `control.audit_events` (verified by querying Postgres). The compiled binary is removed after the test per `CLAUDE.md`.

---

## T-14: [ ] Unit test: minted exchange's mint path latency is unaffected by control-plane unreachability (AUD-3 stress test)

**Satisfies:** AUD-3, and the operational guarantee **"The mint path never blocks on audit"**

- Add `services/authz/internal/exchange/handler_audit_decouple_test.go`:
  - Construct a `Handler` with a real `audit.NewHTTP(...)` whose `target` points at `http://127.0.0.1:<port>` where `<port>` is an unprivileged port the test has just bound and immediately closed (so connect returns `ECONNREFUSED` deterministically, regardless of UID; do not use port 1 — Linux returns `EACCES` for unprivileged processes connecting to ports below 1024). Use a `wal.Log` in a tempdir.
  - Make 50 sequential successful token-exchange requests against the handler via `httptest.NewRecorder()`. Measure the wall-clock latency of each.
  - Assert every request returns HTTP 200 in less than 50 ms (the only audit-related work on the mint path is the synchronous WAL `Append`, which is microseconds).
  - Assert that after the 50 mints, the WAL contains exactly 50 entries (`ReadFromCursor` walked 50 times returns 50 distinct events).
  - Assert that no event has been POSTed (target unreachable) — the drain goroutine has not advanced the cursor — but the **mint path returned** 200 to all 50 callers anyway. This is the AUD-3 acceptance criterion 3 verbatim: "The wall-clock latency of any successful or denied token-exchange response is not measurably affected by the control plane being unreachable".

**Verified when:** `go test ./internal/exchange/... -run TestHandlerAuditDecouple -count=1` exits zero. The latency assertions print the actual median latency in the test log so an operator reviewing the test output sees the decoupling is real (typical: sub-millisecond).

---

## T-15: [ ] End-to-end test: denied exchange is persisted and reconstructable via the chain endpoint

**Satisfies:** AUD-8, CONTRACT.md §9 (denial shape) + §10

- Add `services/control/tests/integration/test_denied_chain.py` (or extend an existing integration test file) that:
  - Brings up the AUD stack against a test Postgres + a real `authz` process (or a Python test that bypasses authz and POSTs a synthetic denied event directly to `POST /audit/events` — the goal is to exercise the control-plane chain endpoint, not authz).
  - POSTs a denied event matching CONTRACT.md §9: `outcome: "denied"`, `policy_reason: "no_matching_allow_entry"`, `scope_granted: null`, `token_jti: null`, `token_exp: null`, `resulting_chain: null`, `existing_chain: ["spiffe://bonafide.local/agent/planner-parent"]`, `actor: "spiffe://bonafide.local/agent/planner"`.
  - `GET /audit/chain/{event_id}` returns 200 with `actors == ["spiffe://bonafide.local/agent/planner", "spiffe://bonafide.local/agent/planner-parent"]`, `current_actor == "spiffe://bonafide.local/agent/planner"`, `reconstructed_from == ["audit_event"]`, `consistent == True`.
  - Separately confirms via direct SQL that `policy_reason` is recoverable from `control.audit_events` for that `event_id` per AUD-8 acceptance criterion 4 ("The denial reason persisted per AUD-1 is recoverable for a denied event ... so that callers can distinguish 'denied' from 'minted' without re-running the policy gate").

**Verified when:** `pytest services/control/tests/integration/test_denied_chain.py -count=1` exits zero. The assertion that `actors` ordering matches AUD-8 (actor first, then existing_chain in order) is encoded as a direct list-equality check.

---

## T-16: [ ] Smoke harness AUD block + outage test + denied-event reconstruction

**Satisfies:** AUD-9 (the slice's end-to-end requirement)

- Extend `scripts/smoke.sh` by appending a single block delimited by `#--- AUD block ---` and `#--- end AUD block ---` markers, exactly per `design.md` "Smoke harness — AUD block":
  - **Minted reconstruction (no token):** Mint a user JWT via `demo-human`; drive `demo-agent` with `--print-task-token` to obtain a task token. Extract the `jti` claim. After a 1-second drain delay, `curl -fsSL http://control.bonafide.local:8090/audit/chain/$EVENT_ID` and assert with `jq -e`:
    - `.subject == "spiffe://bonafide.local/human/alice@example.com"`.
    - `.actors == ["spiffe://bonafide.local/agent/planner"]` per AUD-9 acceptance criterion 2 ("the `actors` array returned by the endpoint, in order, equals the `resulting_chain` of the corresponding audit event").
    - `.current_actor == "spiffe://bonafide.local/agent/planner"` per AUD-9 acceptance criterion 4 ("the `current_actor` of the response equals the SPIFFE ID carried in the `act.sub` of the minted task token").
    - `.reconstructed_from == ["audit_event"]`.
    - `.consistent == true`.
  - **Minted reconstruction (with token, dual source):** Repeat the GET with `Authorization: Bearer $TASK_TOKEN`. Assert `(.reconstructed_from | sort) == ["audit_event", "token_act_claim"]` per AUD-9 acceptance criterion 3 and `.consistent == true`.
  - **Outage test (AUD-4 durability):** `docker compose stop control`. Drive `demo-agent` to mint another exchange. `docker compose start control`. Wait 3 seconds (WAL drain backoff + control startup). `curl` the chain endpoint for the new `event_id` and assert `.event_id == $EVENT_ID3` (the event was buffered locally during the outage and delivered after control came back up — AUD-4 acceptance criteria 1, 2, 5).
  - **Denied event reconstruction:** Drive `demo-agent` with a scope the policy denies (`calendar:write:alice@example.com`); capture the `event_id` from the error trail (the agent SDK surfaces it per OPE's denial-trace contract). After a 1-second drain delay, `curl` the chain endpoint and assert `.reconstructed_from == ["audit_event"]` and `.consistent == true`.
- The AUD block must not modify or delete any prior block. Per AUD-9 acceptance criterion 5: "All prior smoke-check blocks from `token-exchange-core`, `spire-workload-identity`, `vault-spiffe-auth`, and `opa-policy-engine` continue to pass unchanged".
- End the block with `echo "[smoke:AUD] OK"` so an operator reading the smoke output sees the slice's pass marker.

**Verified when:** `./scripts/smoke.sh` exits zero against a freshly brought-up topology (`./scripts/bootstrap.sh` followed by `./scripts/smoke.sh`). The output includes `[smoke:TEC] OK`, `[smoke:SWI] OK`, `[smoke:VSA] OK`, `[smoke:OPE] OK`, and `[smoke:AUD] OK` in order. A grep `grep -c 'AUD block' scripts/smoke.sh` returns 2 (start + end markers). Running smoke a second time also exits zero (the WAL has been drained, no stale events from the first run interfere — idempotency via `ON CONFLICT DO NOTHING` per T-03).

---

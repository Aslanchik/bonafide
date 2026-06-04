# audit-persistence: Design

## Overview

This slice swaps the file-backed `audit.Emitter` from TEC for an HTTP-backed implementation that POSTs every audit event to the control plane. The control plane writes two tables (`audit_events` and `delegation_edges`) in a single transaction. Authz keeps a small on-disk buffer so audit emission survives a control-plane outage and a single authz restart. The mint path never blocks on emission — the channel-and-goroutine pattern from TEC is preserved; only the drain target changes.

The control plane gains `GET /audit/chain/{event_id}` which returns the chain-reconstruction response defined in CONTRACT.md §10. The endpoint always reconstructs from the persisted audit event (the canonical source); if the caller also presents a task token via `Authorization: Bearer <jwt>`, the endpoint validates it against the authz JWKS and includes `token_act_claim` in the `reconstructed_from` list, cross-checking the two sources and reporting `consistent: true | false`. A `false` value is a security signal per CONTRACT.md §10 — the endpoint's status code stays 200 (per §10), the authoritative `actors` array comes from the audit event, and a `discrepancy` field describes the mismatch.

Everything else from TEC, SWI, VSA, and OPE is unchanged. The policy gate, the SPIRE-issued SVIDs, Vault-issued leases, the act-chain builder, the resource SDK, the OPE denial-trace endpoint — all carry forward. The OPE denial-trace endpoint's `audit_reader` swaps from file-tail to a SQL query; that swap is in this slice's scope too.

---

## Stack (additions only)

| Concern | Choice | Why |
|---|---|---|
| Postgres async driver (control plane) | `asyncpg` | Already used by the calendar app; consistent toolchain |
| Migrations | `alembic` | Schema is stable enough; Alembic for the two tables |
| Buffer storage on authz | `tidwall/wal` (`github.com/tidwall/wal`) | Lightweight append-only WAL in Go; survives restart; small dep |
| JWT validation (control plane) | **`PyJWT[crypto] >= 2.10`** | Same lib the resource SDK uses; can be imported from `bonafide_resource.jwks`. The earlier pin to `python-jose[cryptography]` was wrong — it does not implement EdDSA; see `agent-notes.md` 2026-06-04. |

The WAL choice: a single-file append-log keyed by a monotonically increasing index. Authz writes each pending event; the drain goroutine reads from the WAL and POSTs; successful POSTs advance the WAL's read cursor. The cursor is persisted, so a restart resumes from where the drain left off.

---

## Repo additions and modifications

```
+ services/control/alembic/                   # NEW: control plane gets its own migration tree
+   ├── env.py
+   └── versions/0001_audit.py
+
+ services/control/app/audit/
+   ├── ingest.py                              # NEW: POST /audit/events
+   ├── chain.py                               # NEW: GET /audit/chain/{event_id}
+   └── store.py                               # NEW: async DB writers
+
+ services/control/app/policy/audit_reader.py  # MODIFIED (OPE existing file) — swap to SQL
+
+ services/authz/internal/audit/http.go        # NEW: HTTP impl behind audit.Emitter interface
+ services/authz/internal/audit/wal.go         # NEW: WAL-backed durable buffer
+
- services/authz/internal/audit/file.go        # DELETED — TEC impl

  services/authz/internal/audit/audit.go       # interface unchanged
  services/authz/cmd/authz/main.go             # Constructs http.NewEmitter(...) wrapping the WAL

  docker-compose.yml                           # control gets Postgres access; same Postgres as calendar
  scripts/smoke.sh                             # AUD block appended
```

`audit.Emitter` from TEC stays interface-stable. The WAL sits behind the HTTP emitter as a buffer; from the caller's view, `Emitter.Emit(ctx, event)` returns immediately as before.

---

## Postgres schema (Alembic 0001 for the control plane)

The control plane gets its own schema (`control`) in the existing Postgres instance — keeps the calendar app's schema (`public`) and the audit schema separate without spinning up a second Postgres.

```sql
CREATE SCHEMA IF NOT EXISTS control;

-- One row per CONTRACT.md §9 audit event.
CREATE TABLE control.audit_events (
    event_id        TEXT PRIMARY KEY,        -- ULID; same value as the minted token's jti
    schema_version  TEXT NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    outcome         TEXT NOT NULL CHECK (outcome IN ('minted', 'denied')),
    issuer          TEXT NOT NULL,
    subject         TEXT NOT NULL,           -- the human SPIFFE ID
    actor           TEXT NOT NULL,           -- the SPIFFE ID of the actor_token
    audience        TEXT NOT NULL,
    scope_requested TEXT NOT NULL,
    scope_granted   TEXT,                    -- null on denial
    policy_reason   TEXT,                    -- null on minted
    token_jti       TEXT,                    -- null on denial
    token_exp       TIMESTAMPTZ,             -- null on denial
    existing_chain  JSONB NOT NULL,          -- ordered list (outermost-first)
    resulting_chain JSONB,                   -- ordered list (current-actor-first); null on denial
    -- The raw event body (CONTRACT.md §9) is kept verbatim for round-trip per AUD-1.
    raw             JSONB NOT NULL,
    received_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_events_subject     ON control.audit_events (subject);
CREATE INDEX audit_events_actor       ON control.audit_events (actor);
CREATE INDEX audit_events_occurred    ON control.audit_events (occurred_at DESC);
CREATE INDEX audit_events_outcome     ON control.audit_events (outcome);

-- One row per (parent_actor, child_actor) pair within an exchange.
-- parent_actor is the SPIFFE ID that delegated TO child_actor.
-- depth 1: zero edges (one actor; nothing to relate).
-- depth 2: one edge (existing_chain[0] -> outermost actor).
-- depth N: N-1 edges, ordered (outermost edge first).
CREATE TABLE control.delegation_edges (
    event_id     TEXT NOT NULL REFERENCES control.audit_events(event_id) ON DELETE CASCADE,
    edge_index   SMALLINT NOT NULL,           -- 0 = outermost (current_actor -> first prior); 1 = next; ...
    parent_actor TEXT NOT NULL,               -- the older actor in the (parent, child) edge
    child_actor  TEXT NOT NULL,               -- the newer actor (closer to current)
    PRIMARY KEY (event_id, edge_index)
);
CREATE INDEX delegation_edges_parent ON control.delegation_edges (parent_actor);
CREATE INDEX delegation_edges_child  ON control.delegation_edges (child_actor);
```

The `raw` JSONB column stores the entire posted body verbatim. This is the round-trip guarantee from AUD-1: every CONTRACT.md §9 field is recoverable from `raw`. The typed columns are denormalized for query performance and chain-reconstruction without re-decoding `raw`.

The `(audit_events.event_id, delegation_edges.event_id)` FK with `ON DELETE CASCADE` keeps the two tables coherent. Single-statement persistence (below) writes both atomically.

**Edge construction (deterministic):** for an event with `resulting_chain = [tool, planner, planner-parent]`, the edges are:

| edge_index | parent_actor | child_actor |
|---|---|---|
| 0 | planner | tool |
| 1 | planner-parent | planner |

`edge_index 0` is the outermost edge (the most recent delegation). Walking from `edge_index = 0` and reversing the `(parent, child)` orientation reconstructs the chain current-actor-first per CONTRACT.md §10.

For a depth-1 event (`resulting_chain = [planner]`), no edges are written (per AUD-2).

For a denied event, no edges are written (per AUD-2).

---

## Single-transaction persistence (AUD-5)

```python
# services/control/app/audit/store.py
async def persist_event(conn: asyncpg.Connection, event: dict) -> None:
    async with conn.transaction():
        # Idempotency: an event whose event_id is already persisted is a no-op.
        # ON CONFLICT (event_id) DO NOTHING returns 0 rows if it was a duplicate;
        # we skip the edges insert in that case.
        inserted = await conn.fetchval("""
            INSERT INTO control.audit_events
                (event_id, schema_version, occurred_at, outcome, issuer,
                 subject, actor, audience, scope_requested, scope_granted,
                 policy_reason, token_jti, token_exp,
                 existing_chain, resulting_chain, raw)
            VALUES
                ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
                 $14::jsonb, $15::jsonb, $16::jsonb)
            ON CONFLICT (event_id) DO NOTHING
            RETURNING event_id
        """, *_columns(event))

        if inserted is None:
            return  # duplicate; no-op (at-least-once delivery contract, CONTRACT.md §9)

        if event["outcome"] == "minted" and len(event["resulting_chain"]) > 1:
            edges = _edges_from_chain(event["resulting_chain"])
            await conn.executemany("""
                INSERT INTO control.delegation_edges (event_id, edge_index, parent_actor, child_actor)
                VALUES ($1, $2, $3, $4)
            """, [(event["event_id"], i, parent, child) for i, (parent, child) in enumerate(edges)])
```

The `async with conn.transaction()` block is the single transaction. A failure in `executemany` raises and the `audit_events` row rolls back; the WAL on the authz side does not advance its cursor, and the same event is re-POSTed.

`_edges_from_chain([tool, planner, planner-parent])` returns `[(planner, tool), (planner-parent, planner)]`.

---

## Chain reconstruction endpoint (AUD-6, AUD-7)

```python
# services/control/app/audit/chain.py
@router.get("/audit/chain/{event_id}", response_model=ChainResponse)
async def get_chain(
    event_id: str,
    authorization: str | None = Header(default=None),
    db: asyncpg.Connection = Depends(get_db),
) -> ChainResponse:
    row = await db.fetchrow("SELECT * FROM control.audit_events WHERE event_id = $1", event_id)
    if row is None:
        raise HTTPException(404, "event not found")

    edges = await db.fetch(
        "SELECT edge_index, parent_actor, child_actor "
        "FROM control.delegation_edges WHERE event_id = $1 ORDER BY edge_index",
        event_id,
    )

    audit_chain = _chain_from_event_row(row, edges)  # always present
    reconstructed_from = ["audit_event"]
    discrepancy: dict | None = None
    consistent = True

    # Optional cross-check: if caller presents a task token, decode it and compare.
    if authorization is not None and authorization.lower().startswith("bearer "):
        token = authorization.split(" ", 1)[1]
        try:
            token_chain = await _chain_from_token(token, db_jwks)
            reconstructed_from.append("token_act_claim")
            if token_chain != audit_chain:
                consistent = False
                discrepancy = {
                    "audit_event": audit_chain,
                    "token_act_claim": token_chain,
                    "first_divergence_index": _first_diff(audit_chain, token_chain),
                }
        except _TokenInvalid as e:
            # Per CONTRACT.md §10: if the caller-supplied token is invalid, we do
            # NOT silently drop it — we surface the failure but still return the
            # audit_event reconstruction.
            discrepancy = {"token_validation_error": str(e)}
            consistent = False

    return ChainResponse(
        event_id      = event_id,
        subject       = row["subject"],
        actors        = audit_chain if consistent else audit_chain,  # audit is authoritative
        current_actor = audit_chain[0] if audit_chain else row["actor"],
        reconstructed_from = reconstructed_from,
        consistent    = consistent,
        discrepancy   = discrepancy,
        audience      = row["audience"],
        scope         = row["scope_granted"] or row["scope_requested"],
    )

class ChainResponse(BaseModel):
    event_id:          str
    subject:           str
    actors:            list[str]
    current_actor:     str
    reconstructed_from: list[Literal["audit_event", "token_act_claim"]]
    consistent:        bool
    discrepancy:       dict | None
    audience:          str
    scope:             str
```

**Denial events:** `audit_chain = [event["actor"]] + event["existing_chain"]` — the actor that attempted the exchange plus any prior actors from `existing_chain` (per AUD-8). `current_actor` is `event["actor"]`. `reconstructed_from = ["audit_event"]` (no minted token exists). `consistent = True` (only one source).

**`first_divergence_index`** is the first position where `audit_chain[i] != token_chain[i]`. Useful for operators to see which hop went wrong.

The `_chain_from_token` helper validates the bearer token against the authz JWKS (using the same `JWKSCache` from `bonafide_resource.jwks`) — `iss`, `aud`, `exp` all checked. If validation fails, we report `token_validation_error` in `discrepancy` and `consistent = false`. This catches the case where a caller passes a forged or expired token thinking they were proving something.

---

## Authz: WAL-backed HTTP emitter

### Interface (unchanged from TEC)

```go
// services/authz/internal/audit/audit.go (unchanged)
type Emitter interface {
    Emit(ctx context.Context, event Event) error
}
```

The exchange handler's call site is unchanged: `audit.Emitter.Emit(ctx, event)`.

### `audit/wal.go`

```go
// services/authz/internal/audit/wal.go
package audit

import (
    "context"
    "encoding/json"
    "github.com/tidwall/wal"
)

// WAL is an append-only durable queue of pending audit events.
// Authz writes here synchronously inside Emit() (microseconds), then a separate
// goroutine drains the WAL and POSTs to the control plane.
type WAL struct {
    log         *wal.Log
    nextWrite   uint64
}

func OpenWAL(dir string) (*WAL, error) {
    l, err := wal.Open(dir, nil)
    if err != nil {
        return nil, err
    }
    last, _ := l.LastIndex()
    return &WAL{log: l, nextWrite: last + 1}, nil
}

func (w *WAL) Append(e Event) error {
    body, _ := json.Marshal(e)
    if err := w.log.Write(w.nextWrite, body); err != nil {
        return err
    }
    w.nextWrite++
    return nil
}

// ReadFromCursor returns pending events from the WAL's read cursor onward.
func (w *WAL) ReadFromCursor() (idx uint64, body []byte, ok bool, err error) { ... }
func (w *WAL) AdvanceCursor(to uint64) error { ... }
```

The WAL file lives at `/var/lib/bonafide/audit-wal/` in a docker volume. The cursor (last successfully POSTed index) is persisted by the WAL library itself; a restart resumes from there.

### `audit/http.go`

```go
// services/authz/internal/audit/http.go
package audit

import (
    "context"
    "encoding/json"
    "log/slog"
    "net/http"
    "time"
)

type httpEmitter struct {
    wal     *WAL
    client  *http.Client
    target  string                  // e.g. http://control:8090/audit/events
    ch      chan struct{}           // wakes the drain goroutine on new write
}

func NewHTTP(wal *WAL, target string) *httpEmitter {
    e := &httpEmitter{
        wal:    wal,
        client: &http.Client{Timeout: 5 * time.Second},
        target: target,
        ch:     make(chan struct{}, 1),
    }
    go e.drain()
    return e
}

func (e *httpEmitter) Emit(ctx context.Context, ev Event) error {
    // Synchronous WAL write (durable). Then nudge the drain goroutine.
    if err := e.wal.Append(ev); err != nil {
        slog.Error("audit_wal_append_failed", "err", err.Error())
        return err  // VERY rare; signals disk-full or worse
    }
    select {
    case e.ch <- struct{}{}:
    default:               // drain is already busy; that's fine
    }
    return nil
}

func (e *httpEmitter) drain() {
    backoff := time.Second
    for {
        idx, body, ok, err := e.wal.ReadFromCursor()
        if err != nil { time.Sleep(backoff); backoff = backoffNext(backoff); continue }
        if !ok {
            <-e.ch                // block until a new write nudges us
            continue
        }
        resp, err := e.client.Post(e.target, "application/json", bytes.NewReader(body))
        if err != nil || resp.StatusCode >= 500 {
            slog.Warn("audit_post_retry", "idx", idx, "err", err)
            time.Sleep(backoff); backoff = backoffNext(backoff)
            continue
        }
        resp.Body.Close()
        if resp.StatusCode == 400 {
            // Schema rejection at the control plane — the event will NEVER succeed.
            // Skip it forward and log loudly. Per AUD-1, this protects ingest queue health.
            slog.Error("audit_post_400_dropping_event", "idx", idx)
        }
        e.wal.AdvanceCursor(idx)
        backoff = time.Second
    }
}

func backoffNext(d time.Duration) time.Duration {
    n := d * 2
    if n > time.Minute { n = time.Minute }
    return n
}
```

`Emit` is durable (WAL write) and synchronous (returns immediately after fsync — the WAL library handles batched fsyncs internally). The drain goroutine retries with exponential backoff up to 1 minute and keeps trying forever until the control plane comes back. **The mint path never blocks on the HTTP POST.**

Disk-full or WAL-corruption in `Emit` returns an error; the exchange handler's contract is that audit emission failures are logged but the response still returns 200 (per AUD-3 / TEC). The error is logged at ERROR level.

The 400-on-POST case is the only way an event gets "dropped" — and it's the control plane telling us the schema is wrong. We advance the cursor to keep the queue healthy and log loudly so the operator can diagnose. A normal connection error or 5xx does NOT advance the cursor; the same event is retried.

### `cmd/authz/main.go` — wiring

```go
walLog, err := audit.OpenWAL(cfg.AuditWalDir)
if err != nil {
    slog.Error("audit_wal_open_failed", "err", err)
    os.Exit(1)
}
emitter := audit.NewHTTP(walLog, cfg.ControlAuditURL)  // "http://control:8090/audit/events"
```

`BONAFIDE_AUTHZ_AUDIT_WAL_DIR` defaults to `/var/lib/bonafide/audit-wal`. `BONAFIDE_CONTROL_AUDIT_URL` defaults to `http://control:8090/audit/events`. Both env vars added in this slice.

The TEC env var `BONAFIDE_AUTHZ_AUDIT_PATH` is deleted.

---

## OPE denial-trace endpoint — swap

The OPE-introduced `audit_reader.find_event(event_id)` is rewritten:

```python
# services/control/app/policy/audit_reader.py — AUD rewrites this
async def find_event(event_id: str, db: asyncpg.Connection) -> dict | None:
    row = await db.fetchrow(
        "SELECT raw FROM control.audit_events WHERE event_id = $1",
        event_id,
    )
    return row["raw"] if row else None
```

The wire contract of `GET /policies/decisions/{event_id}` is unchanged from OPE; only the implementation reads from Postgres instead of tail-scanning a file.

---

## Smoke harness — AUD block

```bash
#--- AUD block -----------------------------------------------------------------
echo "[smoke:AUD] minting a chain, then reconstructing it via GET /audit/chain/..."

USER_JWT=$(docker compose run --rm demo-human python -m demo_human --email alice@example.com)
RESP=$(docker compose run --rm demo-agent python -m demo_agent --user-jwt "$USER_JWT" --print-task-token)
TASK_TOKEN=$(echo "$RESP" | jq -r '.task_token')
EVENT_ID=$(echo "$TASK_TOKEN" | jwt-cli decode --json | jq -r '.payload.jti')

# Give the WAL drain a moment to reach the control plane.
sleep 1

# Reconstruct WITHOUT presenting the token. Single source (audit_event only).
CHAIN=$(curl -fsSL "http://control.bonafide.local:8090/audit/chain/$EVENT_ID")
echo "$CHAIN" | jq -e '.subject == "spiffe://bonafide.local/human/alice@example.com"' > /dev/null
echo "$CHAIN" | jq -e '.actors == ["spiffe://bonafide.local/agent/planner"]' > /dev/null
echo "$CHAIN" | jq -e '.current_actor == "spiffe://bonafide.local/agent/planner"' > /dev/null
echo "$CHAIN" | jq -e '.reconstructed_from == ["audit_event"]' > /dev/null
echo "$CHAIN" | jq -e '.consistent == true' > /dev/null

# Reconstruct WITH the token — dual source, must remain consistent.
CHAIN2=$(curl -fsSL -H "Authorization: Bearer $TASK_TOKEN" \
    "http://control.bonafide.local:8090/audit/chain/$EVENT_ID")
echo "$CHAIN2" | jq -e '.reconstructed_from | sort == ["audit_event", "token_act_claim"]' > /dev/null
echo "$CHAIN2" | jq -e '.consistent == true' > /dev/null

# Outage test: stop control, run an exchange, restart control, check the event lands.
echo "[smoke:AUD] outage test: bring control plane down, mint, bring up, verify..."
docker compose stop control
RESP3=$(docker compose run --rm demo-agent python -m demo_agent --user-jwt "$USER_JWT" --print-task-token)
EVENT_ID3=$(echo "$RESP3" | jq -r '.task_token' | jwt-cli decode --json | jq -r '.payload.jti')
docker compose start control
sleep 3   # WAL drain backoff + control startup
curl -fsSL "http://control.bonafide.local:8090/audit/chain/$EVENT_ID3" \
    | jq -e ".event_id == \"$EVENT_ID3\"" > /dev/null

# Denied exchange: reconstruct it too.
echo "[smoke:AUD] denied event also reconstructs..."
docker compose run --rm -e BONAFIDE_SCOPE=calendar:write:alice@example.com \
    demo-agent python -m demo_agent --user-jwt "$USER_JWT" --print-error 2>&1 \
    | grep -oE 'event_id=[0-9a-zA-Z-]+' | tail -n 1 | cut -d= -f2 > /tmp/denied_id
DENIED_ID=$(cat /tmp/denied_id)
sleep 1
CHAIN4=$(curl -fsSL "http://control.bonafide.local:8090/audit/chain/$DENIED_ID")
echo "$CHAIN4" | jq -e '.reconstructed_from == ["audit_event"]' > /dev/null
echo "$CHAIN4" | jq -e '.consistent == true' > /dev/null

echo "[smoke:AUD] OK"
#--- end AUD block --------------------------------------------------------------
```

The outage test proves WAL durability across a control-plane restart while the authz process keeps minting.

---

## Open decisions resolved here

- **Schema location: `control` schema in the calendar Postgres.** Avoids spinning up a second Postgres. The calendar app's existing role (`vault_manager`-managed dynamic role) has no access to the `control` schema; control-plane writers connect with their own role.
- **Control-plane Postgres role.** `control_writer` — created at compose-up by an init script — has CRUD on `control.audit_events` and `control.delegation_edges` and nothing else. Connection string baked into the control container's environment.
- **WAL library: `tidwall/wal`.** Small, single-file, append-only, supports cursor persistence. Alternative — BoltDB or BadgerDB — adds machinery for a queue use case.
- **Buffer overflow policy: never drop on `Emit` (within reason).** WAL is bounded only by disk. A disk-full condition would fail `Append` and is logged at ERROR; the exchange still returns 200. The 256-event in-memory drop policy from TEC is gone — replaced by durable WAL.
- **At-least-once delivery: `ON CONFLICT (event_id) DO NOTHING`.** Idempotency lives in the database, not the transport. The WAL retries until the control plane accepts. Duplicates are dropped at the row level.
- **400 from control plane = drop and advance.** The control plane responds 400 only when an event's shape doesn't match CONTRACT.md §9. Such an event will never persist; advancing the cursor protects the queue from a permanently-stuck head. ERROR-level logged with the event_id so the operator can audit.
- **Cross-check authorization input: bearer token in `Authorization` header.** Avoids putting a JWT in a query string. The header is conventional, the validation reuses the resource SDK's JWKS code, and an invalid token is surfaced (not silently dropped).
- **`consistent: false` HTTP status: 200.** Per CONTRACT.md §10. The discrepancy is in the body, not the status line. Operators consuming the endpoint must check `consistent` — that is the security signal.
- **Edge writes: `executemany` inside the same transaction.** Single round-trip to Postgres for the edge batch; rolls back with the parent insert on any failure.
- **OPE's `audit_reader` swap is in scope for AUD.** The OPE-introduced contract of `GET /policies/decisions/{event_id}` is unchanged, but its implementation moves from file-tail-scan to a SQL query against `control.audit_events.raw`. The file-tail code is deleted.
- **TEC's file-backed audit emitter is fully removed.** No fallback path; no dual-write. The WAL is the only buffer.
- **Audit file removal triggers.** `BONAFIDE_AUTHZ_AUDIT_PATH` is removed from config. The audit log volume from TEC is dropped from compose. Operators upgrading from a TEC checkout see a one-line bootstrap warning ("audit log file deprecated, removing").

---

## Files created / modified / deleted

| File | Change |
|---|---|
| `services/control/alembic/env.py` | New |
| `services/control/alembic/versions/0001_audit.py` | New — both tables, indexes, FK |
| `services/control/app/audit/ingest.py` | New |
| `services/control/app/audit/chain.py` | New |
| `services/control/app/audit/store.py` | New |
| `services/control/app/policy/audit_reader.py` | Rewritten — SQL backed |
| `services/control/app/main.py` | Mounts the audit router; lifespan runs migrations |
| `services/authz/internal/audit/http.go` | New |
| `services/authz/internal/audit/wal.go` | New |
| `services/authz/internal/audit/file.go` | Deleted |
| `services/authz/cmd/authz/main.go` | Constructs `audit.NewHTTP(audit.OpenWAL(...))` |
| `services/authz/go.mod` / `go.sum` | Pulls in `github.com/tidwall/wal` |
| `deploy/postgres/control-bootstrap.sql` | New — creates `control_writer` role and schema |
| `docker-compose.yml` | Adds `audit-wal` volume on authz; gives control DB access; control container runs migrations on startup |
| `scripts/smoke.sh` | AUD block appended |

---

## Out of scope for this slice (see requirements.md for the slice-wide list)

- Depth-2 demonstration — SAN owns it. AUD's reconstruction works at depth-2 already (the chain endpoint is depth-generic), but no exchange in this slice produces a depth-2 chain.
- Audit retention / TTL.
- Access control on `POST /audit/events` and `GET /audit/chain/{event_id}`.
- A user-facing audit dashboard. JSON endpoint only.
- A third reconstruction source (SPIRE registration metadata). The `reconstructed_from` array contract allows it; this slice does not implement it.
- Active revocation triggered by `consistent: false`. The endpoint surfaces the signal; consuming it is the caller's responsibility.

# opa-policy-engine: Design

## Overview

This slice swaps `policy.Gate`'s in-memory Go map for an embedded OPA Rego evaluator. The `Gate` interface from TEC is unchanged; the new implementation loads `policies/delegation.rego` at startup, prepares a single Rego query, and runs that query against a `PolicyInput` document for every exchange. The Rego file owns the allow-list, the scope grammar's enforcement, and the `max_chain_depth` cap. The exchange handler reads back the three-field decision (`allowed`, `scope_grant`, `reason`) and proceeds exactly as TEC defined.

The control plane gains two read-only endpoints: `GET /policies/current` returns the bytes of the currently loaded Rego file, and `GET /policies/decisions/{event_id}` returns a denial trace reconstructed from the file-backed audit log (still in place — AUD swaps it for Postgres). The denial trace exposes only fields already present in the audit event per CONTRACT.md §9; the policy gate's `subject_claims` and `actor_claims` fields are private to the gate and are not surfaced.

Hot reload is implemented as **SIGHUP-driven**. An operator who needs to update policy edits `policies/delegation.rego` on disk and sends `SIGHUP` to the authz process. The reload is atomic — a request that has entered the handler completes against the policy in force at entry, and a parse failure on the new file leaves the old policy active with a non-blocking ERROR log.

Everything from TEC, SWI, and VSA is unchanged. The act-chain builder, SPIRE-issued SVIDs, Vault-issued dynamic credentials, the file-backed audit emitter, the resource SDK middleware — all carry forward without modification.

---

## Stack (additions only)

| Concern | Choice | Why |
|---|---|---|
| Rego runtime (Go library) | `github.com/open-policy-agent/opa/rego` | In-process; no sidecar; supported by OPA upstream |
| Rego AST package (for input validation) | `github.com/open-policy-agent/opa/ast` | Used at startup to parse the file once and reuse the compiled module |
| YAML-to-Rego data | none | The single example `delegation.rego` is hand-edited; no separate data files |

OPA is pulled into `services/authz`'s `go.mod`. The opa import alone adds a non-trivial dependency tree; we accept the cost.

---

## Repo additions and deletions

```
+ policies/
+   └── delegation.rego                # The example policy; commented as the slice deliverable.
+
+ services/authz/internal/policy/
+   ├── rego.go                        # NEW: opa-rego-backed Gate implementation
+   └── rego_test.go                   # Table-driven tests for the example policy
+
+ services/authz/internal/policy/reload/
+   └── reload.go                      # SIGHUP-triggered atomic swap; uses sync.RWMutex
+
- services/authz/internal/policy/map.go    # DELETED — the TEC in-memory Go map impl
- deploy/authz/policy.yaml                 # DELETED

  services/authz/internal/policy/policy.go  # Gate interface unchanged
  services/authz/cmd/authz/main.go          # Constructs policy.NewRegoGate(...) instead of policy.NewMapGate(...)
                                            # Registers SIGHUP signal handler that calls reload.Reload()

+ services/control/app/policy/
+   ├── routes.py                      # NEW: GET /policies/current and GET /policies/decisions/{event_id}
+   └── audit_reader.py                # NEW: small file reader over the file-backed audit NDJSON

  services/control/app/main.py         # Mounts the policy router
  scripts/smoke.sh                     # OPE block appended
```

The file-backed audit emitter from TEC is unchanged at the authz side. The control plane reads the audit file via a shared docker volume that already exists from TEC (it was mounted into authz; in OPE it's also mounted into control).

---

## Rego input contract (frozen by this slice — OPE-2)

The exchange handler constructs the following document for every evaluation:

```jsonc
{
  "subject":        "spiffe://bonafide.local/human/alice@example.com",
  "subject_claims": { "iss": "...", "sub": "...", "aud": "...", "exp": 1717180000, "iat": 1717179100, "email": "alice@example.com" },
  "actor":          "spiffe://bonafide.local/agent/planner",
  "actor_claims":   { "iss": "https://bonafide.local", "sub": "...", "aud": "https://authz.bonafide.local", "exp": 1717179400 },
  "scope":          "calendar:read:alice@example.com",
  "audience":       "http://calendar.bonafide.local:9000",
  "existing_chain": []
}
```

Field-by-field semantics (per OPE-2):

| Field | Source | Notes |
|---|---|---|
| `subject` | `subject_token.sub` after signature verification | CONTRACT.md §1, §4 |
| `subject_claims` | All claims of the subject_token after signature verification | Includes optional `email`; never populated from an unverified token |
| `actor` | `actor_token.sub` after `trust.IssuerTrust.Verify` | CONTRACT.md §1 |
| `actor_claims` | All claims of the actor_token after verification | |
| `scope` | The `scope` form parameter, verbatim | CONTRACT.md §2, §7 |
| `audience` | The `audience` form parameter, verbatim | CONTRACT.md §7 |
| `existing_chain` | `FlattenChain(subject_token.act)` from `services/authz/internal/exchange/act_chain.go` | Outermost-first; empty when subject has no `act` |

The handler is the **sole** populator of this input document. Anything not in the table above is never added.

---

## Rego output contract (OPE-3)

```rego
default decision := { "allowed": false, "scope_grant": "", "reason": "default_deny" }

decision := { "allowed": true,  "scope_grant": <scope>, "reason": "" }
decision := { "allowed": false, "scope_grant": "",       "reason": <short_string> }
```

The handler reads `data.bonafide.delegation.decision` and decodes it. Anything else in the package is ignored. An extra field on the document is dropped; a missing required field is a denial with `reason="malformed_decision"`.

---

## The example `policies/delegation.rego`

```rego
package bonafide.delegation

import rego.v1

# ------------------------------------------------------------------------------
# Configuration (operator-tunable; OPE-5 max_chain_depth lives here)
# ------------------------------------------------------------------------------

# Default delegation chain depth cap (cap is inclusive of the new hop).
# Operator override: edit this value and SIGHUP authz.
max_chain_depth := 4

# The agent-to-scope allow table. Each entry says:
#   "agent <actor> may act for any human matching <subject_prefix>,
#    requesting <scope> against <audience>."
registrations := [
    {
        "actor":          "spiffe://bonafide.local/agent/planner",
        "subject_prefix": "spiffe://bonafide.local/human/",
        "scope":          "calendar:read:alice@example.com",
        "audience":       "http://calendar.bonafide.local:9000",
    },
    {
        "actor":          "spiffe://bonafide.local/agent/tool",
        "subject_prefix": "spiffe://bonafide.local/human/",
        "scope":          "calendar:read:alice@example.com",
        "audience":       "http://calendar.bonafide.local:9000",
    },
]

# ------------------------------------------------------------------------------
# Decision
# ------------------------------------------------------------------------------

default decision := {
    "allowed":     false,
    "scope_grant": "",
    "reason":      "default_deny",
}

# Allow path: scope is well-formed AND a matching registration exists AND depth is within cap.
decision := {
    "allowed":     true,
    "scope_grant": input.scope,
    "reason":      "",
} if {
    scope_well_formed
    matched_registration
    chain_within_cap
}

# Denials with explicit reasons, evaluated in declaration order; first match wins.
decision := {
    "allowed":     false,
    "scope_grant": "",
    "reason":      "unknown_scope",
} if not scope_well_formed

decision := {
    "allowed":     false,
    "scope_grant": "",
    "reason":      "chain_too_deep",
} if {
    scope_well_formed
    not chain_within_cap
}

decision := {
    "allowed":     false,
    "scope_grant": "",
    "reason":      "agent_not_registered_for_scope",
} if {
    scope_well_formed
    chain_within_cap
    not matched_registration
}

# ------------------------------------------------------------------------------
# Predicates
# ------------------------------------------------------------------------------

scope_well_formed if regex.match(
    `^[a-z][a-z0-9-]*:(read|write|admin):[^\s:]+$`,
    input.scope,
)

# Cap is inclusive of the new hop: existing chain length + 1 must be ≤ cap.
chain_within_cap if count(input.existing_chain) + 1 <= max_chain_depth

matched_registration if some r in registrations
    r.actor == input.actor
    startswith(input.subject, r.subject_prefix)
    r.scope == input.scope
    r.audience == input.audience
```

The file is ~80 lines and is the single canonical policy artifact this slice ships. Authoring more policies, multi-file bundles, and bundle distribution are explicitly out of scope (per OPE's out-of-scope section).

---

## Go integration — `policy/rego.go`

```go
// services/authz/internal/policy/rego.go
package policy

import (
    "context"
    "fmt"
    "os"
    "sync"

    "github.com/open-policy-agent/opa/rego"
)

type regoGate struct {
    mu       sync.RWMutex
    query    rego.PreparedEvalQuery
    rawBytes []byte             // exposed via control plane's GET /policies/current
}

// NewRegoGate loads the policy from disk, compiles it into a prepared query,
// and returns a Gate impl. Returns a non-nil error if the file is unreadable
// or fails to parse — and the caller is required to exit non-zero. Fail-closed.
func NewRegoGate(ctx context.Context, path string) (*regoGate, error) {
    g := &regoGate{}
    if err := g.loadFromDisk(ctx, path); err != nil {
        return nil, err
    }
    return g, nil
}

func (g *regoGate) loadFromDisk(ctx context.Context, path string) error {
    raw, err := os.ReadFile(path)
    if err != nil {
        return fmt.Errorf("read policy %s: %w", path, err)
    }
    q, err := rego.New(
        rego.Query("data.bonafide.delegation.decision"),
        rego.Module(path, string(raw)),
    ).PrepareForEval(ctx)
    if err != nil {
        return fmt.Errorf("compile policy %s: %w", path, err)
    }
    g.mu.Lock()
    g.query = q
    g.rawBytes = raw
    g.mu.Unlock()
    return nil
}

func (g *regoGate) Decide(ctx context.Context, in PolicyInput) (Decision, error) {
    g.mu.RLock()
    q := g.query
    g.mu.RUnlock()
    rs, err := q.Eval(ctx, rego.EvalInput(map[string]any{
        "subject":        in.Subject,
        "subject_claims": in.SubjectClaims,
        "actor":          in.Actor,
        "actor_claims":   in.ActorClaims,
        "scope":          in.Scope,
        "audience":       in.Audience,
        "existing_chain": in.ExistingChain,
    }))
    if err != nil {
        // Engine error -> fail closed with a stable reason. (OPE-4 last criterion.)
        return Decision{Allowed: false, Reason: "policy_engine_error"}, nil
    }
    if len(rs) == 0 || len(rs[0].Expressions) == 0 {
        return Decision{Allowed: false, Reason: "default_deny"}, nil
    }
    return decodeDecision(rs[0].Expressions[0].Value)
}

func decodeDecision(v any) (Decision, error) {
    m, ok := v.(map[string]any)
    if !ok {
        return Decision{Allowed: false, Reason: "malformed_decision"}, nil
    }
    return Decision{
        Allowed:    boolOr(m["allowed"], false),
        ScopeGrant: stringOr(m["scope_grant"], ""),
        Reason:     stringOr(m["reason"], ""),
    }, nil
}

// CurrentBytes returns the raw bytes currently loaded. Used by the control plane.
func (g *regoGate) CurrentBytes() []byte {
    g.mu.RLock()
    defer g.mu.RUnlock()
    out := make([]byte, len(g.rawBytes))
    copy(out, g.rawBytes)
    return out
}
```

The `mu` mutex is a `sync.RWMutex` — `Decide` takes the read lock once per request (cheap); reload takes the write lock once per SIGHUP (rare). The compiled `rego.PreparedEvalQuery` is goroutine-safe per OPA documentation, so concurrent reads against the same query are sound.

### Reload — `policy/reload/reload.go`

```go
// services/authz/internal/policy/reload/reload.go
package reload

import (
    "context"
    "log/slog"
    "os"
    "os/signal"
    "syscall"

    "github.com/aslanchik/bonafide/services/authz/internal/policy"
)

// Watch installs a SIGHUP handler that triggers gate.loadFromDisk(path) and logs
// the outcome. On failure the old policy stays active.
func Watch(ctx context.Context, gate *policy.RegoGate, path string) {
    ch := make(chan os.Signal, 1)
    signal.Notify(ch, syscall.SIGHUP)
    go func() {
        for {
            select {
            case <-ctx.Done():
                return
            case <-ch:
                if err := gate.LoadFromDisk(ctx, path); err != nil {
                    slog.Error("policy_reload_failed", "path", path, "err", err.Error())
                    continue
                }
                slog.Info("policy_reloaded", "path", path)
            }
        }
    }()
}
```

A failed reload is non-blocking: the existing query stays in `gate.query` (the write lock is only taken on success). The handler logs at ERROR and waits for the next SIGHUP.

### `services/authz/cmd/authz/main.go` — wiring delta

```go
// Constructed at startup:
gate, err := policy.NewRegoGate(ctx, cfg.PolicyPath)   // /etc/authz/delegation.rego
if err != nil {
    slog.Error("startup_policy_load_failed", "err", err)
    os.Exit(1)                                          // fail closed (OPE-4)
}
reload.Watch(ctx, gate, cfg.PolicyPath)
```

`cfg.PolicyPath` defaults to `/etc/authz/delegation.rego`. The compose volume mount points it at `/path/to/repo/policies/delegation.rego` in the authz container.

---

## Control plane endpoints (OPE-6)

### `GET /policies/current`

```python
# services/control/app/policy/routes.py
@router.get("/policies/current", response_class=PlainTextResponse)
async def current_policy() -> str:
    # The control plane reads the same file authz reads (shared compose mount),
    # so this never goes out of sync with what's loaded. (It can be momentarily
    # ahead of authz between an edit and the SIGHUP; that is expected.)
    return _policy_path.read_text(encoding="utf-8")
```

### `GET /policies/decisions/{event_id}`

```python
@router.get("/policies/decisions/{event_id}", response_model=DenialTrace)
async def decision_trace(event_id: str) -> DenialTrace | Response:
    event = await audit_reader.find_event(event_id)
    if event is None:
        return Response(status_code=404)
    if event["outcome"] == "minted":
        return DenialTrace(
            event_id=event_id,
            outcome="minted",
            policy_reason=None,
            input_snapshot=None,
        )
    return DenialTrace(
        event_id=event_id,
        outcome="denied",
        policy_reason=event.get("policy_reason"),
        # Reconstruct only the fields already in the audit event (per OPE-6's
        # privacy criterion). subject_claims and actor_claims are NOT exposed.
        input_snapshot={
            "subject":        event["subject"],
            "actor":          event["actor"],
            "scope":          event["scope_requested"],
            "audience":       event["audience"],
            "existing_chain": event["existing_chain"],
        },
    )

class DenialTrace(BaseModel):
    event_id: str
    outcome: Literal["minted", "denied"]
    policy_reason: str | None
    input_snapshot: InputSnapshot | None

class InputSnapshot(BaseModel):
    subject:        str
    actor:          str
    scope:          str
    audience:       str
    existing_chain: list[str]
```

`audit_reader.find_event(event_id)` scans the NDJSON audit file (LIFO; the most recent denials are near the tail). At M4 scale (a handful of events) this is a `tac | head` equivalent in Python; AUD's Postgres swap makes this a SQL lookup later.

The endpoint rejects any method other than `GET` (FastAPI's default for a `@router.get`-decorated handler returns 405 on POST/PUT/DELETE).

---

## Smoke harness — OPE block

```bash
#--- OPE block -----------------------------------------------------------------
echo "[smoke:OPE] forbidden scope must be denied with the Rego reason..."

USER_JWT=$(docker compose run --rm demo-human python -m demo_human --email alice@example.com)

# Use a syntactically-valid scope that no agent is registered for.
OUT=$(docker compose run --rm \
        -e BONAFIDE_SCOPE="calendar:write:alice@example.com" \
        demo-agent python -m demo_agent --user-jwt "$USER_JWT" --print-error 2>&1 || true)

# Authz must have returned 400 access_denied with error_description = the Rego reason.
echo "$OUT" | grep -q "access_denied"
echo "$OUT" | grep -q "agent_not_registered_for_scope"

# Audit event for the denial must carry the same reason.
EVENT_ID=$(echo "$OUT" | grep -oE 'event_id=[0-9a-zA-Z-]+' | tail -n 1 | cut -d= -f2)
TRACE=$(curl -fsSL "http://control.bonafide.local:8090/policies/decisions/$EVENT_ID")
echo "$TRACE" | jq -e '.policy_reason == "agent_not_registered_for_scope"' > /dev/null
echo "$TRACE" | jq -e '.input_snapshot.scope == "calendar:write:alice@example.com"' > /dev/null
echo "$TRACE" | jq -e '.input_snapshot.existing_chain == []' > /dev/null
# subject_claims / actor_claims must not appear in the trace.
echo "$TRACE" | jq -e 'has("subject_claims") | not' > /dev/null
echo "$TRACE" | jq -e 'has("actor_claims")   | not' > /dev/null

echo "[smoke:OPE] depth-cap denial path..."
# We can't yet *produce* a depth-2 subject_token (SAN owns that), but we can prove
# the policy's depth check is wired by sending the in-memory input through a
# parallel test in the rego unit suite. (This is a unit assertion captured during
# build, not a runtime smoke step.)
docker compose exec -T authz go test ./internal/policy -run TestChainDepthDenial -v

echo "[smoke:OPE] verified scope is still allowed..."
docker compose run --rm demo-agent python -m demo_agent --user-jwt "$USER_JWT" \
    | jq -e '(.events | length) > 0' > /dev/null

echo "[smoke:OPE] OK"
#--- end OPE block --------------------------------------------------------------
```

The OPE block depends on TEC + SWI + VSA blocks all passing first.

---

## Open decisions resolved here

- **Hot-reload mechanism: SIGHUP.** Operator updates `policies/delegation.rego` on disk and signals the authz process. Failure to parse the new file leaves the old query active with an ERROR log; success replaces the query atomically. This is the single OPE-7 mechanism — no listing, no alternative. Restart-on-change was rejected because it interrupts in-flight requests; control-plane push was rejected because it adds a wire format not in CONTRACT.md and would block this slice on a CONTRACT.md change for negligible value.
- **OPA as a library (not a sidecar).** `opa/rego` embedded; no `opa run` daemon. The eval hot path is in-process and contributes ~1 ms per request at this policy size.
- **`rego.PreparedEvalQuery` is the cached object.** The policy is parsed and compiled once at load time. Each request reuses the prepared query under an RWMutex read lock.
- **Engine-error semantics: fail closed with `reason="policy_engine_error"`.** Distinguishes from `default_deny` (no allow rule matched) in the audit log. Per CLAUDE.md's fail-closed constraint, both lead to HTTP 400 access_denied; the distinction is observable only in audit.
- **Denial trace reads from the file audit log directly.** AUD replaces this with a Postgres query. The endpoint contract does not change between OPE and AUD.
- **`subject_claims` and `actor_claims` are NOT exposed by the denial trace.** Per OPE-6's last acceptance criterion. The audit event's CONTRACT.md §9 shape never carried these, so this is "do not invent new exposure paths" rather than "filter out existing exposure." The Rego input contract privately holds them at evaluation time only.
- **Scope grammar is enforced inside Rego.** The handler still pre-validates the form parameter at HTTP-parse time (rejecting truly malformed input with `error=invalid_request`), but the canonical grammar check is in `scope_well_formed` and yields `reason="unknown_scope"`. This avoids the trap of two divergent grammars in Go and Rego.
- **`max_chain_depth` location: inside Rego (`max_chain_depth := 4`).** A SIGHUP suffices to change it. No environment variable shadow.
- **`policies/` directory layout.** A single file (`delegation.rego`) for the MVP. Multi-file bundles, named environments, signed bundles, and bundle distribution are all post-MVP.

---

## Files created / modified / deleted

| File | Change |
|---|---|
| `policies/delegation.rego` | New |
| `services/authz/internal/policy/rego.go` | New |
| `services/authz/internal/policy/rego_test.go` | New — table-driven, includes `TestChainDepthDenial` |
| `services/authz/internal/policy/reload/reload.go` | New |
| `services/authz/internal/policy/map.go` | Deleted (TEC impl) |
| `services/authz/cmd/authz/main.go` | Wires `NewRegoGate` + `reload.Watch` |
| `services/authz/go.mod` / `go.sum` | Pulls in `github.com/open-policy-agent/opa` |
| `services/control/app/policy/routes.py` | New |
| `services/control/app/policy/audit_reader.py` | New — tail-scan over the NDJSON audit file |
| `services/control/app/main.py` | Mounts the policy router |
| `deploy/authz/policy.yaml` | Deleted |
| `docker-compose.yml` | The control container gains a read-only mount on the audit log volume |
| `scripts/smoke.sh` | OPE block appended |

---

## Out of scope for this slice (see requirements.md for the slice-wide list)

- Postgres-backed audit (AUD owns it).
- Multi-file bundles, bundle signatures, bundle distribution.
- A policy authoring UI on the control plane (read-only endpoints only).
- A rich `subject_claims`/`actor_claims` shape — these are passed to Rego as opaque maps and the example policy never inspects them; future policy authors can.
- Rate limiting on the denial-trace endpoint.
- Inter-process locking on the policy file. A bad edit between two SIGHUPs is the operator's responsibility; the reload step does its own parse check.

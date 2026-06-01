# subagent-nesting: Design

## Overview

This slice demonstrates depth-2 of the signed delegation chain end-to-end. The plumbing is mostly already in place: TEC's `BuildAct` function handles arbitrary nesting depth, AUD's chain endpoint reconstructs from a depth-generic `resulting_chain`, OPE's `max_chain_depth` already governs the cap, and the resource SDK's `ActorChain` type already carries `prior_actors`. What SAN adds is (a) a second agent workload with its own SPIFFE identity, (b) a script flow that produces a depth-2 exchange, and (c) tests at depth 2 against real SPIFFE IDs.

The single demo agent introduced in TEC (`apps/demo-agent`, SPIFFE ID `spiffe://bonafide.local/agent/planner`) is split into two:

1. **`apps/demo-planner`** — receives the user JWT as `subject_token`, exchanges for a planner-scoped task token. Its SPIFFE ID is `spiffe://bonafide.local/agent/planner`. The TEC `apps/demo-agent` was already wired to act as the planner; this slice renames it.
2. **`apps/demo-tool`** — receives the planner's task token as `subject_token`, presents its own JWT-SVID as `actor_token`, exchanges for a tool-scoped task token, then calls the calendar with the depth-2 token. Its SPIFFE ID is `spiffe://bonafide.local/agent/tool`.

A new SPIRE workload registration entry is created for `tool`. The OPE allow-list (`policies/delegation.rego`) gains one more entry registering the `tool` agent for the same `calendar:read:alice@example.com` scope. Everything else — the act-chain builder, the policy gate, the JWKS, the Vault SPIFFE auth + DB engine, the SPIRE topology, the audit pipeline, the resource SDK — is unchanged. SAN extends `act_chain_test.go` with depth-2 cases using real SPIFFE IDs (TEC's file already had placeholder-ID depth-2 cases; SAN replaces those with the canonical ones from CONTRACT.md §6.1) and extends the smoke harness with the depth-2 block.

---

## Stack (no additions)

No new dependencies in any language. The slice is pure composition.

---

## Repo additions and modifications

```
+ apps/demo-tool/                              # NEW: the tool agent
+   ├── pyproject.toml
+   └── demo_tool/__main__.py

  apps/demo-agent/  → apps/demo-planner/       # RENAMED: same code, new role label
+ apps/demo-planner/demo_planner/__main__.py   # MODIFIED: now also prints the task token for chaining

  policies/delegation.rego                     # +1 registration for the tool agent
  deploy/spire/registrations.sh                # +1 entry for spiffe://bonafide.local/agent/tool

  apps/demo-calendar/demo_calendar/main.py     # Populates evidence_chain from prior_actors (was empty in earlier slices)

  services/authz/internal/exchange/act_chain_test.go
                                               # Depth-2 cases switched to canonical SPIFFE IDs
  sdks/resource-py/tests/test_middleware.py    # New depth-2 cases

  scripts/smoke.sh                             # SAN block appended
```

The rename `demo-agent → demo-planner` is bookkeeping: `BonafideAgent` does not care what app name calls it.

---

## The two-agent flow

```
user JWT
   │
   ▼
demo-planner (spiffe://bonafide.local/agent/planner)
   │
   │  1. fetches its JWT-SVID via workload API
   │  2. exchanges:
   │       subject_token = user JWT (no act)
   │       actor_token   = planner's JWT-SVID
   │     →  task_token_1: sub = alice, act = { sub: planner }
   │
   │  3. prints task_token_1 to stdout
   ▼
demo-tool   (spiffe://bonafide.local/agent/tool)
   │
   │  4. fetches its JWT-SVID via workload API
   │  5. exchanges:
   │       subject_token = task_token_1   (CARRIES an act claim)
   │       actor_token   = tool's JWT-SVID
   │     →  task_token_2: sub = alice, act = { sub: tool, act: { sub: planner } }
   │                                                   ▲ nested per CONTRACT.md §6.1
   │
   │  6. fetches a Vault lease (its own role; same calendar_reader role)
   │  7. calls calendar with task_token_2
   ▼
demo-calendar
   │
   │  resource SDK validates task_token_2:
   │    - signature → JWKS ✓
   │    - iss / aud / exp ✓
   │    - sub starts with spiffe://bonafide.local/human/ ✓ (impersonation guard)
   │    - act chain is well-formed (no malformed inner act) ✓
   │
   │  ActorChain:
   │    subject       = "spiffe://bonafide.local/human/alice@example.com"
   │    current_actor = "spiffe://bonafide.local/agent/tool"
   │    prior_actors  = ("spiffe://bonafide.local/agent/planner",)
   │
   │  authorizes against (subject, current_actor, scope) only — CONTRACT.md §6.2
   │  → 200 with evidence_chain populated
```

The planner does NOT call Vault and does NOT call the calendar — its job ends with handing the task token to the tool agent. The tool is what reaches the resource.

---

## demo-planner (renamed from demo-agent)

```python
# apps/demo-planner/demo_planner/__main__.py
import json, sys, typer
from bonafide_agent import BonafideAgent

app = typer.Typer()

@app.command()
def main(user_jwt: str, *,
         spiffe_id: str = "spiffe://bonafide.local/agent/planner",
         scope: str    = "calendar:read:alice@example.com",
         audience: str = "http://calendar.bonafide.local:9000"):
    """Exchange the user JWT for a planner-scoped task token. Print the task token to stdout."""
    agent = BonafideAgent(
        authz_token_url="http://authz.bonafide.local:8080/token",
        spiffe_id=spiffe_id,
        spiffe_socket="/run/spire/sockets/agent.sock",
        vault_addr="http://vault:8200",
        vault_auth_mode=os.getenv("BONAFIDE_VAULT_AUTH_MODE", "spiffe"),
    )
    task_token = agent.exchange(subject_token=user_jwt, scope=scope, audience=audience)
    # Print as JSON so demo-tool can parse it deterministically.
    print(json.dumps({"task_token": task_token.access_token,
                      "expires_at": task_token.expires_at,
                      "scope":      task_token.scope}))

if __name__ == "__main__":
    app()
```

`BONAFIDE_VAULT_AUTH_MODE` is read from env so the planner inherits VSA's mode choice without re-detecting.

---

## demo-tool (new)

```python
# apps/demo-tool/demo_tool/__main__.py
import json, sys, typer
from bonafide_agent import BonafideAgent

app = typer.Typer()

@app.command()
def main(planner_task_token: str, *,
         spiffe_id: str = "spiffe://bonafide.local/agent/tool",
         scope: str    = "calendar:read:alice@example.com",
         audience: str = "http://calendar.bonafide.local:9000"):
    """Exchange the planner's task token for a tool-scoped task token (depth-2 act).
    Then fetch a Vault lease and call the calendar."""
    agent = BonafideAgent(
        authz_token_url="http://authz.bonafide.local:8080/token",
        spiffe_id=spiffe_id,
        spiffe_socket="/run/spire/sockets/agent.sock",
        vault_addr="http://vault:8200",
        vault_auth_mode=os.getenv("BONAFIDE_VAULT_AUTH_MODE", "spiffe"),
    )

    # The planner's task token is the subject_token here.
    # The act-chain builder on the authz side nests it under tool's act.
    task_token = agent.exchange(subject_token=planner_task_token, scope=scope, audience=audience)

    lease = agent.fetch_lease()
    resp  = agent.call(url=f"{audience}/events", token=task_token, lease=lease)
    print(resp.text)

if __name__ == "__main__":
    app()
```

The SDK call signature is unchanged from VSA. The novelty is entirely on the wire: this `exchange()` call presents a subject_token that already has an `act` claim, and the authz server's `BuildAct` nests it.

---

## demo-calendar — evidence_chain population

Calendar's response shape from VSA already had `evidence_chain` (always empty at depth 1). SAN extends it to actually populate from `ActorChain.prior_actors`:

```python
# apps/demo-calendar/demo_calendar/main.py — diff vs. VSA
@app.get("/events")
async def get_events(chain: ActorChain = Depends(validator),
                     x_bonafide_connection: str = Header(...)):
    ...
    return {
        "acting_for":      chain.subject,
        "acted_by":        chain.current_actor,
        "evidence_chain":  list(chain.prior_actors),   # ← was always [] before SAN
        "events":          [dict(r) for r in rows],
    }
```

No new typed model needed; `ActorChain.prior_actors` is already a `tuple[str, ...]`.

---

## OPE policy — one added registration

```rego
# policies/delegation.rego — diff in `registrations`
registrations := [
    {
        "actor":          "spiffe://bonafide.local/agent/planner",
        "subject_prefix": "spiffe://bonafide.local/human/",
        "scope":          "calendar:read:alice@example.com",
        "audience":       "http://calendar.bonafide.local:9000",
    },
+   {
+       "actor":          "spiffe://bonafide.local/agent/tool",
+       "subject_prefix": "spiffe://bonafide.local/human/",
+       "scope":          "calendar:read:alice@example.com",
+       "audience":       "http://calendar.bonafide.local:9000",
+   },
]
```

The Rego allow-list now permits both actors against the same `(subject_prefix, scope, audience)`. The `existing_chain` field is *not* used to constrain which actor an outer hop can come from in this slice — that would be richer policy modeling and is out of scope. Per CONTRACT.md §6.2 the policy authorizes by `(subject, actor, scope, audience)` only; `existing_chain` is consulted by Rego solely for the depth cap.

The reload after editing this file is the standard `docker compose kill -s HUP authz`. Idempotent and atomic per OPE-7.

---

## SPIRE registration — one added entry

```bash
# deploy/spire/registrations.sh — diff
ensure_entry spiffe://bonafide.local/agent/planner    bonafide/demo-planner:latest
+ ensure_entry spiffe://bonafide.local/agent/tool       bonafide/demo-tool:latest
ensure_entry spiffe://bonafide.local/service/authz    bonafide/authz:latest
ensure_entry spiffe://bonafide.local/service/calendar bonafide/demo-calendar:latest
```

The `demo-tool` container's Dockerfile labels its image with `bonafide.workload=tool`. SPIRE's docker workload attestor binds the SVID to image-id + label, so the SVID issued in the `demo-tool` container is `spiffe://bonafide.local/agent/tool`. In the `demo-planner` container it's still `agent/planner`.

---

## `act_chain_test.go` — depth-2 with canonical IDs

TEC already shipped depth-2 and depth-3 test cases against placeholder SPIFFE IDs. SAN replaces those placeholders with the canonical IDs from CONTRACT.md §6.1's worked example, and adds a depth-2 test that uses a real subject_token from a depth-1 exchange:

```go
// services/authz/internal/exchange/act_chain_test.go — additions / replacements

func TestBuildAct_Depth2_CanonicalSpiffeIDs(t *testing.T) {
    // Mirror of CONTRACT.md §6.1's depth-2 worked example.
    subjectAct := &Act{Sub: "spiffe://bonafide.local/agent/planner"}
    got := BuildAct("spiffe://bonafide.local/agent/tool", subjectAct)
    want := &Act{
        Sub: "spiffe://bonafide.local/agent/tool",
        Act: &Act{Sub: "spiffe://bonafide.local/agent/planner"},
    }
    require.Equal(t, want, got)

    // Byte-for-byte: the inner subtree equals the input subtree.
    require.Equal(t, subjectAct, got.Act, "nested act must equal the subject_token's act subtree")
}

func TestBuildAct_NeverMutatesSubjectActsCopyIsDefensive(t *testing.T) {
    subjectAct := &Act{Sub: "spiffe://bonafide.local/agent/planner"}
    got := BuildAct("spiffe://bonafide.local/agent/tool", subjectAct)
    // Mutate the input after BuildAct returns.
    subjectAct.Sub = "spiffe://bonafide.local/agent/MUTATED"
    // The output's nested act must NOT see the mutation.
    require.Equal(t, "spiffe://bonafide.local/agent/planner", got.Act.Sub)
}

func TestFlattenChain_Depth2(t *testing.T) {
    act := &Act{
        Sub: "spiffe://bonafide.local/agent/tool",
        Act: &Act{Sub: "spiffe://bonafide.local/agent/planner"},
    }
    require.Equal(t,
        []string{
            "spiffe://bonafide.local/agent/tool",
            "spiffe://bonafide.local/agent/planner",
        },
        FlattenChain(act))
}
```

The TEC tests against placeholders (e.g. `a`, `b`, `c`) are retained for fast unit feedback; the SAN tests are the canonical-ID counterparts.

---

## Resource SDK tests — depth-2

```python
# sdks/resource-py/tests/test_middleware.py — additions

@pytest.mark.asyncio
async def test_depth2_chain_is_extracted_correctly(jwks_pubkey, signing_key):
    token = _sign_task_token(signing_key, claims={
        "sub": "spiffe://bonafide.local/human/alice@example.com",
        "act": {
            "sub": "spiffe://bonafide.local/agent/tool",
            "act": {"sub": "spiffe://bonafide.local/agent/planner"},
        },
        "iss": "http://authz.bonafide.local:8080",
        "aud": "http://calendar.bonafide.local:9000",
        "exp": _now() + 60, "iat": _now(), "jti": "01HX...",
    })
    chain = await _validator_for(jwks_pubkey)(req=_req_with_bearer(token))
    assert chain.subject       == "spiffe://bonafide.local/human/alice@example.com"
    assert chain.current_actor == "spiffe://bonafide.local/agent/tool"
    assert chain.prior_actors  == ("spiffe://bonafide.local/agent/planner",)

@pytest.mark.asyncio
async def test_depth2_inner_actor_does_NOT_affect_authorization(...):
    """Per CONTRACT.md §6.2 — flipping inner act.act.sub must not change the
    authorization decision for the same (sub, act.sub, scope) tuple."""
    chain_a = _build_chain(inner="planner")
    chain_b = _build_chain(inner="other-planner")
    # Calendar's authorization rule is a function of (subject, current_actor, scope) only.
    # Both chains have the same subject and current_actor; inner differs.
    # Therefore the calendar's decision must be identical.
    assert _calendar_decision(chain_a, scope="calendar:read:alice@example.com") == \
           _calendar_decision(chain_b, scope="calendar:read:alice@example.com")

@pytest.mark.asyncio
async def test_malformed_inner_act_triggers_impersonation_guard(jwks_pubkey, signing_key):
    token = _sign_task_token(signing_key, claims={
        "sub": "spiffe://bonafide.local/human/alice@example.com",
        "act": {
            "sub": "spiffe://bonafide.local/agent/tool",
            "act": {"not_sub": "malformed"},  # CONTRACT.md §6.3 — malformed inner act
        },
        # ...other claims as above
    })
    with pytest.raises(HTTPException) as ex:
        await _validator_for(jwks_pubkey)(req=_req_with_bearer(token))
    assert ex.value.status_code == 401
    assert "impersonation guard" in ex.value.detail
```

The middleware's `_extract_chain` already enforces these; the tests prove the property at depth 2 explicitly. The "impersonation guard fires on malformed inner act" test is what makes SAN-4's acceptance criterion observable.

---

## Smoke harness — SAN block

```bash
#--- SAN block -----------------------------------------------------------------
echo "[smoke:SAN] running depth-2 chain end-to-end..."

USER_JWT=$(docker compose run --rm demo-human python -m demo_human --email alice@example.com)

# Step 1: planner runs first, prints task_token_1.
PLANNER_OUT=$(docker compose run --rm demo-planner \
    python -m demo_planner --user-jwt "$USER_JWT")
TASK_TOKEN_1=$(echo "$PLANNER_OUT" | jq -r '.task_token')
EVENT_ID_1=$(echo "$TASK_TOKEN_1" | jwt-cli decode --json | jq -r '.payload.jti')

# Step 2: tool exchanges task_token_1 → task_token_2 (depth-2), calls calendar.
TOOL_OUT=$(docker compose run --rm demo-tool \
    python -m demo_tool --planner-task-token "$TASK_TOKEN_1")

# Calendar response must show all three identities and evidence_chain populated.
echo "$TOOL_OUT" | jq -e '.acting_for == "spiffe://bonafide.local/human/alice@example.com"' > /dev/null
echo "$TOOL_OUT" | jq -e '.acted_by   == "spiffe://bonafide.local/agent/tool"' > /dev/null
echo "$TOOL_OUT" | jq -e '.evidence_chain == ["spiffe://bonafide.local/agent/planner"]' > /dev/null
echo "$TOOL_OUT" | jq -e '(.events | length) > 0' > /dev/null

# Decode task_token_2 and verify the act-chain shape.
TASK_TOKEN_2=$(docker compose run --rm demo-tool \
    python -m demo_tool --planner-task-token "$TASK_TOKEN_1" --print-task-token-and-exit)
ACT=$(echo "$TASK_TOKEN_2" | jwt-cli decode --json | jq -c '.payload.act')
test "$ACT" = '{"sub":"spiffe://bonafide.local/agent/tool","act":{"sub":"spiffe://bonafide.local/agent/planner"}}'

# Sub MUST equal alice in BOTH minted tokens.
SUB_1=$(echo "$TASK_TOKEN_1" | jwt-cli decode --json | jq -r '.payload.sub')
SUB_2=$(echo "$TASK_TOKEN_2" | jwt-cli decode --json | jq -r '.payload.sub')
test "$SUB_1" = "spiffe://bonafide.local/human/alice@example.com"
test "$SUB_2" = "spiffe://bonafide.local/human/alice@example.com"

# Audit reconstruction at depth 2.
EVENT_ID_2=$(echo "$TASK_TOKEN_2" | jwt-cli decode --json | jq -r '.payload.jti')
sleep 1   # WAL drain

CHAIN=$(curl -fsSL -H "Authorization: Bearer $TASK_TOKEN_2" \
    "http://control.bonafide.local:8090/audit/chain/$EVENT_ID_2")
echo "$CHAIN" | jq -e '.actors == ["spiffe://bonafide.local/agent/tool", "spiffe://bonafide.local/agent/planner"]' > /dev/null
echo "$CHAIN" | jq -e '.current_actor == "spiffe://bonafide.local/agent/tool"' > /dev/null
echo "$CHAIN" | jq -e '.reconstructed_from | sort == ["audit_event", "token_act_claim"]' > /dev/null
echo "$CHAIN" | jq -e '.consistent == true' > /dev/null

# Audit event for the depth-2 hop must carry the right existing_chain / resulting_chain.
EVT=$(curl -fsSL "http://control.bonafide.local:8090/audit/chain/$EVENT_ID_2")
# (re-fetch without bearer to verify the audit-only view is the same)
echo "$EVT" | jq -e '.actors == ["spiffe://bonafide.local/agent/tool", "spiffe://bonafide.local/agent/planner"]' > /dev/null

# Depth-1 event ALSO reconstructs (same endpoint, different event_id).
CHAIN_1=$(curl -fsSL "http://control.bonafide.local:8090/audit/chain/$EVENT_ID_1")
echo "$CHAIN_1" | jq -e '.actors == ["spiffe://bonafide.local/agent/planner"]' > /dev/null

echo "[smoke:SAN] OK — MVP smoke harness complete."
#--- end SAN block --------------------------------------------------------------
```

The SAN block requires all five prior blocks (TEC, SWI, VSA, OPE, AUD) to still pass; the cumulative harness is a strict prefix.

---

## Open decisions resolved here

- **demo-tool runs as a separate compose service (one-shot).** Like demo-planner, it's run via `docker compose run --rm`. Sharing a container would defeat the SPIRE workload attestation — the two need different image IDs so SPIRE issues different SVIDs.
- **The depth-2 demo is driven by the smoke harness, not by either agent calling the other.** demo-planner prints the task token to stdout; the smoke harness pipes it to demo-tool's stdin/argv. In a production setting agents would call each other directly, but for the demo the script-driven flow makes the wire shape visible at each hop.
- **The Rego allow-list doesn't constrain depth-2 by chain shape.** Both `planner` and `tool` are independently authorized for the same scope; the policy does NOT require that `tool` only act when `planner` is in the existing chain. Authoring that richer constraint is post-MVP; it would not change anything about CONTRACT.md and would just be a richer Rego rule.
- **`max_chain_depth = 4` stays the default.** Depth-2 passes; depth-3 would also pass (legal under the cap, not exercised); depth-5 would be denied. SAN does not lower the cap.
- **`evidence_chain` was always a list, even at depth 1 (empty list).** SAN doesn't change the shape; only the population. Forward-compatible: any consumer that was already reading `evidence_chain` keeps working.
- **`act_chain_test.go` replaces, does not delete, the TEC placeholder cases.** Per CLAUDE.md the file is never deleted, only extended. SAN extends it with canonical-ID variants alongside the existing placeholder-ID ones — both sets continue to run.
- **No new env vars.** The Vault auth mode is read from `BONAFIDE_VAULT_AUTH_MODE` already exported by VSA's bootstrap. Both agents inherit it.
- **The MVP is complete after this slice.** No follow-on slice is planned within the MVP scope. Post-MVP candidates (depth-3+ demo, chain-shape policy, federation, active revocation) are listed in CLAUDE.md's "Out of scope for the MVP" and remain there.

---

## Files created / modified / deleted

| File | Change |
|---|---|
| `apps/demo-tool/pyproject.toml` | New |
| `apps/demo-tool/demo_tool/__main__.py` | New |
| `apps/demo-planner/...` | Renamed from `apps/demo-agent/` (no logic change beyond the rename and printing the task token) |
| `apps/demo-calendar/demo_calendar/main.py` | One-line: populate `evidence_chain` from `chain.prior_actors` |
| `policies/delegation.rego` | +1 entry for `agent/tool` |
| `deploy/spire/registrations.sh` | +1 entry for `spiffe://bonafide.local/agent/tool` |
| `services/authz/internal/exchange/act_chain_test.go` | Depth-2 tests added with canonical SPIFFE IDs; defensive-copy test added |
| `sdks/resource-py/tests/test_middleware.py` | Depth-2 extraction + impersonation-guard-at-depth-2 tests added |
| `docker-compose.yml` | `demo-tool` service added (build context, label, sockets mount) |
| `scripts/smoke.sh` | SAN block appended |

---

## Out of scope for this slice (see requirements.md for the slice-wide list)

- Depth-3+ demonstration. The cap allows it; neither the demo agents nor the smoke harness produce it.
- Rich chain-shape policy (e.g. "tool may only act under planner").
- Cross-trust-domain delegation. Single trust domain only.
- Active revocation of an inner actor mid-chain.
- A tool agent that itself calls a sub-tool. The chain framework supports it (any actor can become subject_token for another); no demo exercises it.
- Multi-tenancy, federation, mTLS at the resource. Same exclusions as every prior slice.

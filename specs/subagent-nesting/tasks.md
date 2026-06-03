# subagent-nesting: Tasks

Tasks are ordered. Each must be complete and verifiable before the next begins. If a task surfaces a spec problem, stop — update the relevant spec, re-review, then continue. One commit per task.

Status: `[ ]` todo · `[x]` done · `[~]` in progress

---

## T-01: [ ] Rename `apps/demo-agent` to `apps/demo-planner`

**Satisfies:** SAN-1, SAN-2 (scaffolding)

- Move the directory `apps/demo-agent/` to `apps/demo-planner/` via `git mv` so history is preserved.
- Rename the Python package `demo_agent` to `demo_planner` (`git mv apps/demo-planner/demo_agent apps/demo-planner/demo_planner`).
- Update `apps/demo-planner/pyproject.toml`: rename project to `demo-planner`, update package include path, leave the dependency on `bonafide-agent` (local path) unchanged.
- No logic change to `__main__.py` in this task (T-02 modifies it).
- Search the repo (`grep -rn 'demo_agent\|demo-agent\|apps/demo-agent'`) and update every reference — `docker-compose.yml` service name and build context, `scripts/bootstrap.sh`, `scripts/smoke.sh` (the TEC block invocation), and any docs.
- The SPIRE registration in `deploy/spire/registrations.sh` continues to issue `spiffe://bonafide.local/agent/planner` — only the workload selector's image label updates (T-04 owns the SPIRE diff).

**Verified when:** `git ls-files apps/demo-planner/` lists the moved files; `git log --follow apps/demo-planner/demo_planner/__main__.py` shows commits originally made under `apps/demo-agent/`; `grep -rn 'demo_agent\|demo-agent' --exclude-dir=.git` returns no matches; `docker compose config` parses without error and lists a `demo-planner` service with no `demo-agent` reference.

---

## T-02: [ ] Modify `demo-planner` to print the task token as JSON

**Satisfies:** SAN-2

- Edit `apps/demo-planner/demo_planner/__main__.py` per `design.md` "demo-planner":
  - Keep the existing `BonafideAgent` construction (SPIFFE socket, Vault auth mode from `BONAFIDE_VAULT_AUTH_MODE` env).
  - Replace the existing pipeline (which previously called `fetch_lease()` and `call()`) with: exchange the user JWT, then print exactly the JSON object `{"task_token": <access_token>, "expires_at": <epoch_seconds>, "scope": <scope>}` to stdout on one line, no trailing whitespace beyond `\n`.
  - The planner no longer calls Vault and no longer calls the calendar — its job ends at printing the task token (per `design.md` "The two-agent flow").
  - SPIFFE ID default remains `spiffe://bonafide.local/agent/planner`; scope default `calendar:read:alice@example.com`; audience default `http://calendar.bonafide.local:9000`.
- Any exception aborts and exits non-zero with the error class and message; no retries (matches the TEC `demo-agent` behaviour).

**Verified when:** `docker compose run --rm demo-planner python -m demo_planner --user-jwt $(...)` against a brought-up topology produces a single line of JSON on stdout. `echo "$OUT" | jq -e 'has("task_token") and has("expires_at") and has("scope")'` exits zero. A decode of the printed `task_token` shows `sub == "spiffe://bonafide.local/human/alice@example.com"` and `act.sub == "spiffe://bonafide.local/agent/planner"` with no `act.act` (the depth-1 case per CONTRACT.md §6.1).

---

## T-03: [ ] Create `apps/demo-tool` package skeleton

**Satisfies:** SAN-1, SAN-3 (scaffolding)

- Create `apps/demo-tool/pyproject.toml` declaring dependencies on `bonafide-agent` (local path), `typer`, `pydantic-settings` per `design.md` "demo-tool (new)".
- Create the package directory `apps/demo-tool/demo_tool/` with an empty `__init__.py` and a placeholder `__main__.py` whose only contents are `if __name__ == "__main__": pass` so `python -m demo_tool` exits zero (T-05 fills in the real CLI).
- Match the project metadata style of `apps/demo-planner/pyproject.toml` (name `demo-tool`, package include path).

**Verified when:** `uv pip install -e apps/demo-tool/` exits zero, `python -m demo_tool` exits zero, and `git ls-files apps/demo-tool/` includes `pyproject.toml`, `demo_tool/__init__.py`, `demo_tool/__main__.py`.

---

## T-04: [ ] Add the SPIRE registration entry for `spiffe://bonafide.local/agent/tool`

**Satisfies:** SAN-1, and the safety constraint **"No static long-lived secrets ... outside SPIRE and Vault"** (the tool's identity is SPIRE-issued, not file-bound)

- Edit `deploy/spire/registrations.sh` per `design.md` "SPIRE registration — one added entry": append `ensure_entry spiffe://bonafide.local/agent/tool bonafide/demo-tool:latest` between the existing planner and authz lines.
- The selector for the new entry must pin both the container image (`bonafide/demo-tool:latest`) and the Unix UID under which the workload runs (matching the pattern used by the existing planner entry per `design.md` "SPIRE registration").
- Author a Dockerfile at `apps/demo-tool/Dockerfile` so the image labels itself `bonafide.workload=tool` (per `design.md` "SPIRE registration": SPIRE's docker workload attestor binds the SVID to image-id + label). The Dockerfile follows the same minimal pattern as `apps/demo-planner/Dockerfile`.
- A workload that does not match the selectors of the planner or tool entries receives no SVID; do not add a permissive fallback entry. Per SAN-1 acceptance criterion: "A workload that does not match the selectors of either registration entry receives no SVID and any code path requiring a JWT-SVID for that workload fails closed."

**Verified when:** After running `deploy/spire/registrations.sh` against the running SPIRE Server, `spire-server entry show` lists exactly one entry whose `SPIFFE ID` equals `spiffe://bonafide.local/agent/tool` and whose `Selector` set includes both the image-id selector and the UID selector matching the `demo-tool` container. A `docker compose run --rm demo-tool sh -c 'wait-and-fetch-svid'` (using the standard Workload API socket mount) returns a JWT-SVID whose `sub` equals `spiffe://bonafide.local/agent/tool`. A workload run with an image label other than `bonafide.workload=tool` receives no SVID and exits non-zero (fail closed).

---

## T-05: [ ] Implement `apps/demo-tool/demo_tool/__main__.py` (depth-2 exchange + calendar call)

**Satisfies:** SAN-3, SAN-5, and the safety constraint **"All credentials short-lived"** (the tool inherits the SDK's strict TTL enforcement; never reuses past `exp`)

- Replace the placeholder `__main__.py` from T-03 with the implementation in `design.md` "demo-tool (new)":
  - `typer` CLI accepting `planner_task_token` (positional or `--planner-task-token`) and optional `--print-task-token-and-exit` (for the smoke harness to capture the depth-2 token without calling the calendar).
  - Construct `BonafideAgent` using SPIFFE socket and `BONAFIDE_VAULT_AUTH_MODE` env (inherited from VSA's bootstrap; no new env vars per `design.md` "Open decisions resolved here").
  - SPIFFE ID default `spiffe://bonafide.local/agent/tool`; scope default `calendar:read:alice@example.com`; audience default `http://calendar.bonafide.local:9000`.
  - Pipeline:
    1. `task_token = agent.exchange(subject_token=planner_task_token, scope=scope, audience=audience)` — the planner's task token is the `subject_token`; the SDK's exchange flow is unchanged (the novelty is on the wire, per `design.md` "demo-tool").
    2. If `--print-task-token-and-exit`, print the `access_token` to stdout and exit zero.
    3. `lease = agent.fetch_lease()` (Vault DB lease; same `calendar_reader` role the planner used in earlier slices).
    4. `resp = agent.call(url=f"{audience}/events", token=task_token, lease=lease)` — calls the calendar with the depth-2 task token.
    5. Print `resp.text` to stdout.
  - Any exception aborts and exits non-zero; no retries, no fallback (fail closed).
- The tool agent never reads any credential from disk, env, or container image layers per SAN-1 acceptance criterion: actor credential is the JWT-SVID fetched via the Workload API socket.

**Verified when:** Against a brought-up topology with `demo-planner` having produced a task token in `$TASK_TOKEN_1`, `docker compose run --rm demo-tool python -m demo_tool "$TASK_TOKEN_1"` exits zero and prints a JSON body. With `--print-task-token-and-exit`, the printed access_token decodes to a JWT whose `sub == "spiffe://bonafide.local/human/alice@example.com"`, `act.sub == "spiffe://bonafide.local/agent/tool"`, and `act.act == {"sub": "spiffe://bonafide.local/agent/planner"}` per CONTRACT.md §6.1 nested form. `grep -rn 'open\|os.environ\[' apps/demo-tool/demo_tool/` produces no read of any credential file (only the SPIFFE socket path, which is a Unix socket, not a credential).

---

## T-06: [ ] Add `demo-tool` service to `docker-compose.yml`

**Satisfies:** SAN-1 (compose topology), and the open decision **"demo-tool runs as a separate compose service (one-shot)"**

- Edit `docker-compose.yml` per `design.md` "Files created / modified / deleted":
  - Add a `demo-tool` service with: build context `apps/demo-tool/`, image tag `bonafide/demo-tool:latest`, label `bonafide.workload=tool`, the SPIRE Workload API socket mount at `/run/spire/sockets/agent.sock`, env-var inheritance for `BONAFIDE_VAULT_AUTH_MODE`, `BONAFIDE_AUTHZ_TOKEN_URL`, `BONAFIDE_VAULT_ADDR`, and any other env vars the planner service consumes.
  - The service is one-shot, run via `docker compose run --rm demo-tool ...` (no always-on `command:` / `restart:` policy), per `design.md` "Open decisions resolved here".
  - The image-id and label selectors must match the SPIRE registration entry added in T-04 — sharing a container with `demo-planner` would defeat the workload attestation (per `design.md` "Open decisions resolved here": "Sharing a container would defeat the SPIRE workload attestation — the two need different image IDs so SPIRE issues different SVIDs").
- Do not add any new env vars beyond what VSA already exports (per `design.md` "Open decisions resolved here": "No new env vars").

**Verified when:** `docker compose config` parses without error and lists `demo-tool` as a service with build context `apps/demo-tool/`, image `bonafide/demo-tool:latest`, label `bonafide.workload=tool`, and the SPIRE socket mount. `docker compose images` after a build shows `bonafide/demo-tool:latest` with a distinct image-id from `bonafide/demo-planner:latest`. No new env-var key appears in `docker-compose.yml` that was not already present in the VSA-era file.

---

## T-07: [ ] Add the `agent/tool` registration to `policies/delegation.rego`

**Satisfies:** SAN-3 (policy permits the tool actor), SAN-8 (cap applies; tool registration does not bypass the cap)

- Edit `policies/delegation.rego` per `design.md` "OPE policy — one added registration": append one entry to the `registrations` data list:
  - `actor: "spiffe://bonafide.local/agent/tool"`
  - `subject_prefix: "spiffe://bonafide.local/human/"`
  - `scope: "calendar:read:alice@example.com"`
  - `audience: "http://calendar.bonafide.local:9000"`
- Per `design.md` "OPE policy": the policy authorizes by `(subject, actor, scope, audience)` only; `existing_chain` is consulted by Rego solely for the depth cap. Do not add any chain-shape constraint between `planner` and `tool` — that is explicitly out of scope per the slice's "Open decisions resolved here" and "Out of scope" lists.
- The `max_chain_depth` cap default of 4 is unchanged (per `design.md` "Open decisions resolved here": "`max_chain_depth = 4` stays the default").
- Reload the authz process with `docker compose kill -s HUP authz` per OPE-7; the reload is idempotent and atomic.

**Verified when:** After editing and reloading, `opa eval --data policies/delegation.rego --input '{"subject":"spiffe://bonafide.local/human/alice@example.com","actor":"spiffe://bonafide.local/agent/tool","scope":"calendar:read:alice@example.com","audience":"http://calendar.bonafide.local:9000","existing_chain":["spiffe://bonafide.local/agent/planner"]}' 'data.bonafide.delegation.allow'` returns `true`. The same eval with `actor` flipped to `spiffe://bonafide.local/agent/unknown` returns `false`. A query with `existing_chain` of length 4 (so the resulting chain would be depth 5) returns `false` with `reason == "chain_too_deep"` (cap unchanged at 4).

---

## T-08: [ ] Populate `evidence_chain` in `demo-calendar` from `chain.prior_actors`

**Satisfies:** SAN-5, and the safety constraint **"Authorization decisions use top-level `sub` + outermost `act.sub` only. Inner `act` entries are evidence and audit material, never authorization input"**

- Edit `apps/demo-calendar/demo_calendar/main.py` per `design.md` "demo-calendar — evidence_chain population":
  - In the `GET /events` handler, change `evidence_chain: []` to `evidence_chain: list(chain.prior_actors)` in the JSON response body.
  - The authorization decision must remain a function of `(chain.subject, chain.current_actor, scope)` only — do not introduce any reference to `chain.prior_actors` inside a conditional or policy check, per CONTRACT.md §6.2 and SAN-5 acceptance criterion ("Authorization decisions performed by the calendar application are a function of `(sub, current_actor, scope)` only; inner `act` entries are never used as authorization input").
- No new typed model needed — `ActorChain.prior_actors` is already a `tuple[str, ...]` from earlier slices.

**Verified when:** A pytest in `apps/demo-calendar/tests` asserts: (a) a depth-1 task token (no `act.act`) produces a 200 response whose `evidence_chain == []`; (b) a depth-2 task token with `act.act.sub == "spiffe://bonafide.local/agent/planner"` produces a 200 response whose `evidence_chain == ["spiffe://bonafide.local/agent/planner"]`. `grep -n 'prior_actors' apps/demo-calendar/demo_calendar/main.py` shows `prior_actors` referenced only in the response-body assembly (the `evidence_chain` line), never inside an `if`/`elif`/`assert` that gates access.

---

## T-09: [ ] Add depth-2 canonical-ID tests to `act_chain_test.go` (extend, never delete)

**Satisfies:** SAN-3 (the depth-2 test acceptance criterion), and the safety constraint **"The `act` chain in minted tokens must always nest, never overwrite. A test asserting this against `CONTRACT.md` §6 ships in Slice 1 and is never deleted. This slice extends those tests to assert that the depth-2 mint's `act.act` equals the subject_token's `act` byte-for-byte"**

- Append (do NOT delete or replace) tests to `services/authz/internal/exchange/act_chain_test.go` per `design.md` "`act_chain_test.go` — depth-2 with canonical IDs":
  - `TestBuildAct_Depth2_CanonicalSpiffeIDs`: with `subjectAct = &Act{Sub: "spiffe://bonafide.local/agent/planner"}` and `currentActor = "spiffe://bonafide.local/agent/tool"`, assert the result equals `&Act{Sub: "spiffe://bonafide.local/agent/tool", Act: &Act{Sub: "spiffe://bonafide.local/agent/planner"}}` per CONTRACT.md §6.1 nested form. Additionally assert byte-for-byte that `got.Act` equals `subjectAct` ("the nested act must equal the subject_token's act subtree").
  - `TestBuildAct_NeverMutatesSubjectActsCopyIsDefensive`: after `BuildAct` returns, mutate the input `*Act`'s `Sub` field; assert the returned `*Act`'s inner `Sub` is unaffected (proves `cloneAct` is real, not aliased — already proven at TEC-T-05 against placeholder IDs; SAN proves it at canonical IDs).
  - `TestFlattenChain_Depth2`: with `act = &Act{Sub: "spiffe://bonafide.local/agent/tool", Act: &Act{Sub: "spiffe://bonafide.local/agent/planner"}}`, assert `FlattenChain(act) == ["spiffe://bonafide.local/agent/tool", "spiffe://bonafide.local/agent/planner"]` (matches the canonical `resulting_chain` ordering in CONTRACT.md §9: current-actor-first).
- The TEC placeholder-ID tests (rows 1–6 of `TestBuildAct` from TEC-T-05) remain in place per `CLAUDE.md`'s rule that the act-chain tests are never deleted, only extended — both sets continue to run.

**Verified when:** `go test ./internal/exchange/... -run 'TestBuildAct|TestFlattenChain' -count=1` exits zero and the test output names all three new tests as passing alongside the preserved TEC tests. `grep -c 'func Test' services/authz/internal/exchange/act_chain_test.go` shows the count strictly greater than the count after TEC-T-05 (extension, not replacement). A `git log --follow -p services/authz/internal/exchange/act_chain_test.go` shows the TEC table-driven test still present in the working tree.

---

## T-10: [ ] Add depth-2 resource-SDK middleware tests

**Satisfies:** SAN-4, SAN-5 (resource-side acceptance), and the safety constraint **"The impersonation guard is unconditional ... The guard remains active and is verified at depth 2"**

- Append tests to `sdks/resource-py/tests/test_middleware.py` per `design.md` "Resource SDK tests — depth-2":
  - `test_depth2_chain_is_extracted_correctly`: sign a task token with `sub == "spiffe://bonafide.local/human/alice@example.com"`, `act == {"sub": "spiffe://bonafide.local/agent/tool", "act": {"sub": "spiffe://bonafide.local/agent/planner"}}`, `iss`/`aud`/`exp`/`iat`/`jti` set per CONTRACT.md §5. Call the validator and assert `chain.subject == ".../human/alice@example.com"`, `chain.current_actor == ".../agent/tool"`, `chain.prior_actors == (".../agent/planner",)` per CONTRACT.md §6.2.
  - `test_depth2_inner_actor_does_NOT_affect_authorization`: construct two chains identical in `(subject, current_actor, scope)` but differing in the inner `act.act.sub` (planner vs. some other SPIFFE ID). Assert the calendar's authorization decision is identical for both — per SAN-5 acceptance criterion: "flipping the inner `act.act.sub` from the planner to any other SPIFFE ID does not change the calendar application's authorization decision for the same `(sub, act.sub, scope)` tuple".
  - `test_malformed_inner_act_triggers_impersonation_guard`: sign a token whose inner `act.act` is malformed (e.g. `{"not_sub": "x"}` instead of `{"sub": "..."}`). Assert the validator raises `HTTPException` with `status_code == 401` and detail referencing the impersonation guard, per CONTRACT.md §6.3 and SAN-4 acceptance criterion.
  - An additional test asserting that the validator records an `impersonation_guard_triggered` event when a depth-2 token's `sub` does not match the subject_token's `sub` (the impersonation guard at depth 2, per SAN-4 acceptance criterion).
- These tests prove SAN-4 and SAN-5 at the unit level before the smoke harness exercises them end-to-end (T-13).

**Verified when:** `pytest sdks/resource-py/tests/test_middleware.py -k 'depth2 or impersonation' -count=1` exits zero. The test output names all four new tests. Each test's assertion references the specific CONTRACT.md anchor it covers (§6.1 for nesting, §6.2 for the authorization rule, §6.3 for the impersonation guard).

---

## T-11: [ ] Add depth-2 handler test in `services/authz/internal/exchange`

**Satisfies:** SAN-3, SAN-4 (mint-time enforcement), SAN-8 (cap respected at depth 2), and the safety constraints **"The impersonation guard is unconditional"** and **"The `act` chain in minted tokens must always nest, never overwrite"**

- Add a test (or test cases) to `services/authz/internal/exchange/handler_test.go` covering the depth-2 mint path:
  - Drive the handler with `subject_token` = a depth-1 task token (signed by the authz signer in a test fixture) carrying `act = {"sub": "spiffe://bonafide.local/agent/planner"}` and `sub = "spiffe://bonafide.local/human/alice@example.com"`, and `actor_token` = a JWT-SVID-shaped token whose `sub == "spiffe://bonafide.local/agent/tool"`.
  - Assert the response is HTTP 200 with body per CONTRACT.md §8.
  - Decode the minted `access_token` and assert: `sub == subject_token.sub` (per CONTRACT.md §§5, 6.3 — `sub` is never mutated; SAN-4 mint-time check); `act.sub == "spiffe://bonafide.local/agent/tool"`; the nested `act.act` claim equals the subject_token's `act` subtree using structural equality via `require.JSONEq` (per CONTRACT.md §6.1 rule 3 and the safety-constraint test extension required by SAN-3); `exp - iat <= 300` per CONTRACT.md §5 and `DESIGN.md` §4 (SAN-3 acceptance criterion: "the depth-2 token's TTL ceiling is not relaxed relative to the depth-1 ceiling").
  - A negative case: a `subject_token` carrying a depth-3 chain such that the resulting chain would be depth 5; assert HTTP 400 `access_denied` with `policy_reason == "chain_too_deep"` per CONTRACT.md §7 and SAN-8 acceptance criterion ("a token-exchange request that would yield a chain whose depth exceeds the configured `max_chain_depth` cap is denied").
  - A negative case for SAN-4 at depth 2: the impersonation guard at mint time — assert that for every accepted `subject_token` shape including a depth-1 task token carrying a single-actor `act`, `mint(subject_token).sub == subject_token.sub` (per SAN-4 acceptance criterion and CONTRACT.md §6.3).

**Verified when:** `go test ./internal/exchange/... -run 'TestHandler_Depth2' -count=1` exits zero. The happy-path assertion compares the minted `act.act` to the subject_token's `act` using a JSON byte-for-byte comparison (e.g. `require.JSONEq`). The chain-too-deep negative case asserts both HTTP 400 and that the audit emitter received a `denied` event whose `policy_reason == "chain_too_deep"` (per SAN-8 acceptance criterion: "the denial in the over-cap case occurs before any task token is minted; no token is issued and no `outcome=\"minted\"` audit event is emitted for the denied request").

---

## T-12: [ ] Add depth-2 audit + chain-reconstruction tests in `services/control`

**Satisfies:** SAN-6, SAN-7, CONTRACT.md §§9, 10

- Add a test (or test cases) to the control-plane audit/chain tests (introduced in AUD) covering the depth-2 case:
  - Ingest two audit events through the control plane's `POST /audit/events` endpoint: (a) the depth-1 event with `existing_chain == []`, `resulting_chain == ["spiffe://bonafide.local/agent/planner"]`, `subject == ".../human/alice@example.com"`, `actor == ".../agent/planner"`, `outcome == "minted"`; (b) the depth-2 event with `existing_chain == [".../agent/planner"]`, `resulting_chain == [".../agent/tool", ".../agent/planner"]`, `subject == ".../human/alice@example.com"` (unchanged from the human), `actor == ".../agent/tool"`, `outcome == "minted"`, per CONTRACT.md §9 and SAN-6 acceptance criteria.
  - `GET /audit/chain/{event_id_2}` returns HTTP 200 with body per CONTRACT.md §10: `subject == ".../human/alice@example.com"`, `actors == [".../agent/tool", ".../agent/planner"]`, `current_actor == ".../agent/tool"`, `reconstructed_from` includes both `"audit_event"` and `"token_act_claim"`, `consistent == true`. Cross-check the chain derived from the audit event's `resulting_chain` against the chain derived from decoding the depth-2 task token's `act` claim — they must be equal (per SAN-7 acceptance criterion: "the chain derived from the audit event's `resulting_chain` equals the chain derived from decoding the minted token's `act` claim").
  - `GET /audit/chain/{event_id_1}` returns HTTP 200 with `actors == [".../agent/planner"]` (exactly one element) — demonstrating the same endpoint reconstructs both depths correctly, per SAN-7 acceptance criterion.
  - SAN-6 cross-checks: assert each audit event's `event_id == token.jti`, `token_jti == token.jti`, `token_exp == time.Unix(token.exp).UTC().Format(time.RFC3339Nano)`, `scope_granted` matches CONTRACT.md §2 grammar, per CONTRACT.md §9 and SAN-6 acceptance criteria.

**Verified when:** `pytest services/control/tests -k 'depth2 or chain_reconstruction' -count=1` exits zero. The test fixtures include a real depth-2 task token signed by a test Ed25519 key so the `token_act_claim` reconstruction source is real (not stubbed). A test asserts that perturbing the audit event's `resulting_chain` (so it disagrees with the token's `act`) yields `consistent == false` and a `discrepancy` field per CONTRACT.md §10.

---

## T-13: [ ] Append the SAN block to `scripts/smoke.sh` (end-to-end depth-2 chain)

**Satisfies:** SAN-9, and every preceding SAN capability end-to-end (SAN-1 through SAN-8) — the cumulative smoke check is a strict prefix of all six slices

- Append a new block to `scripts/smoke.sh` between markers `#--- SAN block ---` and `#--- end SAN block ---` exactly per `design.md` "Smoke harness — SAN block":
  1. Mint the user JWT via `docker compose run --rm demo-human python -m demo_human --email alice@example.com`.
  2. Run the planner: `PLANNER_OUT=$(docker compose run --rm demo-planner python -m demo_planner --user-jwt "$USER_JWT")`. Extract `TASK_TOKEN_1` and `EVENT_ID_1` (the latter from the JWT `jti`).
  3. Run the tool: `TOOL_OUT=$(docker compose run --rm demo-tool python -m demo_tool --planner-task-token "$TASK_TOKEN_1")`. Assert `acting_for`, `acted_by`, `evidence_chain == ["spiffe://bonafide.local/agent/planner"]`, and `(.events | length) > 0` per CONTRACT.md §§6.2, 8 and SAN-9 acceptance criteria.
  4. Re-run `demo-tool` with `--print-task-token-and-exit` to capture `TASK_TOKEN_2`. Decode the payload to JSON and assert with `jq -e '.act == {"sub":"spiffe://bonafide.local/agent/tool","act":{"sub":"spiffe://bonafide.local/agent/planner"}}'` (jq performs structural map-equality, so JSON key order does not affect the assertion) per CONTRACT.md §6.1. Assert `payload.sub == "spiffe://bonafide.local/human/alice@example.com"` for both `TASK_TOKEN_1` and `TASK_TOKEN_2` (the human is never mutated, per CONTRACT.md §§5, 6.1, 6.3 and SAN-9 acceptance criteria).
  5. Extract `EVENT_ID_2` from the `jti` of `TASK_TOKEN_2`. `sleep 1` for audit WAL drain. `GET /audit/chain/$EVENT_ID_2` on the control plane: assert `actors == [".../agent/tool", ".../agent/planner"]`, `current_actor == ".../agent/tool"`, `reconstructed_from` includes both `"audit_event"` and `"token_act_claim"` (use `jq '.reconstructed_from | sort'`), `consistent == true` — per CONTRACT.md §10 and SAN-9 acceptance criteria.
  6. `GET /audit/chain/$EVENT_ID_1` on the control plane: assert `actors == [".../agent/planner"]` exactly — same endpoint handles both depths per SAN-7 acceptance criterion.
  7. Final line: `echo "[smoke:SAN] OK — MVP smoke harness complete."`.
- Per SAN-9 acceptance criterion: "All prior smoke-check blocks from `token-exchange-core`, `spire-workload-identity`, `vault-spiffe-auth`, `opa-policy-engine`, and `audit-persistence` continue to pass unchanged." Do not modify any earlier block.

**Verified when:** Against a freshly brought-up topology (`./scripts/bootstrap.sh`), `./scripts/smoke.sh` exits zero with no manual steps between bring-up and harness execution. The smoke output contains the line `[smoke:SAN] OK — MVP smoke harness complete.`. A `grep -c 'SAN block' scripts/smoke.sh` returns 2 (start + end markers). All earlier blocks (TEC, SWI, VSA, OPE, AUD) continue to pass — running `./scripts/smoke.sh` against the same topology twice produces identical zero exits, demonstrating idempotency. A `git diff` on the `scripts/smoke.sh` file shows only the appended SAN block; no edit to any earlier block.

---

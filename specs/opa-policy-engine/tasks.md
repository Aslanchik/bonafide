# opa-policy-engine: Tasks

Tasks are ordered. Each must be complete and verifiable before the next begins. If a task surfaces a spec problem, stop — update the relevant spec, re-review, then continue. One commit per task.

Status: `[ ]` todo · `[x]` done · `[~]` in progress

---

## T-01: [ ] Add `github.com/open-policy-agent/opa` to `services/authz` go.mod

**Satisfies:** OPE-1

- Add `github.com/open-policy-agent/opa` as a direct dependency of `services/authz` per `design.md` "Stack (additions only)". Use the latest stable OPA release that exposes both `opa/rego` and `opa/ast`.
- Do not add any other Rego-related dependency (no `opa run` sidecar tooling, no YAML→Rego data loaders, no bundle libraries) — `design.md` "Stack" lists exactly these two import paths and `CLAUDE.md` requires asking before adding any dependency not on the stack pins.
- Run `go mod tidy` so `go.sum` is consistent. Do not modify any non-stub Go source file in this task.

**Verified when:** `go build ./...` inside `services/authz` exits zero, `go mod why github.com/open-policy-agent/opa/rego` confirms the dependency is direct, and the build binary is deleted immediately after per `CLAUDE.md` "Delete compiled binaries after build/test".

---

## T-02: [ ] Author `policies/delegation.rego`

**Satisfies:** OPE-1, OPE-2, OPE-3, OPE-5

- Create `policies/delegation.rego` at the repo root with the exact contents shown in `design.md` "The example `policies/delegation.rego`":
  - `package bonafide.delegation` and `import rego.v1`.
  - `max_chain_depth := 4` as a top-level value (OPE-5 default; inclusive of the new hop).
  - A `registrations` list with the two example entries (`agent/planner` and `agent/tool`, both for `subject_prefix = "spiffe://bonafide.local/human/"`, `scope = "calendar:read:alice@example.com"`, `audience = "http://calendar.bonafide.local:9000"`).
  - `default decision := { "allowed": false, "scope_grant": "", "reason": "default_deny" }`.
  - The four conditional `decision` rules (allow path, `unknown_scope` denial, `chain_too_deep` denial, `agent_not_registered_for_scope` denial) in the order shown in `design.md`.
  - The three predicates `scope_well_formed`, `chain_within_cap`, `matched_registration` as shown in `design.md`.
- The `scope_well_formed` regex must be `^[a-z][a-z0-9-]*:(read|write|admin):[^\s:]+$` per `design.md`; this is the canonical encoding of `CONTRACT.md` §2 in Rego.
- `chain_within_cap` must be `count(input.existing_chain) + 1 <= max_chain_depth` per OPE-5 acceptance criterion "the cap is inclusive of the new hop".
- Add a top-of-file comment naming this as the single canonical policy artifact of the MVP and referencing OPE-1 through OPE-5.

**Verified when:** `opa parse policies/delegation.rego` exits zero (run via `docker run --rm -v $PWD/policies:/policies openpolicyagent/opa:latest parse /policies/delegation.rego` — no host toolchain required), and `grep -E 'max_chain_depth := 4|scope_well_formed|chain_within_cap|matched_registration' policies/delegation.rego` returns at least four matches.

---

## T-03: [ ] Implement `policy/rego.go` — `RegoGate` with `LoadFromDisk`, `Decide`, `CurrentBytes`

**Satisfies:** OPE-1, OPE-2, OPE-3, and the safety constraint **"Fail closed. Missing actor_token, unknown agent, unknown scope grammar, unparseable token, unreachable policy engine, expired anything → deny. There is no permissive mode and no fallback that grants access."**

- Create `services/authz/internal/policy/rego.go` with the `regoGate` struct and `NewRegoGate`, `loadFromDisk`, `Decide`, `decodeDecision`, `CurrentBytes` functions exactly per `design.md` "Go integration — `policy/rego.go`".
- `regoGate` holds a `sync.RWMutex`, a `rego.PreparedEvalQuery`, and `rawBytes []byte` (used by OPE-6 control-plane endpoint).
- `NewRegoGate(ctx, path)` reads the file and calls `rego.New(rego.Query("data.bonafide.delegation.decision"), rego.Module(path, string(raw))).PrepareForEval(ctx)`. A read or compile failure returns a non-nil error so the caller (T-09) can exit non-zero.
- `Decide(ctx, in PolicyInput) (Decision, error)`:
  - Takes the read lock, copies the prepared query reference, releases the lock, runs `q.Eval` with the seven fields of `PolicyInput` (`subject`, `subject_claims`, `actor`, `actor_claims`, `scope`, `audience`, `existing_chain`) mapped into a `map[string]any` whose keys are exactly those names per OPE-2.
  - An engine error returns `Decision{Allowed: false, Reason: "policy_engine_error"}` with a `nil` Go error — per `design.md` "Open decisions resolved here" and the OPE-4 acceptance criterion "If the policy gate is unreachable from the exchange handler's point of view for any reason, the request is denied".
  - An empty result set returns `Decision{Allowed: false, Reason: "default_deny"}`.
  - The decoded decision document with a missing required field returns `Decision{Allowed: false, Reason: "malformed_decision"}` per OPE-4 "A policy that does not define the required output rule, or returns a document missing `allowed`, `scope_grant`, or `reason`, results in HTTP 400 with `error=access_denied`".
- The `Gate` interface from `policy/policy.go` (shipped in TEC) is unchanged; `regoGate` satisfies it via its `Decide` method. Do not modify the interface or its `PolicyInput`/`Decision` shapes in this task.
- Export `LoadFromDisk` and `RegoGate` (capitalised) so `reload/reload.go` in T-04 can call them from a sibling package.

**Verified when:** `go build ./internal/policy/...` exits zero, `grep -n 'func.*NewRegoGate\|func.*LoadFromDisk\|func.*Decide\|func.*CurrentBytes\|policy_engine_error\|default_deny\|malformed_decision' services/authz/internal/policy/rego.go` shows every function and every fail-closed reason string, and `go vet ./internal/policy/...` reports no issues.

---

## T-04: [ ] Implement `policy/reload/reload.go` — SIGHUP-driven atomic policy reload

**Satisfies:** OPE-7, and the OPE-4 acceptance criterion **"a Rego policy that becomes unparseable on reload causes the previously loaded good policy to remain in force ... a new bad policy is never adopted"**

- Create `services/authz/internal/policy/reload/reload.go` exactly per `design.md` "Reload — `policy/reload/reload.go`":
  - `Watch(ctx context.Context, gate *policy.RegoGate, path string)` installs `signal.Notify(ch, syscall.SIGHUP)` and spawns a goroutine that loops on `ch` until `ctx.Done()`.
  - On every SIGHUP, the goroutine calls `gate.LoadFromDisk(ctx, path)`. Success logs `policy_reloaded` at INFO; failure logs `policy_reload_failed` at ERROR with the path and error, and the goroutine continues without unloading the prior good query.
  - The reload is atomic: `LoadFromDisk` takes the write lock only after a successful `PrepareForEval`. A parse failure never touches `gate.query` or `gate.rawBytes` — the prior policy remains active.
- The reload mechanism is the single OPE-7 mechanism: SIGHUP, with no fallback and no alternative. Do not introduce any new wire format, control-plane endpoint, or filesystem-watching dependency — `CONTRACT.md` is not amended by this slice (per OPE-7 acceptance criterion "does not introduce any new wire format not defined in `CONTRACT.md`").

**Verified when:** `go build ./internal/policy/reload/...` exits zero, and `grep -n 'SIGHUP\|policy_reload_failed\|policy_reloaded\|LoadFromDisk' services/authz/internal/policy/reload/reload.go` returns at least four matches confirming the chosen mechanism, both log lines, and the call into the gate.

---

## T-05: [ ] Table-driven tests for `RegoGate.Decide` against the example policy

**Satisfies:** OPE-1, OPE-2, OPE-3, OPE-4, OPE-5

- Create `services/authz/internal/policy/rego_test.go` with table-driven tests against `policies/delegation.rego` (load the file from disk via a relative path, or copy its contents inline as a test fixture, whichever keeps the test self-contained):
  - `TestHappyPath`: input `{actor: planner, subject: human/alice, scope: calendar:read:alice@example.com, audience: http://calendar.bonafide.local:9000, existing_chain: []}` → `Allowed=true, ScopeGrant="calendar:read:alice@example.com", Reason=""`.
  - `TestUnknownAgentDeny`: input identical to the happy path but with `actor = spiffe://bonafide.local/agent/unregistered` → `Allowed=false, Reason="agent_not_registered_for_scope"`.
  - `TestMismatchedAudienceDeny`: identical to happy path but with `audience = http://other.bonafide.local:9000` → `Allowed=false, Reason="agent_not_registered_for_scope"`.
  - `TestMismatchedScopeDeny`: identical to happy path but with `scope = calendar:write:alice@example.com` → `Allowed=false, Reason="agent_not_registered_for_scope"` (the scope is well-formed but no registration exists for it).
  - `TestMalformedScopeDeny`: identical to happy path but with `scope = "not_a_scope"` → `Allowed=false, Reason="unknown_scope"` (OPE-4: scope grammar enforced in Rego per `design.md` "Open decisions resolved here").
  - `TestChainDepthDenial`: identical to happy path but with `existing_chain = ["agent/a", "agent/b", "agent/c", "agent/d"]` (count = 4, new hop would make 5) → `Allowed=false, Reason="chain_too_deep"`. This test is the explicit dependency of the smoke harness in T-12 — its name must be `TestChainDepthDenial` so `docker compose exec -T authz go test ./internal/policy -run TestChainDepthDenial -v` resolves it (per `design.md` "Smoke harness — OPE block").
  - `TestChainDepthAtCapAllowed`: identical to happy path but with `existing_chain` of length 3 (new hop = 4, equal to `max_chain_depth`) → `Allowed=true` (OPE-5 acceptance criterion: "the cap is inclusive of the new hop").
  - `TestEmptyExistingChain`: confirms `existing_chain = []` is accepted as the empty list (OPE-2: "is an empty list when the subject_token carries no `act` claim").

**Verified when:** `go test ./internal/policy -run 'TestHappyPath|TestUnknownAgentDeny|TestMismatchedAudienceDeny|TestMismatchedScopeDeny|TestMalformedScopeDeny|TestChainDepthDenial|TestChainDepthAtCapAllowed|TestEmptyExistingChain' -count=1 -v` exits zero and all eight cases pass.

---

## T-06: [ ] Wire `cmd/authz/main.go` to construct `RegoGate` instead of `MapGate`; install SIGHUP handler

**Satisfies:** OPE-1, OPE-7, and the OPE-4 acceptance criterion **"The authz server starts only if it can successfully load and parse a Rego policy at a configured path; if the file is missing, unreadable, or fails to parse, startup fails with a non-zero exit code and an error log identifying the policy path."**

- Modify `services/authz/cmd/authz/main.go` per `design.md` "`services/authz/cmd/authz/main.go` — wiring delta":
  - Replace the `policy.NewMapGate(...)` call with `gate, err := policy.NewRegoGate(ctx, cfg.PolicyPath)`. The `cfg.PolicyPath` env var is the existing `BONAFIDE_AUTHZ_POLICY_PATH` from TEC T-13 (no new env var introduced).
  - On error, log a structured slog line with key `path=cfg.PolicyPath` (so the failure message identifies the policy file, per OPE-4's first acceptance criterion) and call `os.Exit(1)`.
  - After successful construction, call `reload.Watch(ctx, gate, cfg.PolicyPath)` to install the SIGHUP handler from T-04.
  - The handler wiring (mounting `POST /token` etc.) is otherwise unchanged.
- Update the `BONAFIDE_AUTHZ_POLICY_PATH` documentation/comment in `internal/config/config.go` to note the value points to a `.rego` file rather than the prior YAML (the env var name does not change).

**Verified when:** Running the binary with `BONAFIDE_AUTHZ_POLICY_PATH=/nonexistent.rego` exits non-zero within 1 second and emits a slog line containing the string `/nonexistent.rego`. Running it with `BONAFIDE_AUTHZ_POLICY_PATH=policies/delegation.rego` and the rest of the TEC required env vars binds the listener and `curl http://127.0.0.1:8080/healthz` returns 200. Sending `SIGHUP` to the running binary while logging at INFO produces a `policy_reloaded` log line.

---

## T-07: [ ] Delete `policy/map.go` and `deploy/authz/policy.yaml`

**Satisfies:** OPE-1 (the Rego gate is the single policy implementation), OPE-4 (no fallback path)

- Delete `services/authz/internal/policy/map.go` (the TEC in-memory implementation). No production code may reference `MapGate` after this task.
- Delete `services/authz/internal/policy/map_test.go` if it exists alongside `map.go`.
- Delete `deploy/authz/policy.yaml` (the TEC allow-table file). `policies/delegation.rego` is now the single policy artifact, per OPE's out-of-scope note "This slice ships exactly one example policy file".
- Update any compose volume mount that pointed at `deploy/authz/policy.yaml` in `docker-compose.yml` to point at `./policies/delegation.rego` mounted at `/etc/authz/delegation.rego` instead, per `design.md` "Go integration — `policy/rego.go`": "`cfg.PolicyPath` defaults to `/etc/authz/delegation.rego`. The compose volume mount points it at `/path/to/repo/policies/delegation.rego` in the authz container."
- The `Gate` interface in `policy/policy.go` is retained unchanged.

**Verified when:** `git ls-files | grep -E 'internal/policy/map\.go|deploy/authz/policy\.yaml'` returns no matches, `grep -rn 'MapGate' services/authz` returns no matches in production code, and `docker compose config` shows a volume mount for `./policies/delegation.rego` into the authz container.

---

## T-08: [ ] Exchange handler — pass `existing_chain` and consume Rego `reason` verbatim

**Satisfies:** OPE-2, OPE-3, OPE-4, OPE-8, and the safety constraint **"Authorization decisions use top-level `sub` + outermost `act.sub` only. Inner `act` entries are evidence and audit material, never authorization input."**

- Modify `services/authz/internal/exchange/handler.go` (the TEC handler) so the `PolicyInput` it builds carries the OPE-2 input contract:
  - `subject = subject_claims.Sub` (top-level human SPIFFE ID per `CONTRACT.md` §4).
  - `subject_claims` = the decoded `UserJWTClaims` map (full claim set after signature verification per OPE-2 "neither is ever populated from an unverified token").
  - `actor = actor_claims.Sub` (outermost `act.sub` — the SPIFFE ID presenting the actor_token per `CONTRACT.md` §1).
  - `actor_claims` = the decoded `ActorTokenClaims` map.
  - `scope` = the verbatim `scope` form parameter per `CONTRACT.md` §7.
  - `audience` = the verbatim `audience` form parameter per `CONTRACT.md` §7.
  - `existing_chain = FlattenChain(subject_claims.Act)` per OPE-2 "outermost-first, and is an empty list when the subject_token carries no `act` claim". `FlattenChain` is the existing TEC function in `services/authz/internal/exchange/act_chain.go`; do not modify it.
- On a denial (`!decision.Allowed`), map `decision.Reason` to the HTTP `error` code per OPE-4 and `design.md` "Open decisions resolved here":
  - `unknown_scope` → `error=invalid_scope` per `CONTRACT.md` §7.
  - Any other reason (`agent_not_registered_for_scope`, `chain_too_deep`, `default_deny`, `policy_engine_error`, `malformed_decision`) → `error=access_denied`.
  - `error_description` is **exactly** `decision.Reason` (no rewording, no prefix). This is the OPE-8 wire requirement: "`error_description` is exactly the `reason` string returned by the policy".
- On a denial, emit the audit event with `outcome="denied"`, `policy_reason=decision.Reason`, `scope_granted=null`, `token_jti=null`, `token_exp=null` per `CONTRACT.md` §9 and OPE-8.
- The minted task token's `scope` claim is exactly `decision.ScopeGrant` (OPE-3 acceptance criterion: "The `scope` set on a minted task token is exactly `scope_grant`; the handler does not synthesize, broaden, or narrow it").
- The TTL cap is unchanged: `Exp = now + min(settings.TaskTokenTTLSeconds, 300)` per `CONTRACT.md` §5 and the safety constraint **"All credentials short-lived per `DESIGN.md` §4 TTL budget. ... the handler caps the minted token's `exp` per `CONTRACT.md` §5 regardless of policy output."**

**Verified when:** `go test ./internal/exchange -run TestHandler -count=1` passes against an updated test table that includes: (a) a denied case asserting `error=access_denied` and `error_description="agent_not_registered_for_scope"`; (b) a denied case for a malformed scope asserting `error=invalid_scope` and `error_description="unknown_scope"`; (c) the happy path still mints a token with `scope == decision.ScopeGrant`. `grep -n 'existing_chain\|FlattenChain' services/authz/internal/exchange/handler.go` confirms the input is wired.

---

## T-09: [ ] Authz handler — fail-closed paths for missing decision fields and policy engine errors

**Satisfies:** OPE-4, and the safety constraint **"Fail closed ... There is no permissive mode and no fallback that grants access."**

- In `services/authz/internal/exchange/handler.go`, ensure the policy result is consumed strictly. A returned `Decision` with `Allowed=false` and any of the reserved reasons (`malformed_decision`, `policy_engine_error`, `default_deny`) results in `error=access_denied` per `CONTRACT.md` §7 and an audit event with `outcome="denied"` and a non-null `policy_reason` per `CONTRACT.md` §9 (OPE-4 second acceptance criterion).
- The handler never reads any decision field other than `Allowed`, `ScopeGrant`, `Reason` (OPE-3 acceptance criterion: "The exchange handler never inspects any field other than these three"). An extra field on the Rego document is silently ignored by `decodeDecision` in T-03.
- Scope-grammar enforcement is owned by Rego. The Rego module from T-02 returns `reason="unknown_scope"` for a syntactically invalid scope. T-08 already maps `unknown_scope` to HTTP `error=invalid_scope`, so the wire surface required by OPE-4's third acceptance criterion is satisfied without a second Go-side regex check. **Do not introduce a Go-side scope grammar constant** — two regexes would drift, and the project's safety stance is that scope grammar lives in exactly one place (the canonical Rego module per T-02). Per `CLAUDE.md` "don't extend the grammar quietly."
- Add tests (in `services/authz/internal/exchange/handler_test.go`) asserting:
  - A malformed scope form parameter yields HTTP 400 `invalid_scope` via the Rego `unknown_scope` path (the policy *is* consulted, but returns `unknown_scope`; T-08's mapping converts it to `invalid_scope`).
  - A `Gate` whose `Decide` returns `{Allowed: false, Reason: "policy_engine_error"}` yields HTTP 400 `access_denied` and an audit event with `policy_reason="policy_engine_error"`.
  - A `Gate` whose `Decide` returns `{Allowed: false, Reason: "malformed_decision"}` yields HTTP 400 `access_denied`.

**Verified when:** `go test ./internal/exchange -run TestHandler -count=1` exits zero with the three new test cases passing, and `grep -rn 'scope_well_formed\|scopeRegex\|scope.*regex' services/authz/internal/exchange/` returns **no Go regex constants** for scope grammar (the single source of truth lives in `policies/delegation.rego`).

---

## T-10: [ ] Control plane — `GET /policies/current` returns the loaded Rego file

**Satisfies:** OPE-6 (first acceptance criterion)

- Create `services/control/app/policy/__init__.py` (empty) and `services/control/app/policy/routes.py` per `design.md` "Control plane endpoints (OPE-6)":
  - An `APIRouter()` exposing `GET /policies/current` that returns `PlainTextResponse` with the bytes of `BONAFIDE_CONTROL_POLICY_PATH` (the same file the authz container mounts).
  - The control container reads the file fresh on every call; there is no cache. `design.md` notes this can be momentarily ahead of authz between an edit and the SIGHUP — that is expected and not an error.
  - The route accepts only `GET` (FastAPI's default for `@router.get`); confirm by absence of `@router.post`/`@router.put` decorations on the path (OPE-6: "Neither endpoint accepts a write; both reject any method other than `GET`").
- Mount the router in `services/control/app/main.py` so the path resolves at `GET /policies/current`.
- Update the compose `control` service to mount the host `./policies/` directory read-only into the container at the path `BONAFIDE_CONTROL_POLICY_PATH` resolves to, per `design.md` "Repo additions and deletions": "the control container gains a read-only mount on the audit log volume" — extend the same logic to the policy file.

**Verified when:** With the topology brought up, `curl -fsSL http://localhost:8090/policies/current` returns the literal text of `policies/delegation.rego` (status 200, `Content-Type: text/plain; charset=utf-8`), `curl -X POST http://localhost:8090/policies/current` returns 405, and `diff <(curl -fsSL http://localhost:8090/policies/current) policies/delegation.rego` produces no output.

---

## T-11: [ ] Control plane — `GET /policies/decisions/{event_id}` denial-trace endpoint

**Satisfies:** OPE-6 (second, third, fourth, fifth acceptance criteria)

- Create `services/control/app/policy/audit_reader.py` per `design.md` "Control plane endpoints (OPE-6)":
  - A `find_event(event_id: str) -> dict | None` async function that scans the NDJSON audit file at `BONAFIDE_CONTROL_AUDIT_PATH` (the same file the authz container writes from TEC T-09; the control container gains a read-only mount on the shared volume).
  - Reads the file LIFO (most recent first) for efficiency at the M4 event count; per `design.md` this is `tac | head` equivalent in Python and is replaced by a SQL lookup in AUD.
  - Returns the parsed JSON object whose `event_id` matches, or `None`.
- Extend `services/control/app/policy/routes.py` with `GET /policies/decisions/{event_id}`:
  - Returns 404 if `find_event` returns `None` (OPE-6: "The decision-trace endpoint returns 404 for an `event_id` that does not exist").
  - For `outcome="minted"`, returns `DenialTrace(event_id, outcome="minted", policy_reason=None, input_snapshot=None)` (OPE-6: "for `outcome="minted"` events, indicates that no denial trace exists").
  - For `outcome="denied"`, returns `DenialTrace(event_id, outcome="denied", policy_reason=event["policy_reason"], input_snapshot=InputSnapshot(subject, actor, scope=event["scope_requested"], audience, existing_chain))`.
  - **The response body must not contain `subject_claims` or `actor_claims` fields under any code path** (OPE-6 fifth acceptance criterion: "Neither endpoint exposes the contents of `subject_claims` or `actor_claims` fields that are not already present in the audit event per `CONTRACT.md` §9"). Encode this as a Pydantic `InputSnapshot` model with exactly five fields (`subject`, `actor`, `scope`, `audience`, `existing_chain`) and `model_config = ConfigDict(extra="forbid")` so a future code change cannot accidentally widen the shape.
- The route accepts only `GET`; no `@router.post`/`@router.put`/`@router.delete` decoration on the same path (OPE-6: "both reject any method other than `GET`").

**Verified when:** With the topology brought up and at least one denied exchange recorded in the audit file, `curl -fsSL http://localhost:8090/policies/decisions/$EVENT_ID | jq -e '.policy_reason != null and .input_snapshot.subject_claims == null and .input_snapshot.actor_claims == null'` exits zero. `curl -o /dev/null -w '%{http_code}' http://localhost:8090/policies/decisions/does-not-exist` returns `404`. `curl -X POST http://localhost:8090/policies/decisions/x` returns `405`. A grep `grep -n 'subject_claims\|actor_claims' services/control/app/policy/` shows no exposure of either field in any response model.

---

## T-12: [ ] Smoke harness — OPE block

**Satisfies:** OPE-8 (the slice's end-to-end requirement), and all OPE-1 through OPE-7 capabilities exercised end-to-end

- Append the OPE block to `scripts/smoke.sh` between markers `#--- OPE block ---` and `#--- end OPE block ---`, per `design.md` "Smoke harness — OPE block". The block must be additive — prior TEC/SWI/VSA blocks remain unchanged and continue to pass (OPE-8 fourth acceptance criterion).
- The block performs, in order:
  1. Mint a user JWT for `alice@example.com` via `docker compose run --rm demo-human python -m demo_human --email alice@example.com`.
  2. Drive the agent with a syntactically-valid-but-not-permitted scope `BONAFIDE_SCOPE="calendar:write:alice@example.com"`. The exchange must return HTTP 400 with `error=access_denied` and `error_description="agent_not_registered_for_scope"` per OPE-8 second acceptance criterion. Assert both with `grep -q`.
  3. Recover the `event_id` from authz's audit log (the OPE slice still uses TEC's file-backed audit emitter; AUD swaps it to HTTP later). The smoke executes `EVENT_ID=$(docker compose exec -T authz tail -n 50 "$BONAFIDE_AUTHZ_AUDIT_PATH" | jq -rs 'map(select(.outcome == "denied" and .policy_reason == "agent_not_registered_for_scope")) | last | .event_id')`. **No change to the demo-agent CLI and no change to `CONTRACT.md` §7** — `event_id` is not on the wire because it does not need to be: callers needing the event_id read the audit log via the control plane (today, the file; after AUD, the SQL row).
  4. Query `GET /policies/decisions/$EVENT_ID` on the control plane and assert with `jq -e`:
     - `.policy_reason == "agent_not_registered_for_scope"` (OPE-8 third acceptance criterion: `policy_reason` equal to the same `reason` string).
     - `.input_snapshot.scope == "calendar:write:alice@example.com"`.
     - `.input_snapshot.existing_chain == []` (OPE-2: empty when no `act` is present).
     - `has("subject_claims") | not` and `has("actor_claims") | not` on the response body (OPE-6 privacy criterion).
  5. Run the `TestChainDepthDenial` unit test from T-05 inside the authz container: `docker compose exec -T authz go test ./internal/policy -run TestChainDepthDenial -v` per `design.md` "Smoke harness — OPE block" (the depth-cap path cannot yet be exercised at the wire because no slice yet produces a depth-2 subject_token; SAN owns that).
  6. Re-run the prior allowed path: `docker compose run --rm demo-agent python -m demo_agent --user-jwt "$USER_JWT"` and assert `.events | length > 0` to prove the slice did not regress TEC/SWI/VSA happy-path behaviour (OPE-8 fourth acceptance criterion).

**Verified when:** `./scripts/smoke.sh` exits zero against a freshly brought-up topology that has the OPE slice applied. The output contains the lines `[smoke:OPE] forbidden scope must be denied with the Rego reason...`, `[smoke:OPE] depth-cap denial path...`, `[smoke:OPE] verified scope is still allowed...`, and `[smoke:OPE] OK`. `grep -c 'OPE block' scripts/smoke.sh` returns 2 (start + end markers) confirming the block boundaries are explicit for future-slice append-only edits. All prior TEC/SWI/VSA blocks continue to pass.

---

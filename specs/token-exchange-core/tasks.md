# token-exchange-core: Tasks

Tasks are ordered. Each must be complete and verifiable before the next begins. If a task surfaces a spec problem, stop — update the relevant spec, re-review, then continue. One commit per task.

Status: `[ ]` todo · `[x]` done · `[~]` in progress

---

## T-01: [x] Bootstrap repo skeleton and gitignore

**Satisfies:** scaffolding for all of TEC-1 through TEC-11

- Create the directory tree from `design.md` "Repo structure": `services/authz/`, `services/control/` (empty placeholder for AUD), `sdks/agent-py/`, `sdks/resource-py/`, `apps/demo-human/`, `apps/demo-agent/`, `apps/demo-calendar/`, `deploy/authz/`, `deploy/postgres/`, `deploy/vault/`, `deploy/spire-stub/`, `scripts/`.
- Add empty `.gitkeep` files where needed so empty directories are tracked.
- Add `.gitignore` entries for `deploy/authz/signing.key`, `deploy/spire-stub/*.pem`, audit log paths, Go build outputs (`authz`, `*.test`), Python `__pycache__`, `.venv`, `*.egg-info`, per `CLAUDE.md` "Delete compiled binaries after build/test".
- Add per-language `README` stubs only if absolutely necessary for build tooling to function; do not author documentation.

**Verified when:** `git ls-files` shows the directory tree from `design.md` "Repo structure" present (every directory listed in the tree exists and is tracked by git), and a fresh `git status` after a build/test cycle of any package produces no untracked binaries.

---

## T-02: [x] Define Go module and pin Go data-plane dependencies

**Satisfies:** TEC-1, TEC-3, TEC-4

- Create `services/authz/go.mod` with module path `bonafide.local/services/authz` (a literal placeholder owned by the project; replaceable in one commit if a GitHub-style owner is chosen later) and Go directive at the latest stable version (≥ 1.23) per `CLAUDE.md` "Stack pins".
- Add the dependency set fixed in `design.md` "Go data plane": `github.com/zitadel/oidc/v3`, `github.com/go-jose/go-jose/v4`, `github.com/google/uuid`, `github.com/go-chi/chi/v5`, `github.com/stretchr/testify`. No additional dependencies.
- Place a `cmd/authz/main.go` that compiles to nothing more than a `func main() { /* TODO: wire in T-13 */ }` so `go build ./...` succeeds.

**Verified when:** `go build ./...` inside `services/authz` exits zero, and `go list -m -f '{{.Indirect}}' github.com/zitadel/oidc/v3` prints `false` (confirming the dependency is direct, not transitive). The resulting binary is deleted after the build per `CLAUDE.md` "Delete compiled binaries after build/test". *Verification command amended from the original `go mod why` after T-02 surfaced a build-tag traversal limitation — see `agent-notes.md` 2026-06-03.*

---

## T-03: [x] Define `exchange.Act`, `TaskTokenClaims`, `UserJWTClaims`, `ActorTokenClaims`, `PolicyInput`, `PolicyDecision`

**Satisfies:** TEC-3, TEC-5 (input contract), CONTRACT.md §§4, 5, 6.1

- Create `services/authz/internal/exchange/types.go` containing the type declarations from `design.md` "Key types": `TaskTokenClaims`, `Act` (recursive `Sub`/`Act` pointer per CONTRACT.md §6.1), `UserJWTClaims`, `ActorTokenClaims`, `PolicyInput`, `PolicyDecision`.
- JSON struct tags match the field names in CONTRACT.md §§4–5 byte-for-byte (`iss`, `sub`, `aud`, `iat`, `exp`, `jti`, `scope`, `act`, `email`, `client_id`).
- `Act.Act` is `*Act` with `omitempty` so absent inner act serialises as no `act` field (CONTRACT.md §6.1 rule 4).
- No method definitions yet; pure type declarations.

**Verified when:** `go vet ./internal/exchange/...` exits zero, and a one-shot JSON marshalling test that round-trips an `Act{Sub: "x"}` (no nested `act`) produces literally `{"sub":"x"}` with no `act` key — matching CONTRACT.md §6.1 example for "no prior `act`".

---

## T-04: [x] Implement `BuildAct`, `cloneAct`, `ChainDepth`, `FlattenChain` (the canonical nesting function)

**Satisfies:** TEC-3, CONTRACT.md §6.1, and the safety constraint **"The `act` chain in minted tokens must always nest, never overwrite. A test asserting this against CONTRACT.md §6 ships in Slice 1 and is never deleted."**

- Create `services/authz/internal/exchange/act_chain.go` with the four functions defined in `design.md` "The act-chain builder":
  - `BuildAct(currentActor string, subjectAct *Act) *Act` — sets `Sub = currentActor`, sets `Act = cloneAct(subjectAct)`; never reads or mutates a subject identity.
  - `cloneAct(a *Act) *Act` — recursive defensive copy; returns `nil` for `nil` input.
  - `ChainDepth(subjectAct *Act) int` — counts the new mint hop as 1 plus the length of the subject chain (for OPE-5's cap).
  - `FlattenChain(act *Act) []string` — returns current-actor-first slice (for audit event `resulting_chain`, CONTRACT.md §9).
- Add the file-level comment that names this as the most important function in the codebase and references CONTRACT.md §6.1 and `CLAUDE.md`.
- Do not write the tests in this task; T-05 owns them.

**Verified when:** `go build ./internal/exchange/...` exits zero, and `grep -n 'func BuildAct\|func ChainDepth\|func FlattenChain' services/authz/internal/exchange/act_chain.go` returns three exported functions, while `grep -n 'func cloneAct' services/authz/internal/exchange/act_chain.go` returns the one unexported helper.

---

## T-05: [x] Table-driven tests for `act_chain.go` against CONTRACT.md §6.1

**Satisfies:** TEC-3 (the test acceptance criterion), the safety constraint **"a test asserting this against CONTRACT.md §6 ships in Slice 1 and is never deleted"**, CONTRACT.md §§6.1, 6.3

- Create `services/authz/internal/exchange/act_chain_test.go` with a single `TestBuildAct` table-driven test covering every case in `design.md` "Test plan for `act_chain_test.go`":
  1. `subjectAct == nil`, `currentActor == "spiffe://bonafide.local/agent/planner"` → result `{Sub: "planner"}` with `Act == nil` (the first-hop case for TEC).
  2. `subjectAct == &Act{Sub: "planner"}`, `currentActor == "tool"` → `{Sub: "tool", Act: {Sub: "planner"}}` (depth-2; SAN's case, asserted here so it never regresses).
  3. `subjectAct == &Act{Sub: "tool", Act: &Act{Sub: "planner"}}`, `currentActor == "tool2"` → `{Sub: "tool2", Act: {Sub: "tool", Act: {Sub: "planner"}}}` (depth-3; unbounded recursion).
  4. `FlattenChain({Sub: "tool", Act: {Sub: "planner"}})` → `["tool", "planner"]`.
  5. `ChainDepth({Sub: "tool", Act: {Sub: "planner"}})` → `3`.
  6. Defensive-copy: mutate the input `*Act` after `BuildAct` returns; assert the returned value is unaffected (`cloneAct` is real, not aliased).
- An additional named test `TestBuildActImpersonationGuardShape` asserts that `BuildAct` never reads or writes to a `Sub` parameter representing the subject — the function signature accepts `currentActor` only — protecting CONTRACT.md §6.3.

**Verified when:** `go test ./internal/exchange/... -run TestBuildAct -count=1` exits zero, all six table rows pass, and `grep -c '"sub"' services/authz/internal/exchange/act_chain_test.go` confirms inputs are written as Go literals matching the CONTRACT.md §6.1 example shapes character-for-character.

---

## T-06: [x] Implement `keys.Signer` (Ed25519 load + JWKS publication)

**Satisfies:** TEC-3 (signature), TEC-4 (JWKS), CONTRACT.md §§3, 11, and the safety constraint **"All credentials short-lived ... No code path may extend these"** (the signer caps token TTLs implicitly via its `Sign` API)

- Create `services/authz/internal/keys/keys.go` with:
  - `Signer` struct holding the Ed25519 private key and a derived `kid` (first 12 chars of base64url(sha256(public_key)) per `design.md` "JWKS publication").
  - `LoadSigner(path string) (*Signer, error)` that reads the PEM Ed25519 private key, returns a non-nil error if the file is missing/unreadable, and on success prepares the public-key half for JWKS.
  - `Sign(header, claims map[string]any) (string, error)` that signs with `alg=EdDSA` (CONTRACT.md §3) and includes `kid` in the JOSE header.
  - `JWKSDocument() (json.RawMessage, error)` returning a JSON document conforming to CONTRACT.md §11 (Ed25519 keys only).
- On `LoadSigner` failure the caller exits non-zero (wired in T-13) per `design.md` "JWKS publication": "if the signing key file is missing, the server exits non-zero (per the 'fail closed' safety constraint)".

**Verified when:** `go test ./internal/keys/... -count=1` includes a test that calls `LoadSigner` on a generated Ed25519 PEM, confirms `Sign` produces a JWT whose header `alg=="EdDSA"` and whose `kid` matches the `Signer`'s kid, and confirms `JWKSDocument` parses as JSON containing exactly one Ed25519 key whose `kid` matches.

---

## T-07: [x] Implement `trust.IssuerTrust` (YAML-backed actor_token verifier; M1 stub)

**Satisfies:** TEC-1 (actor_token verification), TEC-5 (fail-closed denial on bad actor_token), and the safety constraint **"Fail closed. Missing actor_token, unknown agent, unknown scope grammar, unparseable token, unreachable policy engine, expired anything → deny"**

- Create `services/authz/internal/trust/trust.go` containing **only** the `IssuerTrust` interface: `Verify(actorJWT string) (claims ActorTokenClaims, err error)`. The interface is the long-lived seam; SWI swaps in a SPIRE-backed implementation without touching this file.
- Create `services/authz/internal/trust/static.go` containing the M1 `YAMLTrust` impl (SWI's T-08 deletes this file):
  - Loads `actor-trust.yaml` (shape in `design.md` "Configuration") at startup into a `map[spiffeID]struct{kid, publicKey}` and verifies the actor_token's signature against the public key whose `kid` matches the JOSE header `kid`.
  - Rejects (returns a non-nil error) for: unknown `kid`, signature mismatch, expired `exp`, missing required claims (`iss`, `sub`, `aud`, `iat`, `exp`), `sub` not matching the SPIFFE ID grammar of CONTRACT.md §1, `aud` not equal to the authz issuer URL.
  - No fallback path: if the YAML file is missing at startup, `LoadYAMLTrust` returns an error; the binary will refuse to start (wired in T-13).
- Stub a sample `deploy/authz/actor-trust.yaml` schema document (real keys filled by `scripts/bootstrap.sh` in T-27); the file may contain a single placeholder entry to allow unit-test fixture loading.

**Verified when:** `go test ./internal/trust/... -count=1` includes tests asserting that an actor_token signed by an unknown kid, with a wrong signature, with an expired `exp`, or with a missing `aud` each return a non-nil error and an empty `ActorTokenClaims`. A valid actor_token returns the parsed claims with `Sub` equal to the registered SPIFFE ID.

---

## T-08: [x] Implement `policy.Gate` interface and YAML-map implementation (fail closed)

**Satisfies:** TEC-5, and the safety constraint **"Fail closed ... There is no permissive mode and no fallback that grants access"**

- Create `services/authz/internal/policy/policy.go` with:
  - `Gate` interface: `Decide(input exchange.PolicyInput) exchange.PolicyDecision`.
  - `MapGate` impl that loads `policy.yaml` (shape in `design.md` "Configuration") into a slice of allow entries `{actor, subject_prefix, scope, audience}`.
  - `Decide` returns `Allowed: true, ScopeGrant: input.Scope` only when an allow entry matches all four fields (`actor` equals `input.Actor`, `input.Subject` has prefix `subject_prefix`, `scope` equals `input.Scope`, `audience` equals `input.Audience`).
  - A scope that does not match the grammar in CONTRACT.md §2 (regex from §2 enforced as a precondition) returns `Allowed: false, Reason: "unknown_scope"`.
  - Any unmatched tuple returns `Allowed: false, Reason: "no_matching_allow_entry"`.
  - No permissive default, no wildcard expansion beyond what CONTRACT.md §2 specifies (`*` only inside `<qualifier>`).
- The policy file path is read from `BONAFIDE_AUTHZ_POLICY_PATH`; a missing file is a startup error (fail closed).

**Verified when:** `go test ./internal/policy/... -count=1` includes table-driven tests confirming: (a) the example allow entry from `design.md` "Configuration" returns `Allowed: true` and `ScopeGrant == input.Scope`; (b) a non-matching actor, non-matching audience, or non-matching scope each return `Allowed: false`; (c) a scope failing the §2 grammar regex returns `Reason: "unknown_scope"`; (d) `MapGate` constructed with no entries denies every request.

---

## T-09: [x] Implement `audit.Emitter` interface and file-backed implementation

**Satisfies:** TEC-6, CONTRACT.md §9

- Create `services/authz/internal/audit/audit.go` defining the `Emitter` interface (`Emit(event Event)`) and an `Event` struct whose JSON tags match every field of CONTRACT.md §9 byte-for-byte (`schema_version`, `event_id`, `occurred_at`, `outcome`, `issuer`, `subject`, `actor`, `existing_chain`, `resulting_chain`, `audience`, `scope_requested`, `scope_granted`, `policy_reason`, `token_jti`, `token_exp`).
- Create `services/authz/internal/audit/file.go` implementing a `FileEmitter`:
  - Opens the configured path with `os.OpenFile(path, O_APPEND|O_CREATE|O_WRONLY, 0o600)` per `design.md` "File-backed audit emitter".
  - Owns a buffered channel (size 256) and a single drain goroutine that writes one NDJSON line per event.
  - A full channel blocks the producer for up to 100 ms; after that the event is dropped and an ERROR-level slog line `event=audit_buffer_dropped` is emitted. The mint path still returns 200 (TEC-6 acceptance criterion: "The mint path of the exchange handler does not block on audit emission").
  - `Close()` drains the channel and closes the file.
- `schema_version` is hard-coded to `"1"` (CONTRACT.md §9).

**Verified when:** `go test ./internal/audit/... -count=1` includes tests asserting: (a) a minted event written through `Emit` appears as a single NDJSON line in the configured file whose JSON keys exactly match the CONTRACT.md §9 set; (b) a denied event has `scope_granted: null`, `token_jti: null`, `token_exp: null`; (c) restarting the emitter (open → write → close → reopen → read) returns previously-written events (TEC-6 durability criterion).

---

## T-10: [x] Implement `httputil` router and OAuth error helper

**Satisfies:** TEC-1 (HTTP 400 with JSON error body per CONTRACT.md §7), and the safety constraint that 5xx is reserved for genuine server errors

- Create `services/authz/internal/httputil/router.go` with:
  - A `NewRouter()` function returning a `chi.Router` with three routes registered: `GET /.well-known/openid-configuration`, `GET /.well-known/jwks.json`, `POST /token`, plus `GET /healthz`. Handlers are wired in T-13; this task delivers the router shape only.
  - `WriteOAuthError(w http.ResponseWriter, code string, description string)` writing HTTP 400 with JSON body `{"error": "<code>", "error_description": "<description>"}` per RFC 6749 §5.2 / CONTRACT.md §7. The set of legal `code` values is constrained to `{"invalid_request", "invalid_grant", "invalid_scope", "access_denied"}` (CONTRACT.md §7).
  - `WriteJSON(w, status, body)` and `WriteServerError(w, err)` (the only path that writes 5xx).

**Verified when:** `go test ./internal/httputil/... -count=1` includes tests asserting `WriteOAuthError` produces status 400, content-type `application/json`, and a body that JSON-decodes to exactly the two-field RFC 6749 shape; and a test that the router exposes the four routes above (probe each with `httptest.NewRecorder()` and assert non-404 status for the registered paths).

---

## T-11: [x] Implement RFC 8693 `POST /token` handler

**Satisfies:** TEC-1, TEC-3, TEC-5, CONTRACT.md §§7, 8, and the safety constraints **"Fail closed"**, **"All credentials short-lived"** (5-min cap on task token), **"The impersonation guard is unconditional"** (sub is never mutated)

- Create `services/authz/internal/exchange/handler.go` exposing `Handler(policy.Gate, trust.IssuerTrust, keys.Signer, audit.Emitter, settings) http.HandlerFunc` implementing exactly the flow listed in `design.md` "Exchange handler flow":
  1. Parse form-encoded request; verify `grant_type == "urn:ietf:params:oauth:grant-type:token-exchange"`. Missing/bad grant_type → 400 `invalid_request`.
  2. Verify every required parameter from CONTRACT.md §7 is present (`subject_token`, `subject_token_type`, `actor_token`, `actor_token_type`, `requested_token_type`, `audience`, `scope`). Any missing → 400 `invalid_request`.
  3. Verify `requested_token_type == "urn:ietf:params:oauth:token-type:jwt"` per CONTRACT.md §§7, 8. Mismatch → 400 `invalid_request`.
  4. Verify `subject_token_type == "urn:ietf:params:oauth:token-type:jwt"` and `actor_token_type == "urn:ietf:params:oauth:token-type:jwt"`. Mismatch → 400 `invalid_request`.
  5. Decode `subject_token` and verify its signature against `keys.Signer`'s key (the authz signed it via the demo-human CLI per `design.md` "JWT signing key"); validate `iss`, `aud`, `exp`. Any failure → 400 `invalid_grant` with a precise `error_description`. The enumerated failure modes that map to `invalid_grant` are: signature mismatch, expired `exp` (strict, no leeway per `DESIGN.md` §4), `iss` mismatch, `aud` mismatch, and unparseable JWT. Missing required claims (no `iss`/`aud`/`exp`/`sub`/`iat`) also map to `invalid_grant`.
  6. Reject subject_token if it carries an `act` claim per CONTRACT.md §4: 400 `invalid_request`, `error_description="subject_token must not carry act on first hop"` (TEC-2 acceptance criterion).
  7. Decode `actor_token` via `trust.IssuerTrust.Verify`; failure (missing, malformed, expired, unknown kid, signature mismatch, audience mismatch) → 400 `invalid_request` (TEC-5).
  8. Call `policy.Gate.Decide(PolicyInput{...})`. If `!Allowed`, emit a denied audit event with `policy_reason = decision.Reason` per CONTRACT.md §9, then return 400 with the mapped error code: `unknown_scope` → `invalid_scope`; anything else → `access_denied` (TEC-5).
  9. Compute `existingChain := FlattenChain(subject_claims.Act)` (an empty slice in TEC, since subject_token must carry no `act`).
  10. `taskAct := BuildAct(currentActor=actor_claims.Sub, subjectAct=subject_claims.Act)`.
  11. Construct `TaskTokenClaims` with `Iss = settings.Issuer`, `Sub = subject_claims.Sub` (unchanged; CONTRACT.md §5 / §6.3), `Aud = request.audience`, `Iat = now`, `Exp = now + min(decision.TTL, settings.TaskTokenTTLSeconds, 300)`, `Jti = uuid.NewString()`, `Scope = decision.ScopeGrant`, `Act = taskAct`, `ClientID = actor_claims.Sub`.
  12. Sign with `keys.Signer.Sign`.
  13. Emit a minted audit event whose `event_id == jti`, `resulting_chain == FlattenChain(taskAct)`, `existing_chain == existingChain`, `token_exp == time.Unix(exp).UTC().Format(time.RFC3339Nano)` per CONTRACT.md §9.
  14. Return HTTP 200 with the RFC 8693 §2.2 body: `access_token`, `issued_token_type="urn:ietf:params:oauth:token-type:jwt"`, `token_type="Bearer"`, `expires_in = exp - iat`, `scope = decision.ScopeGrant`. No `refresh_token` (CONTRACT.md §8).
- The handler must never return 5xx for a malformed request — only for genuine server faults (signing failure, audit goroutine crash, panic). Use `httputil.WriteOAuthError` for all 400s and `httputil.WriteServerError` for 5xx.
- Audit emission is non-blocking and never alters the HTTP response (TEC-6 acceptance criterion).

**Verified when:** `go test ./internal/exchange/... -run TestHandler -count=1` includes table-driven cases for every required-parameter omission, every malformed-token shape, the `act`-carrying-subject_token case, every policy-deny case, and the happy path. Every error case asserts a JSON body with exactly two keys (`error`, `error_description`) and status 400; the happy path asserts an RFC 8693 §2.2 body with `expires_in <= 300`, `issued_token_type == "urn:ietf:params:oauth:token-type:jwt"`, no `refresh_token` key, and a `scope` matching CONTRACT.md §2.

---

## T-12: [ ] Mint-side impersonation guard test — `sub` is never mutated

**Satisfies:** TEC-3 (the mint-output invariant), CONTRACT.md §6.3, and the safety constraint **"The impersonation guard is unconditional ... Authorization decisions use top-level `sub` + outermost `act.sub` only."** This task is standalone per the tasks-writer rule that safety-critical paths are not bundled.

- Add a dedicated, named test to `services/authz/internal/exchange/handler_test.go` called `TestHandler_MintedSubEqualsSubjectSub`:
  - Drive the handler with a well-formed `subject_token` whose `sub` is `spiffe://bonafide.local/human/alice@example.com` and a valid `actor_token`. Run the happy-path exchange.
  - Decode the minted `access_token` and assert `minted.sub == subject_token.sub` as a Go string equality, byte-for-byte. There is **no transformation, normalisation, or lowercasing** of `sub` between input and output.
  - Repeat the assertion for at least three distinct subject SPIFFE IDs (e.g. `human/alice@example.com`, `human/bob@example.com`, `human/charlie+test@example.com`) to guard against accidental coupling to a single literal.
  - Run a negative case: drive the handler with `actor_token.sub == subject_token.sub` (a workload presenting an actor token whose subject equals the human's SPIFFE ID — the mint-time impersonation attempt). The mint must still succeed (since the handler does not inspect this relationship — only the resource SDK does per TEC-9). Decode the minted token and assert `minted.sub == subject_token.sub` (the mint did not adopt the actor's identity); the resource SDK is the layer that rejects this pattern at consume time and is tested in T-21.
- This test is independent of `TestBuildActImpersonationGuardShape` (T-05): T-05 asserts the function signature cannot read a subject identity; this task asserts the end-to-end mint output preserves it.

**Verified when:** `go test ./internal/exchange/... -run TestHandler_MintedSubEqualsSubjectSub -count=1` exits zero, and `grep -n 'TestHandler_MintedSubEqualsSubjectSub' services/authz/internal/exchange/handler_test.go` returns exactly one match. The test's assertion uses Go string equality on the decoded `sub` claim, not a regex or prefix check. A second grep confirms the test exercises ≥3 distinct subject SPIFFE IDs.

---

## T-13: [ ] Implement `internal/config` and wire `cmd/authz/main.go`

**Satisfies:** TEC-1, TEC-4, and the fail-closed startup safety constraint (binary exits non-zero on missing key, missing trust file, missing policy file)

- Create `services/authz/internal/config/config.go` reading the environment variables from `design.md` "Configuration" → `services/authz (Go)`: `BONAFIDE_AUTHZ_LISTEN`, `BONAFIDE_AUTHZ_ISSUER`, `BONAFIDE_AUTHZ_SIGNING_KEY_PATH`, `BONAFIDE_AUTHZ_ACTOR_TRUST_PATH`, `BONAFIDE_AUTHZ_POLICY_PATH`, `BONAFIDE_AUTHZ_AUDIT_PATH`, `BONAFIDE_AUTHZ_TASK_TOKEN_TTL_SECONDS` (default 300; reject values > 300 at load time per `CLAUDE.md` "No code path may extend these").
- Wire `cmd/authz/main.go` to: load config, construct `keys.Signer`, `trust.YAMLTrust`, `policy.MapGate`, `audit.FileEmitter`; mount the exchange handler at `POST /token`; mount the JWKS handler at `GET /.well-known/jwks.json`; mount the OIDC discovery handler at `GET /.well-known/openid-configuration` (advertise `jwks_uri = ${ISSUER}/.well-known/jwks.json` per CONTRACT.md §11 / TEC-4); mount `GET /healthz`; listen on `BONAFIDE_AUTHZ_LISTEN`.
- On any startup error (missing key file, missing trust file, missing policy file, audit path unwritable, TTL > 300) exit non-zero with a structured slog line before listening — fail closed.

**Verified when:** Running the binary with `BONAFIDE_AUTHZ_SIGNING_KEY_PATH` pointing at a non-existent file exits non-zero within 1 second. Running it with all required env vars and valid files binds the listener and a probe `curl http://127.0.0.1:8080/healthz` returns 200, `curl http://127.0.0.1:8080/.well-known/openid-configuration` returns 200 with a body containing the `jwks_uri` advertising `/.well-known/jwks.json`, and `curl http://127.0.0.1:8080/.well-known/jwks.json` returns 200 with a JSON body containing only Ed25519 keys. The compiled binary is deleted immediately after the test.

---

## T-14: [ ] Implement `apps/demo-human` CLI (user JWT minting)

**Satisfies:** TEC-2, CONTRACT.md §4, and the safety constraint **"All credentials short-lived"** (15-min ceiling)

- Create `apps/demo-human/pyproject.toml` declaring dependencies `typer`, `python-jose[cryptography]`, `pydantic-settings`. Use `uv` per `design.md` "Stack (locked for this slice)".
- Create `apps/demo-human/demo_human/__main__.py` with a `typer` CLI accepting `--email <addr>` (required) and `--ttl <seconds>` (default 900, hard cap 900 per `design.md` "Configuration" → `apps/demo-human` and `CLAUDE.md` "Safety constraints").
- Load the Ed25519 PEM at `BONAFIDE_DEMO_HUMAN_SIGNING_KEY_PATH` (same key the authz signs with per `design.md` "JWT signing key").
- Mint a JWT with the CONTRACT.md §4 claim set: `iss = BONAFIDE_AUTHZ_ISSUER`, `sub = spiffe://bonafide.local/human/<email>`, `aud = BONAFIDE_AUTHZ_ISSUER`, `iat`, `exp = iat + min(ttl, 900)`, `jti = uuid4`, optional `email = <email>`. Sign with `alg=EdDSA`. Set the JOSE header `kid` to the authz signer's kid (so the authz JWKS resolves it).
- The CLI must refuse if asked to include an `act` claim (TEC-2 acceptance criterion: the CLI never emits a JWT carrying `act`). There is no `--act` flag.
- Print the JWT to stdout, one line, no trailing newline beyond a single `\n`.

**Verified when:** `python -m demo_human --email alice@example.com` produces a JWT whose decoded claims satisfy: `iss == "https://authz.bonafide.local"` (the canonical issuer per `CONTRACT.md` §4 and `requirements.md`; `design.md` excerpts that show an `http://...:8080` form are a documentation drift to be reconciled before this task runs — the task uses the CONTRACT.md form), `sub == "spiffe://bonafide.local/human/alice@example.com"`, `aud == iss`, `exp - iat <= 900`, header `alg == "EdDSA"`, and the token verifies against the JWKS published by the authz server at `/.well-known/jwks.json`. A grep of the source confirms there is no code path that adds an `act` claim.

---

## T-15: [ ] Implement `sdks/agent-py` package skeleton + `identity.sign_actor_token`

**Satisfies:** TEC-10 (actor_token minting portion), and **"No static long-lived secrets ... The agent SDK never reads a credential from disk"** — clarified by `design.md`: the per-workload Ed25519 key on disk is the M1 stub for SPIRE; the SDK reads it once at construction time and signs in memory; no Vault/exchange credentials are read from disk

- Create `sdks/agent-py/pyproject.toml` with dependencies `python-jose[cryptography]`, `httpx`, `hvac` (Vault client), `pydantic`. Package name `bonafide_agent`.
- Create `sdks/agent-py/bonafide_agent/__init__.py` exporting `BonafideAgent`, `TaskToken`, errors.
- Create `sdks/agent-py/bonafide_agent/identity.py` implementing `sign_actor_token(key_path, kid, spiffe_id, issuer_audience) -> str` exactly per `design.md` "`identity.py`":
  - Reads Ed25519 PEM from `key_path` once and signs `{iss, sub, aud, iat, exp, jti}` with `alg=EdDSA`, `kid` in JOSE header.
  - `iss == sub == spiffe_id` (agent is its own issuer in M1).
  - `aud == issuer_audience` (so the authz server's audience check passes).
  - `exp - iat == 60` (1-minute actor_token TTL per `design.md` "`identity.py`").
- Create `sdks/agent-py/bonafide_agent/errors.py` with `BonafideAgentError`, `ExchangeError`, `VaultReadError`, `ResourceCallError`.

**Verified when:** `pytest sdks/agent-py` includes a test that calls `sign_actor_token` against a generated key, decodes the result, and asserts every claim above; and a test that verifies the resulting JWT against the corresponding public key (cryptography is real, not stubbed). No code path in `bonafide_agent/` calls `open(path, ...)` for any credential other than the per-workload key.

---

## T-16: [ ] Implement `BonafideAgent.exchange`

**Satisfies:** TEC-10 (exchange pipeline portion), CONTRACT.md §§7, 8, and the safety constraint **"Fail closed ... A failure at any pipeline step (exchange denial, Vault read failure, resource non-2xx) causes the SDK to abort the request and surface the failure; no permissive fallback exists"**

- Create `sdks/agent-py/bonafide_agent/client.py` implementing `BonafideAgent.exchange(subject_token, scope, audience) -> TaskToken` per `design.md` "The `BonafideAgent` client":
  - Calls `sign_actor_token` to mint a fresh actor_token (1-min TTL) with `aud = authz_token_url`.
  - POSTs form-encoded to `BONAFIDE_AUTHZ_TOKEN_URL` with the exact parameter set listed in CONTRACT.md §7: `grant_type`, `subject_token`, `subject_token_type`, `actor_token`, `actor_token_type`, `requested_token_type=urn:ietf:params:oauth:token-type:jwt`, `audience`, `scope`.
  - On any non-200 response or a JSON body missing `access_token`/`expires_in`/`scope`, raises `ExchangeError` with the body's `error`/`error_description` if present.
  - Returns `TaskToken(access_token, expires_at = now + expires_in, scope)`.
  - The SDK never caches a `TaskToken` past `expires_at` (TEC-10 acceptance criterion).

**Verified when:** `pytest sdks/agent-py` includes a test that uses a mock HTTP transport (httpx `MockTransport`) to assert: (a) the POST body contains every required parameter from CONTRACT.md §7 with the correct values; (b) a 200 JSON body matching CONTRACT.md §8 produces a `TaskToken` with the expected `expires_at`; (c) a 400 response with `{"error": "access_denied", "error_description": "..."}` raises `ExchangeError`; (d) a 200 response missing `access_token` raises `ExchangeError`.

---

## T-17: [ ] Implement `BonafideAgent.fetch_connection` (Vault KV stub read)

**Satisfies:** TEC-7, and the safety constraint **"No static long-lived secrets in source, env files, or container images outside SPIRE and Vault"** (Vault holds the only static value; the SDK reads it at runtime)

- Extend `sdks/agent-py/bonafide_agent/client.py` with `fetch_connection() -> str` per `design.md`:
  - Constructs an `hvac.Client(url=vault_addr, token=vault_token)`. The Vault token is `BONAFIDE_VAULT_TOKEN` (`devroot` in M1; replaced by SPIFFE auth in VSA).
  - Reads `BONAFIDE_VAULT_KV_PATH` (KV v2, default `secret/data/calendar/connection`).
  - Returns the `connection` field of the KV value.
  - Any read failure raises `VaultReadError`; there is no fallback (TEC-7 acceptance criterion: "A failure to read the configured KV path causes the agent SDK to abort the request before calling the calendar resource; no fallback credential path exists").

**Verified when:** `pytest sdks/agent-py` includes a test using a mocked `hvac.Client` that asserts: (a) a successful KV read returns the stored connection string; (b) a 404 from Vault raises `VaultReadError`; (c) an `hvac.exceptions.Forbidden` raises `VaultReadError`; (d) no fallback credential path exists (grep `sdks/agent-py/` produces no `try ... except: return ...` that swallows Vault errors).

---

## T-18: [ ] Implement `BonafideAgent.call` (resource invocation)

**Satisfies:** TEC-10 (resource call portion), and the safety constraint **"The SDK never re-uses any token, lease, or credential past its `exp`; expiry is enforced strictly with no leeway"**

- Extend `sdks/agent-py/bonafide_agent/client.py` with `call(url, token, connection) -> httpx.Response` per `design.md`:
  - Before invoking, assert `time.time() < token.expires_at`; if not, raise `ExchangeError("task token expired")`. No leeway.
  - Sends an HTTP GET to `url` with `Authorization: Bearer <token.access_token>` and (if `connection` is not None) `X-Bonafide-Connection: <connection>` per `design.md` "`apps/demo-calendar`".
  - Raises `ResourceCallError` on any non-2xx response, capturing the status and (best-effort) body.

**Verified when:** `pytest sdks/agent-py` includes a test asserting: (a) a 200 response is returned unchanged; (b) a `TaskToken` whose `expires_at` is in the past raises `ExchangeError` without sending an HTTP request; (c) a 401 response raises `ResourceCallError`. A grep for `leeway` in `sdks/agent-py/` produces no matches that would permit reuse past `exp`.

---

## T-19: [ ] Implement `sdks/resource-py` package skeleton, `chain.ActorChain`, `errors`

**Satisfies:** TEC-9 (types), CONTRACT.md §6.2

- Create `sdks/resource-py/pyproject.toml` with dependencies `fastapi`, `python-jose[cryptography]`, `httpx`, `pydantic`. Package name `bonafide_resource`.
- Create `sdks/resource-py/bonafide_resource/__init__.py` exporting `TokenValidator`, `ActorChain`, errors.
- Create `sdks/resource-py/bonafide_resource/chain.py` defining the `ActorChain` dataclass exactly per `design.md` "Middleware": `subject: str`, `current_actor: str`, `prior_actors: tuple[str, ...]`, and `all_actors: tuple[str, ...]` property (current_actor first, per CONTRACT.md §6.2).
- Create `sdks/resource-py/bonafide_resource/errors.py` with `TokenValidationError`, `ImpersonationGuardError`.

**Verified when:** `pytest sdks/resource-py` includes a test that builds an `ActorChain` and asserts `all_actors == (current_actor, *prior_actors)`. `grep -n 'subject\|current_actor' sdks/resource-py/bonafide_resource/chain.py` confirms both attributes exist on the dataclass.

---

## T-20: [ ] Implement `JWKSCache` with rate-limited refresh

**Satisfies:** TEC-9 (JWKS validation), TEC-4 (consumer side), and the open decision **"Resource SDK error handling for stale JWKS"** from DESIGN.md §6

- Create `sdks/resource-py/bonafide_resource/jwks.py` implementing `JWKSCache` exactly per `design.md` "JWKS cache":
  - `REFRESH_INTERVAL = 60.0`, `FETCH_TIMEOUT = 3.0`.
  - On `get_key(kid)`: return cached key; on miss, acquire the refresh lock; re-check; if `monotonic() - last_fetch < REFRESH_INTERVAL`, raise `TokenValidationError("unknown kid; refresh rate-limited")`; otherwise fetch the JWKS, update cache and `last_fetch`; raise if `kid` is still absent.
  - The fetch uses `httpx.AsyncClient` with `FETCH_TIMEOUT`. Parses the response as JWKS and stores keys by their `kid`.

**Verified when:** `pytest sdks/resource-py` includes tests using a mocked async HTTP transport that assert: (a) a known kid is returned without a fetch; (b) an unknown kid triggers a fetch and is then returned; (c) two consecutive unknown-kid requests within 60 s result in exactly one network fetch; (d) a 60+ second gap permits a second fetch.

---

## T-21: [ ] Implement `TokenValidator` middleware including impersonation guard

**Satisfies:** TEC-9, CONTRACT.md §§3, 6, 6.2, 6.3, and the safety constraints **"The impersonation guard is unconditional"** and **"Authorization decisions use top-level `sub` + outermost `act.sub` only"**

- Create `sdks/resource-py/bonafide_resource/middleware.py` implementing `TokenValidator` exactly per `design.md` "Middleware":
  - Extract the bearer token from `Authorization`. Missing → HTTPException 401 `"missing bearer token"`.
  - Read the unverified JOSE header. If `alg in (None, "none")`, reject with HTTPException 401 `"alg=none rejected"` — regardless of JWKS contents (CONTRACT.md §3).
  - Resolve the public key via `JWKSCache.get_key(header["kid"])`.
  - Verify the JWT with `python-jose`: `algorithms=["EdDSA"]`, `issuer=self._issuer`, `audience=self._audience`, `leeway=0` (strict `exp` per DESIGN.md §4 / TEC-9 acceptance criterion: "The middleware applies `exp` strictly with no leeway").
  - Call `_extract_chain(claims)` to construct `ActorChain`:
    - If `act is None` or `"sub" not in act`, raise 401 `"task token missing act claim"`.
    - Impersonation guard (CONTRACT.md §6.3): if `claims["sub"]` does not start with `spiffe://bonafide.local/human/`, log an `impersonation_guard_triggered` event (slog/structlog/print depending on app config) and raise 401 with `impersonation_guard_triggered` in the message.
    - Walk the inner `act` chain; any inner entry missing `"sub"` raises 401 `"impersonation guard: malformed inner act"` and logs `impersonation_guard_triggered`.
  - On success, set `request.state.actor_chain = chain` and return it.
- The middleware is registered as a FastAPI dependency; the calendar app declares it on its protected route (T-23). Prior actors are exposed for logging/response only — never as authorization input (CONTRACT.md §6.2 enforced by the calendar handler in T-23, not here).

**Verified when:** `pytest sdks/resource-py` includes tests asserting: (a) a valid task token with `sub == spiffe://bonafide.local/human/alice@example.com` and `act.sub == .../agent/planner` populates `request.state.actor_chain` with `subject=alice`, `current_actor=planner`, `prior_actors=()`; (b) a token whose `alg` header is `none` is rejected with 401 even when the JWKS would resolve a key; (c) a token whose `sub` is `spiffe://bonafide.local/agent/planner` (not a human) is rejected 401 with `impersonation_guard_triggered`; (d) a token whose signature does not verify, whose `iss` differs, whose `aud` differs, or whose `exp` is in the past is rejected 401; (e) a token with `act.act = {"act": {"sub": "x"}}` (inner entry missing `sub`) is rejected 401 with `impersonation_guard_triggered`; (f) the validator applies `leeway=0` (a token whose `exp` is `now - 1` is rejected).

---

## T-22: [ ] Postgres calendar fixture init script

**Satisfies:** TEC-8 (calendar data source)

- Create `deploy/postgres/init.sql` with exactly the schema and seed data from `design.md` "Postgres calendar fixture":
  - `calendar_events` table with `id`, `owner_email`, `title`, `starts_at`.
  - Two seed rows for `alice@example.com`.
  - A `calendar_reader` Postgres role with password `calendar-dev-password` (dev-only; matches the connection string the Vault KV stub holds in T-26).
  - Grants `CONNECT` on the `calendar` database, `USAGE` on `public`, and `SELECT` on `calendar_events` to `calendar_reader`.
- **MVP scaffolding note (must be present in `init.sql` as a `-- comment`):** the `calendar_reader` static role and its literal password exist only for the TEC slice as documented scaffolding. VSA T-01 deletes the static role; from VSA onwards the connection credentials are issued dynamically by Vault's database secrets engine with a 5-minute TTL. The literal password in the source tree is therefore confined to the TEC slice's git-history window and the `CLAUDE.md` "No static long-lived secrets" guarantee is preserved by VSA's removal step. This caveat must be visible to anyone reading the file.
- **MVP shortcut note:** the calendar fixture and (later) the AUD control-plane schema share a single Postgres database. This is a deliberate MVP shortcut — in production the audit/control plane would have its own DB. Mark this as a TODO comment in `init.sql` so AUD's T-02 has visible context.

**Verified when:** Running the Postgres container with this script mounted at `/docker-entrypoint-initdb.d/init.sql`, the SQL `SELECT count(*) FROM calendar_events WHERE owner_email='alice@example.com'` (executed as `calendar_reader` against the `calendar` database with password `calendar-dev-password`) returns 2.

---

## T-23: [ ] Implement `apps/demo-calendar` FastAPI app

**Satisfies:** TEC-8, CONTRACT.md §6.2, and the safety constraint **"Authorization decisions use top-level `sub` + outermost `act.sub` only. Inner `act` entries are evidence and audit material, never authorization input"**

- Create `apps/demo-calendar/pyproject.toml` depending on `bonafide-resource` (local path), `fastapi`, `uvicorn`, `asyncpg`, `pydantic-settings`.
- Create `apps/demo-calendar/demo_calendar/main.py`:
  - Read config from env per `design.md` "Configuration" → `apps/demo-calendar`.
  - Construct `TokenValidator(issuer=BONAFIDE_AUTHZ_ISSUER, jwks_url=BONAFIDE_AUTHZ_JWKS_URL, audience=BONAFIDE_RESOURCE_AUDIENCE)`.
  - Single protected route `GET /events` declaring `chain = Depends(validator)`.
  - Authorization decision uses only `chain.subject` (the human) and `chain.current_actor` (CONTRACT.md §6.2). Concretely: parse `chain.subject` as `spiffe://bonafide.local/human/<email>`; assert that the validated `scope` claim equals `calendar:read:<email>` for that email. **The scope is exposed by extending `TokenValidator`'s return value to include the validated `scope` string** — the route does not re-decode the JWT (decoding the same token in two places is a security smell; the validator is the single JWT-parsing path). Reject otherwise with 403.
  - Read the DSN from the `X-Bonafide-Connection` header (the Vault KV stub forwarded by the agent per TEC-10).
  - Open a transient `asyncpg` connection using that DSN, `SELECT id, title, starts_at FROM calendar_events WHERE owner_email = $1`, then close the connection.
  - Respond with the JSON body shape from `design.md` "`apps/demo-calendar`": `{acting_for, acted_by, evidence_chain (empty in TEC), events: [...]}`.
- Add `GET /healthz` returning 200.
- The handler does not reference `chain.prior_actors` for any authorization decision (CONTRACT.md §6.2); `evidence_chain` is set from `prior_actors` for response body only.

**Verified when:** `pytest apps/demo-calendar/tests` includes tests asserting: (a) a request with a valid task token + valid `X-Bonafide-Connection` returns 200 with the documented body shape; `acting_for` equals the human SPIFFE ID, `acted_by` equals the planner SPIFFE ID, `evidence_chain == []`; (b) a request without a bearer token returns 401; (c) a request with a token whose scope does not match `calendar:read:<email>` for the subject's email returns 403; (d) a grep of `apps/demo-calendar/demo_calendar/main.py` confirms `prior_actors` is referenced only when assembling the response body, never inside a conditional that affects access (e.g. no `if chain.prior_actors` gate before a `SELECT`).

---

## T-24: [ ] Implement `apps/demo-agent` one-shot driver CLI

**Satisfies:** TEC-10, and the end-to-end pipeline of TEC-11

- Create `apps/demo-agent/pyproject.toml` depending on `bonafide-agent` (local path), `typer`, `pydantic-settings`.
- Create `apps/demo-agent/demo_agent/__main__.py`:
  - `typer` CLI accepting `--user-jwt <jwt>` (required) and `--raw` (optional; on, dumps the full `httpx.Response` status line + headers + body for the impersonation-guard smoke check in T-28).
  - Construct `BonafideAgent` from env per `design.md` "Configuration" → `apps/demo-agent and sdks/agent-py`.
  - Pipeline:
    1. `token = agent.exchange(subject_token=user_jwt, scope=BONAFIDE_SCOPE, audience=BONAFIDE_CALENDAR_URL)`
    2. `connection = agent.fetch_connection()`
    3. `response = agent.call(url=f"{BONAFIDE_CALENDAR_URL}/events", token=token, connection=connection)`
    4. Print `response.json()` (or, with `--raw`, the full status line + body).
  - Any exception aborts and exits non-zero with the error class and message; no retries (TEC-10 acceptance criterion: "no permissive fallback exists").

**Verified when:** Against a brought-up topology (T-27), `python -m demo_agent --user-jwt $(python -m demo_human --email alice@example.com)` exits zero and prints a JSON body where `acting_for == "spiffe://bonafide.local/human/alice@example.com"`, `acted_by == "spiffe://bonafide.local/agent/planner"`, and `events` has at least one entry. A run with a `BONAFIDE_VAULT_KV_PATH` pointing to a non-existent path exits non-zero before any HTTP call to the calendar.

---

## T-25: [ ] Author `deploy/authz/policy.yaml` and the M1 stub for `deploy/authz/actor-trust.yaml`

**Satisfies:** TEC-5 (policy table), TEC-1 (actor trust input)

- Create `deploy/authz/policy.yaml` containing exactly the one allow entry from `design.md` "Configuration":
  - `actor: spiffe://bonafide.local/agent/planner`
  - `subject_prefix: spiffe://bonafide.local/human/`
  - `scope: calendar:read:alice@example.com`
  - `audience: http://calendar.bonafide.local:9000`
- Create `deploy/authz/actor-trust.yaml` as a structured template that the bootstrap script (T-27) fills in with the freshly generated agent public key. The template documents the shape from `design.md` "Configuration" → `actor-trust.yaml` and contains a single placeholder entry for `spiffe://bonafide.local/agent/planner` with `kid=planner-dev-key-1`.

**Verified when:** `cat deploy/authz/policy.yaml` parses as YAML and has exactly one allow entry matching the four field values above; `cat deploy/authz/actor-trust.yaml` parses as YAML with a top-level `trusts:` list containing one entry whose `spiffe_id` is `spiffe://bonafide.local/agent/planner` and `kid` is `planner-dev-key-1`.

---

## T-26: [ ] `deploy/vault/bootstrap.sh` writing the calendar KV stub

**Satisfies:** TEC-7

- Create `deploy/vault/bootstrap.sh` exactly per `design.md` "Vault dev-mode + KV stub":
  - Sets `VAULT_ADDR=http://vault:8200`, `VAULT_TOKEN=devroot`.
  - Idempotently enables KV v2 at `secret/` (`vault secrets enable -path=secret kv-v2 || true`).
  - Writes `vault kv put secret/calendar/connection connection="postgresql://calendar_reader:calendar-dev-password@postgres:5432/calendar"`.
- **MVP scaffolding note (top-of-file `# comment`):** the literal `calendar-dev-password` written into the KV stub matches the literal in `deploy/postgres/init.sql` (T-22) and exists only for the TEC slice. VSA T-06 deletes this KV path entirely and replaces it with a Vault database-secrets-engine lease whose username/password are minted per-request with a 5-minute TTL. The static password is therefore a documented, time-bounded scaffold — not a long-lived credential — and `CLAUDE.md` "No static long-lived secrets" is satisfied by VSA's deletion step.
- Make the script executable.

**Verified when:** Running this script inside the `vault` container (Vault running in dev mode with token `devroot`) exits zero on first invocation and on a second invocation, and `vault kv get -field=connection secret/calendar/connection` returns the documented connection string.

---

## T-27: [ ] `docker-compose.yml` + `scripts/bootstrap.sh`

**Satisfies:** TEC-11 (bring-up portion)

- Create `docker-compose.yml` covering the containers introduced in TEC per DESIGN.md §5: `authz`, `calendar`, `postgres`, `vault`. (The `control` and `agent` containers from DESIGN.md §5 are introduced as follows: `control` is a placeholder stub container running a trivial FastAPI `/healthz` — see note below — and `agent` is run on-demand by the smoke script via `docker compose run --rm demo-agent`, not always-on, per `design.md` "Docker compose".)
- The compose file uses the env-var values and host names listed in `design.md` "Docker compose" and "Configuration".
- Add a minimal placeholder `services/control` Python project with one FastAPI route `GET /healthz` so the `control` container exists (it does no work in TEC; AUD wires it in). This is the stub-package-for-future-slices pattern from the agent definition rules.
- Create `scripts/bootstrap.sh` performing exactly the five steps of `design.md` "Bootstrap script":
  1. Generate `deploy/authz/signing.key` if missing (`openssl genpkey -algorithm Ed25519`).
  2. Generate `deploy/spire-stub/agent-planner.key.pem` + `.pub.pem` if missing.
  3. Run `scripts/_update_actor_trust.py` to update `deploy/authz/actor-trust.yaml` with the agent's freshly generated public key (idempotent).
  4. `docker compose up -d --wait`.
  5. Execute `deploy/vault/bootstrap.sh` inside the `vault` container.
- Create `scripts/_update_actor_trust.py` as a tiny argparse utility that reads a public-key PEM and patches the YAML in place, replacing the placeholder `public_key_pem` value with the real one.
- Make `scripts/bootstrap.sh` idempotent (re-running produces the same end state).

**Verified when:** From a clean checkout, `./scripts/bootstrap.sh` exits zero. `docker compose ps` shows `authz`, `calendar`, `postgres`, `vault`, `control` all `running` or `healthy`. `curl http://localhost:8080/healthz`, `curl http://localhost:9000/healthz`, `curl http://localhost:8200/v1/sys/health` (Vault, dev mode), and a Postgres `pg_isready` all succeed. Running `./scripts/bootstrap.sh` a second time also exits zero and produces no diff in `deploy/authz/actor-trust.yaml`.

---

## T-28: [ ] Smoke harness TEC block + `scripts/_tamper_sub.py`

**Satisfies:** TEC-11 (the slice's end-to-end requirement)

- Create `scripts/_tamper_sub.py` accepting `--token <jwt>` and `--new-sub <spiffe-id>`. It decodes the JWT's payload, replaces `sub` with `--new-sub`, **does not re-sign** (so the signature no longer verifies — the resource SDK's signature check is the primary line of defence; the impersonation-guard log is the secondary), and prints the resulting (broken) JWT. Document in a top-of-file comment that this script forges only as a sanity check against the resource-SDK guard and is not a legitimate code path.
- Create `scripts/smoke.sh` containing the TEC block exactly per `design.md` "Smoke harness (first block — TEC-11)":
  - Mint a user JWT via `docker compose run --rm demo-human python -m demo_human --email alice@example.com`.
  - Drive the agent via `docker compose run --rm demo-agent python -m demo_agent --user-jwt "$USER_JWT"`.
  - Assert with `jq -e` that the response satisfies `.acting_for == "spiffe://bonafide.local/human/alice@example.com"` AND `.acted_by == "spiffe://bonafide.local/agent/planner"` AND `(.events | length) > 0` per CONTRACT.md §6.2.
  - Pass a tampered token (via `scripts/_tamper_sub.py`) through `demo-agent --raw` and assert the resource returns HTTP 401 (impersonation guard, CONTRACT.md §6.3).
  - Probe the exchange endpoint directly with `curl`, omitting `actor_token`, and assert HTTP 400 (CONTRACT.md §7).
- Block layout marked clearly with `#--- TEC block ---` and `#--- end TEC block ---` markers so later slices append their own blocks below per `design.md` "Smoke harness".

**Verified when:** `./scripts/smoke.sh` exits zero against a freshly brought-up topology (after `./scripts/bootstrap.sh`) with no manual steps between bring-up and harness execution. The smoke output contains the lines `[smoke:TEC] OK` and the three `[smoke:TEC] checking ...` log lines from `design.md`. A grep `grep -c 'TEC block' scripts/smoke.sh` returns 2 (start + end markers) confirming the block boundaries are explicit for future-slice append-only edits.

---

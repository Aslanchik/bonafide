# spire-workload-identity: Tasks

Tasks are ordered. Each must be complete and verifiable before the next begins. If a task surfaces a spec problem, stop — update the relevant spec, re-review, then continue. One commit per task.

Status: `[ ]` todo · `[x]` done · `[~]` in progress

---

## T-01: [ ] Decide pyspiffe vs. raw gRPC for the Python Workload API path

**Satisfies:** SWI-3 (precondition), SWI-5 (precondition), and the `design.md` "Open decisions resolved here" note that "the decision is made at the start of T-NN that introduces the agent-side SVID fetch; we do not preemptively write the gRPC version"

- Run a one-line `pip install pyspiffe` inside a throwaway Python 3.12 venv and confirm the import surface used by `design.md` "New `identity_spire.py`" exists: `from pyspiffe.workloadapi import default_jwt_source`, the `DefaultJwtSource(workload_api_client=..., audiences=[...])` constructor, the `get_jwt_svid()` method, and the `close()` method.
- If the import or any of the listed call sites is missing on Python 3.12 against SPIRE 1.10's wire protocol, record the failure in `agent-notes.md` (symptom / diagnosis / fix) per `CLAUDE.md` "Update `agent-notes.md` when an agent fails an interesting way before patching", and update `specs/spire-workload-identity/design.md` "Open decisions resolved here" to switch the default to a raw gRPC client built against `spiffe/spire-api-sdk`. Re-review the design diff before continuing.
- If the import succeeds, record a one-paragraph "pyspiffe path confirmed" note in `agent-notes.md` referencing the installed version and the SPIRE Server image tag used at the smoke check.
- This task adds no source code under `services/` or `sdks/`; its only artefact is the `agent-notes.md` entry (and, on the failure branch, a `design.md` diff).

**Verified when:** `grep -n "pyspiffe path confirmed\|pyspiffe path rejected" agent-notes.md` returns at least one line. If the note says "pyspiffe path rejected", a follow-up grep `grep -F 'pyspiffe' sdks/agent-py/pyproject.toml` returns **no matches** at the end of T-13 (the decision is enforced downstream — a `rejected` outcome must not result in a `pyspiffe` dependency landing). If the note says "pyspiffe path confirmed", the same grep returns at least one match after T-13. Additionally, if the rejection branch fired, `git diff specs/spire-workload-identity/design.md` shows the "Open decisions resolved here" bullet updated to the gRPC fallback.

---

## T-02: [ ] Add SPIRE Server and SPIRE Agent config files under `deploy/spire/`

**Satisfies:** SWI-1

- Create `deploy/spire/spire-server.conf` with the HCL shape from `design.md` "`spire-server.conf`": trust domain `bonafide.local`, `bind_address = "0.0.0.0"`, `bind_port = "8081"`, `data_dir = "/opt/spire/data"`, `ca_ttl = "168h"`, `default_x509_svid_ttl = "1h"`, `default_jwt_svid_ttl = "5m"` per the TTL ceiling in `DESIGN.md` §4 and `CLAUDE.md` "All credentials short-lived. TTL ceilings in `DESIGN.md` §4 (... JWT-SVID ≤ 5 min ...). No code path may extend these".
- Wire the `DataStore "sql"` plugin with `database_type = "sqlite3"` and `connection_string = "/opt/spire/data/datastore.sqlite3"`, the `NodeAttestor "x509pop"` plugin with `ca_bundle_path = "/etc/spire/server/agent-ca.crt"`, and the `KeyManager "disk"` plugin with `keys_path = "/opt/spire/data/keys.json"`, exactly as listed in `design.md` "`spire-server.conf`".
- Create `deploy/spire/spire-agent.conf` with the HCL shape from `design.md` "`spire-agent.conf`": `server_address = "spire-server"`, `server_port = "8081"`, `socket_path = "/run/spire/sockets/agent.sock"`, `trust_bundle_path = "/etc/spire/agent/bootstrap.crt"`, `trust_domain = "bonafide.local"`, the `NodeAttestor "x509pop"` plugin (with `private_key_path` and `certificate_path` under `/etc/spire/agent/`), the `WorkloadAttestor "docker"` plugin pointed at `/var/run/docker.sock`, and the `KeyManager "disk"` plugin.
- Add `deploy/spire/data/` to `.gitignore` per `design.md` "Files created / modified / deleted" so the SQLite datastore and CA materials never enter git.

**Verified when:** `cat deploy/spire/spire-server.conf | grep -E 'trust_domain|default_jwt_svid_ttl|NodeAttestor "x509pop"|KeyManager "disk"' | wc -l` is at least 4 and the file contains the literal string `default_jwt_svid_ttl = "5m"`; `cat deploy/spire/spire-agent.conf | grep -E 'server_address|socket_path|WorkloadAttestor "docker"' | wc -l` is at least 3; `grep -F 'deploy/spire/data' .gitignore` returns at least one line.

---

## T-03: [ ] Add `deploy/spire/generate-agent-ca.sh` for the `x509pop` bootstrap

**Satisfies:** SWI-1 (agent attestation prerequisite), and **"No static long-lived secrets in source, env files, or container images outside SPIRE and Vault"** — the generated CA + agent cert are SPIRE materials and live only under `deploy/spire/` which is gitignored

- Create `deploy/spire/generate-agent-ca.sh` as an idempotent script (re-runs are no-ops when both files exist) that produces, under `deploy/spire/`:
  1. A bootstrap CA private key + self-signed certificate that the SPIRE Server trusts (`agent-ca.crt` is referenced by `spire-server.conf` from T-02).
  2. A per-agent leaf certificate signed by that CA (`agent.crt` + `agent.key`) that the SPIRE Agent presents at attestation time via the `x509pop` node attestor.
- Use `openssl` only — no extra dependencies, matching `CLAUDE.md` "Ask before adding any dependency not on this list".
- The script must `chmod 0600` every private key it writes. Public certs may be world-readable.
- The script writes only inside `deploy/spire/`; all outputs land under the `deploy/spire/data/` gitignored prefix or alongside the conf files in a sub-tree also gitignored. No file is written outside the repo.

**Verified when:** First invocation creates the three artefacts and exits zero; second invocation exits zero and produces no diff (the files are not regenerated). `git status` after both runs reports no tracked-file modification. `openssl x509 -in deploy/spire/agent.crt -noout -issuer` confirms the agent cert is issued by the generated CA.

---

## T-04: [ ] Add `deploy/spire/registrations.sh` creating the three workload entries

**Satisfies:** SWI-2, and the TTL ceiling safety constraint **"All credentials short-lived ... JWT-SVID ≤ 5 min"**

- Create `deploy/spire/registrations.sh` exactly per `design.md` "Workload registration entries (`registrations.sh`)":
  - Idempotent (`entry show` first; only `entry create` when the entry is absent).
  - For each workload, the SPIFFE ID matches the grammar `spiffe://bonafide.local/{role}/{name}` of `CONTRACT.md` §1:
    - `spiffe://bonafide.local/agent/planner` for the agent (SWI-2 acceptance criterion).
    - `spiffe://bonafide.local/service/calendar` for the calendar (SWI-2 acceptance criterion).
    - `spiffe://bonafide.local/service/authz` for the authz server (SWI-2 acceptance criterion).
  - Selectors per `design.md`: `docker:image_id:<sha>` AND `docker:label:bonafide.workload=<name>`. The image-ID selector makes the binding tamper-evident; the label selector is the human-readable handshake. Both must be present on every entry, per SWI-2 acceptance criterion "Each registration entry is bound to its workload by selectors that pin both the container image and the Unix UID under which the workload runs" — read together with `design.md`'s `docker:image_id`/`docker:label` choice (the docker workload attestor implements both as selectors).
  - `-jwtSVIDTTL 300` (5 minutes — `DESIGN.md` §4 ceiling; `CLAUDE.md` "No code path may extend these"). `-x509SVIDTTL 3600` for the X.509 SVID (out of scope at the wire in this slice but issued anyway).
  - The script's `parent_id` derives from `spiffe://bonafide.local/spire/agent/x509pop/<agent-id-digest>` so it works against the SPIRE Agent registered via T-03's cert.
- Make the script executable.

**Verified when:** Running this script against a healthy SPIRE Server with the three workload images already built exits zero; running it a second time also exits zero and produces no new entries; `docker compose exec -T spire-server spire-server entry show -spiffeID spiffe://bonafide.local/agent/planner` returns exactly one entry whose `jwtSVIDTTL` is 300 seconds and whose selectors include both a `docker:image_id:` and a `docker:label:bonafide.workload=planner` row.

---

## T-05: [ ] Add `spire-server` and `spire-agent` services to `docker-compose.yml`

**Satisfies:** SWI-1, SWI-2 (selectors), SWI-6 (calendar mounts the workload-api socket), SWI-7 (the SPIRE Server's HTTP listener is reachable inside the compose network)

- Extend `docker-compose.yml` with the additions from `design.md` "docker-compose additions":
  - `spire-server` running `ghcr.io/spiffe/spire-server:1.10.0`, mounting `deploy/spire/spire-server.conf` read-only and a named volume `spire-server-data` at `/opt/spire/data`. Expose port 8081 inside the compose network for both the SPIRE bundle/registration API and the OIDC discovery HTTP listener.
  - `spire-agent` running `ghcr.io/spiffe/spire-agent:1.10.0`, mounting `deploy/spire/spire-agent.conf` read-only, the host docker socket read-only at `/var/run/docker.sock`, and a named volume `spire-agent-sockets` at `/run/spire/sockets`. The named volume is the shared Workload API UDS — SPIRE Agent is the only writer.
  - Add `depends_on: [spire-server]` to `spire-agent`.
  - On the existing `authz`, `calendar`, and `demo-agent` (`docker compose run --rm demo-agent ...`) services, add `labels: { bonafide.workload: <name> }` so the `docker:label:` selectors from T-04 match. Add a read-only mount of `spire-agent-sockets` at `/run/spire/sockets` on each of the three workload containers.
  - Declare the two new named volumes `spire-server-data` and `spire-agent-sockets` at the top-level `volumes:` key.
- Do not introduce any new exposed host ports beyond what TEC already publishes — `CLAUDE.md` "Don't widen the surface unnecessarily ... no endpoints, claims, env vars, or container ports not required by an approved slice". SPIRE Server's 8081 is reachable inside the compose network only.

**Verified when:** `docker compose config` parses cleanly; `docker compose config --services` lists both `spire-server` and `spire-agent`; `docker compose config --volumes` lists `spire-server-data` and `spire-agent-sockets`; `docker compose config | grep -A1 'authz:' | grep -c 'bonafide.workload'` is at least 1; the same grep for `calendar:` succeeds.

---

## T-06: [ ] Add a `spire-oidc-discovery-provider` sidecar to the compose stack

**Satisfies:** SWI-7

- Per `design.md` "SPIRE OIDC discovery provider", attach the SPIRE OIDC Discovery Provider companion binary to the `spire-server` container (either as a sidecar service in compose or as a second process invoked from the same container) so that:
  - `GET http://spire-server:8081/.well-known/openid-configuration` returns a JSON document advertising the SPIRE Server's issuer URI for trust domain `bonafide.local`.
  - The advertised `jwks_uri` resolves inside the compose network and returns a JWKS containing the SPIRE Server's current JWT signing keys.
  - The advertised issuer URI is the literal string `https://bonafide.local` (per `design.md` "SPIRE OIDC discovery provider": "The issuer URI advertised is `https://bonafide.local` (the trust-domain-derived issuer)").
- Per the SWI-7 acceptance criterion "No bonafide workload consumes the discovery endpoint in this slice", no workload container is wired to consume the JSON — the endpoint exists for VSA's fallback only.

**Verified when:** From inside the compose network (`docker compose exec -T authz curl -fsSL http://spire-server:8081/.well-known/openid-configuration`), the response is HTTP 200 and the JSON body's `issuer` field is `https://bonafide.local`; a follow-up `curl` against that body's `jwks_uri` returns HTTP 200 with a JWKS containing at least one key.

---

## T-07: [ ] Extend `scripts/bootstrap.sh` with the SPIRE bring-up sequence

**Satisfies:** SWI-1, SWI-2, SWI-7 (bootstrap-time smoke), and the fail-closed safety constraint at startup

- Extend `scripts/bootstrap.sh` per `design.md` "Bootstrap script additions", inserting four steps after the existing TEC `docker compose up --wait`:
  1. Invoke `./deploy/spire/generate-agent-ca.sh` (idempotent; T-03).
  2. Wait for spire-server to be healthy via `docker compose exec -T spire-server spire-server healthcheck`.
  3. Invoke `./deploy/spire/registrations.sh` to create or refresh the three entries (T-04).
  4. Smoke-verify the OIDC discovery endpoint with `curl -fsSL http://spire-server:8081/.well-known/openid-configuration > /dev/null` (SWI-7).
- Remove the TEC steps that generated per-agent dev keys for the YAML-trust path: the `deploy/spire-stub/` directory and `deploy/authz/actor-trust.yaml` are deleted in T-12 and T-13; the bootstrap must not regenerate them.
- Per `design.md`: "If `deploy/spire-stub/` still exists from a TEC checkout, the bootstrap removes it with a one-line warning." Implement this guard in the bootstrap so that an old checkout cleans itself up on the next run.
- The user-JWT signing-key generation step (TEC) is preserved unchanged — the authz still signs user JWTs with its own Ed25519 key.

**Verified when:** From a clean checkout, `./scripts/bootstrap.sh` exits zero and `docker compose ps` shows both `spire-server` and `spire-agent` as `running`; a second invocation also exits zero, removes any leftover `deploy/spire-stub/` if it existed, and produces no diff in any tracked file under `deploy/`; `docker compose exec -T spire-server spire-server entry show` lists three entries (planner, calendar, authz) per T-04.

---

## T-08: [ ] Replace `services/authz/internal/trust` static impl with `workloadapi.go`

**Satisfies:** SWI-4, and the safety constraints **"Fail closed. Missing actor_token, unknown agent, unknown scope grammar, unparseable token, unreachable policy engine, expired anything → deny"** and **"No static long-lived secrets ... outside SPIRE and Vault"**

- Add `github.com/spiffe/go-spiffe/v2` (≥ v2.4) to `services/authz/go.mod` per `CLAUDE.md` "Stack pins: `github.com/spiffe/go-spiffe/v2` (v2.4+) for Go".
- Delete `services/authz/internal/trust/static.go` (the M1 YAML impl `YAMLTrust`, authored in TEC's T-07 as a sibling of the `IssuerTrust` interface). Do not delete `services/authz/internal/trust/trust.go` — the `IssuerTrust` interface lives there and is unchanged per `design.md` "Interface (unchanged from TEC)". If a checkout from before the TEC-T-07 file-split has the impl inside `trust.go` instead of `static.go`, the deletion target is the `YAMLTrust` type and its constructor specifically — not the interface.
- Create `services/authz/internal/trust/workloadapi.go` exactly per `design.md` "SWI impl":
  - `NewFromWorkloadAPI(ctx, socketPath, audience string) (*workloadAPITrust, error)` dials the Workload API at `unix://<socketPath>`.
  - `(*workloadAPITrust).Verify(ctx, rawJWT)` performs three fail-closed checks: (1) signature against bundles from `FetchJWTBundles(ctx)`; (2) audience match against `audience`; (3) trust domain match against `spiffe://bonafide.local` and accepted role `agent/*` or `service/*` only (CONTRACT.md §1 grammar; never `human/*` per SWI-1 acceptance criterion "No human identities are issued by SPIRE").
  - Any of the three checks failing returns a non-nil error whose string is suitable for an OAuth `error_description`.
  - The returned `ActorTokenClaims` carries `Iss = svid.Claims["iss"].(string)` (the SPIRE Server's issuer URI), `Sub = svid.ID.String()`, `Aud = audience`, `Exp = svid.Expiry.Unix()`. The `Sub` is the SPIFFE ID verbatim and is what the exchange handler from TEC consumes (no call-site change).
- The file-level comment explicitly cites CONTRACT.md §1, §3, §7 and the relevant SWI acceptance criteria.
- The M1 YAML-trust code path is deleted, not commented out — SWI-4 acceptance criterion: "The dev-signed `actor_token` code path used in `token-exchange-core` is no longer accepted by the authz server after this slice."

**Verified when:** `go build ./...` inside `services/authz` exits zero; `grep -r 'staticTrust\|YAMLTrust\|actor-trust.yaml' services/authz/` returns no matches; `grep -n 'FetchJWTBundles\|ParseAndValidate\|MemberOf' services/authz/internal/trust/workloadapi.go` returns all three calls; `go vet ./internal/trust/...` exits zero. The compiled binary is deleted immediately after the build per `CLAUDE.md` "Delete compiled binaries after build/test".

---

## T-09: [ ] Unit tests for `workloadapi.go` covering every SWI-4 rejection criterion

**Satisfies:** SWI-4 (test acceptance criteria), and the safety constraint **"Fail closed"**

- Create `services/authz/internal/trust/workloadapi_test.go` with table-driven tests covering each rejection branch in `design.md` "SWI impl" plus each rejection acceptance criterion of SWI-4. Use a `jwtsvid.Source` mock or an in-memory go-spiffe bundle (the go-spiffe v2 `jwtbundle.Bundle` type accepts test keys directly) — no real SPIRE process is needed for the unit tests.
  - **Signature mismatch:** a JWT signed by a key not in the configured bundle → non-nil error; `ActorTokenClaims` is empty. SWI-4 acceptance criterion: "An `actor_token` whose signature does not verify against any current JWT bundle for `bonafide.local` is rejected with HTTP 400 and `error=invalid_request`".
  - **Wrong issuer:** a JWT whose `iss` is not the SPIRE Server's `bonafide.local` issuer URI → non-nil error. SWI-4 acceptance criterion: "An `actor_token` whose `iss` does not equal the SPIRE Server's issuer URI for the `bonafide.local` trust domain is rejected with HTTP 400 and `error=invalid_request`".
  - **Expired `exp`:** a JWT whose `exp` is in the past → non-nil error. SWI-4 acceptance criterion: "An `actor_token` whose `exp` is in the past is rejected with HTTP 400 and `error=invalid_request`".
  - **Bad `sub` grammar:** a JWT whose `sub` is e.g. `spiffe://bonafide.local/human/foo` (or any non-`agent/*`/non-`service/*` path, or a non-`bonafide.local` trust domain) → non-nil error. SWI-4 acceptance criterion: "An `actor_token` whose `sub` is not a SPIFFE ID matching `spiffe://bonafide.local/{role}/{name}` per CONTRACT.md §1 is rejected with HTTP 400 and `error=invalid_request`".
  - **Wrong `aud`:** a JWT whose `aud` does not match the constructor-time `audience` → non-nil error.
  - **Happy path:** a JWT signed by a key in the bundle with correct `iss`/`sub`/`aud`/`exp` returns `ActorTokenClaims` whose `Sub` equals the SPIFFE ID verbatim.

**Verified when:** `go test ./internal/trust/... -count=1 -run TestVerify` exits zero and every table row above runs at least once. The compiled test binary is deleted after the run per `CLAUDE.md` "Delete compiled binaries after build/test".

---

## T-10: [ ] Wire `cmd/authz/main.go` to construct `trust.NewFromWorkloadAPI` and remove the YAML-trust load

**Satisfies:** SWI-4 (integration; the dev-signed code path is no longer reachable), and the fail-closed safety constraint at startup

- Update `services/authz/cmd/authz/main.go` per `design.md` "Files created / modified / deleted" → `services/authz/cmd/authz/main.go`:
  - Drop the YAML-trust constructor and the `BONAFIDE_AUTHZ_ACTOR_TRUST_PATH` env-var read. The env var is gone from `internal/config` too (T-11 update below).
  - Add a `BONAFIDE_SPIFFE_WORKLOAD_API_SOCKET` env var (default `/run/spire/sockets/agent.sock`, per `design.md` "Authz container") and pass it plus `settings.Issuer` to `trust.NewFromWorkloadAPI(ctx, socketPath, settings.Issuer)`.
  - At startup, the authz server waits up to 3 seconds for the workload-api socket to be present per `design.md` "Authz container": "Authz waits for the workload-api socket at startup (3-second budget); if absent, it exits non-zero with a clear log line." On timeout, exit non-zero with a structured slog line — fail closed per `CLAUDE.md`.
  - The construction failure of `trust.NewFromWorkloadAPI` also exits non-zero; no fallback to YAML trust exists (the symbol is gone after T-08).

**Verified when:** Running the binary with the env var pointing to a non-existent socket path exits non-zero within 4 seconds; running it pointing at a valid `unix://` path (the running compose stack) starts the server and a `curl http://127.0.0.1:8080/healthz` returns 200; `grep -r 'BONAFIDE_AUTHZ_ACTOR_TRUST_PATH\|YAMLTrust' services/authz/` returns no matches. The compiled binary is deleted after the test.

---

## T-11: [ ] Update `internal/config` to drop the trust-yaml var and add the workload-api socket var

**Satisfies:** SWI-4 (config surface)

- Edit `services/authz/internal/config/config.go`:
  - Remove `BONAFIDE_AUTHZ_ACTOR_TRUST_PATH` (the M1 YAML-trust path).
  - Add `BONAFIDE_SPIFFE_WORKLOAD_API_SOCKET` with default `/run/spire/sockets/agent.sock`. Validate at load time: the path string must be non-empty (a startup with an empty value exits non-zero).
- Per `CLAUDE.md` "Don't widen the surface unnecessarily ... no endpoints, claims, env vars, or container ports not required by an approved slice", do not add any other env var to the authz binary in this slice.

**Verified when:** `grep -F 'BONAFIDE_AUTHZ_ACTOR_TRUST_PATH' services/authz/internal/config/config.go` returns no matches; `grep -F 'BONAFIDE_SPIFFE_WORKLOAD_API_SOCKET' services/authz/internal/config/config.go` returns at least one match; `go build ./...` inside `services/authz` exits zero.

---

## T-12: [ ] Delete `deploy/authz/actor-trust.yaml` and `deploy/spire-stub/`

**Satisfies:** SWI-4 (the M1 trust artefacts are gone), and the safety constraint **"No static long-lived secrets in source, env files, or container images outside SPIRE and Vault"**

- Delete `deploy/authz/actor-trust.yaml`.
- Delete the `deploy/spire-stub/` directory and all its contents (the per-agent dev keys from TEC's T-27).
- Update `.gitignore` to no longer reference `deploy/spire-stub/*.pem` (the directory is gone). Add `deploy/spire/data/*` and the agent CA materials per `design.md` "Files created / modified / deleted" → `.gitignore` row.
- Per `design.md` "Authz container" and `design.md` "Files created / modified / deleted", the YAML path and stub keys are not part of the SWI runtime; the workload-api socket is now the only trust source.

**Verified when:** `test ! -f deploy/authz/actor-trust.yaml`; `test ! -d deploy/spire-stub`; `grep -F 'deploy/spire/data' .gitignore` returns at least one line; `grep -F 'deploy/spire-stub' .gitignore` returns no matches. `git status` reports the deletions as staged or untracked-deletion.

---

## T-13: [ ] Replace `sdks/agent-py/bonafide_agent/identity.py` with `identity_spire.py`

**Satisfies:** SWI-3, and the safety constraint **"No static long-lived secrets in source, env files, or container images outside SPIRE and Vault. The agent SDK never reads a credential from disk"**

- Delete `sdks/agent-py/bonafide_agent/identity.py` (the M1 local-key signer from TEC's T-15).
- Create `sdks/agent-py/bonafide_agent/identity_spire.py` exactly per `design.md` "New `identity_spire.py`":
  - `SpireActorTokenSigner.__init__(*, spiffe_socket: str, audience: str)` constructs a `pyspiffe.workloadapi.default_jwt_source.DefaultJwtSource` with the workload-api client pointed at `spiffe_socket` and the requested `audiences=[audience]`.
  - `fetch_actor_token() -> str` calls `self._source.get_jwt_svid()` and returns `svid.token` — the raw JWT-SVID string. SWI-3 acceptance criterion: "Before every token-exchange request, the agent fetches a JWT-SVID from the Workload API socket mounted into its container."
  - `close()` releases the workload-api connection.
  - The class never reads from disk, environment, or container image layers for a credential (the workload-api UDS is the only source) per SWI-3 acceptance criterion and `CLAUDE.md` "The agent SDK never reads a credential from disk".
  - No internal cache. The signer asks pyspiffe for the SVID each call; pyspiffe's `DefaultJwtSource` is responsible for refusing to return an expired SVID. SWI-5 acceptance criterion: "There is no agent code path that extends, refreshes in place, or otherwise prolongs a JWT-SVID beyond its issued `exp`."
- Add `pyspiffe` to `sdks/agent-py/pyproject.toml` and remove `python-jose[cryptography]` from the dependencies if it is no longer used elsewhere in `bonafide_agent/` (the resource SDK still depends on it; the agent SDK does not after this swap).

**Verified when:** `test ! -f sdks/agent-py/bonafide_agent/identity.py`; `grep -rE "open\\(.+key|RSAKey|EdKey|jwt\\.encode" sdks/agent-py/bonafide_agent/` returns no matches (the agent SDK no longer reads a key from disk or signs in process); `pip install -e sdks/agent-py` succeeds and `python -c "from bonafide_agent.identity_spire import SpireActorTokenSigner"` exits zero.

---

## T-14: [ ] Update `BonafideAgent.exchange` to use `SpireActorTokenSigner` and swap the constructor

**Satisfies:** SWI-3 (the SVID is the `actor_token` on the exchange request), and the safety constraint that the SDK never re-uses or extends an SVID past its `exp`

- Edit `sdks/agent-py/bonafide_agent/client.py` per `design.md` "Agent SDK: identity swap" → "Interface":
  - Replace the constructor parameters `key_path` and `kid` with `spiffe_socket: str = "/run/spire/sockets/agent.sock"`. All other parameters (`authz_token_url`, `spiffe_id`, `vault_addr`, `vault_token`, `vault_kv_path`) keep their TEC semantics.
  - Inside `__init__`, instantiate a `SpireActorTokenSigner(spiffe_socket=spiffe_socket, audience=authz_token_url)` and hold it on the instance.
  - Inside `exchange(subject_token, scope, audience)`, replace the previous `sign_actor_token(...)` call with `self._signer.fetch_actor_token()`. SWI-3 acceptance criterion: the JWT-SVID is sent verbatim as the `actor_token` parameter; `actor_token_type` is unchanged at `urn:ietf:params:oauth:token-type:jwt` (CONTRACT.md §7).
  - No other code in `exchange` changes: the request body shape, the POST URL, the response handling, the `ExchangeError` mapping all stay as in TEC's T-16.
  - SWI-3 acceptance criterion: "If the Workload API is unreachable or returns no SVID for the agent's selectors, no token-exchange request is sent and the operation fails closed." Implement this by surfacing any exception raised by `fetch_actor_token()` directly to the caller (no `except: pass`); the SDK does not call `httpx.post` if the signer raises.
- Add a `close()` method on `BonafideAgent` that closes the signer (call it from the demo-agent CLI in T-15).

**Verified when:** `pytest sdks/agent-py -k exchange -count=1` includes tests asserting: (a) the constructor accepts `spiffe_socket` and rejects unknown kwargs `key_path`/`kid`; (b) a mocked `SpireActorTokenSigner` whose `fetch_actor_token` raises causes `exchange()` to raise without sending any HTTP request (use a mocked `httpx.MockTransport` whose `handler` records call count and assert it is zero); (c) the POST body still contains every CONTRACT.md §7 parameter, with `actor_token_type == "urn:ietf:params:oauth:token-type:jwt"` and the `actor_token` parameter equal to the string returned by `fetch_actor_token`.

---

## T-15: [ ] Update `apps/demo-agent` CLI to pass `spiffe_socket` and add `--print-actor-token-and-exit`

**Satisfies:** SWI-3 (driver side), SWI-8 (smoke-block dependency)

- Edit `apps/demo-agent/demo_agent/__main__.py`:
  - Replace the `key_path`/`kid` env vars (TEC) with `BONAFIDE_SPIFFE_WORKLOAD_API_SOCKET` (default `/run/spire/sockets/agent.sock`, matching the authz side in T-10).
  - Construct `BonafideAgent(...)` with `spiffe_socket=...` per T-14.
  - Add a `--print-actor-token-and-exit` flag (per `design.md` "Smoke harness — SWI block" — the smoke needs the raw SVID for `iss`/`sub` assertions). When the flag is set, the CLI fetches a single actor_token via the SDK's signer, prints it to stdout (no trailing whitespace beyond a single newline), and exits zero without performing the exchange/Vault/resource pipeline.
  - All other CLI behavior is unchanged from TEC's T-24: the `--user-jwt` flag is still required; without `--print-actor-token-and-exit`, the pipeline still runs end-to-end.
  - Any exception aborts and exits non-zero per the TEC fail-closed contract.
- Add the `bonafide.workload=planner` label to the `demo-agent` docker compose service definition (carrying forward from T-05) so that the smoke harness's `docker compose run --rm demo-agent` selects against the planner registration entry.

**Verified when:** Inside the brought-up compose stack, `docker compose run --rm demo-agent python -m demo_agent --user-jwt "$USER_JWT" --print-actor-token-and-exit` exits zero and prints a JWT string. Decoding that JWT (without verification) yields `iss == "https://bonafide.local"` and `sub == "spiffe://bonafide.local/agent/planner"` per `design.md` "Smoke harness — SWI block".

---

## T-16: [ ] Add the calendar workload-api mount (SVID issuance only, no wire use)

**Satisfies:** SWI-6

- Per `design.md` "Calendar container", confirm the `calendar` compose service from T-05 has both:
  - `labels: { bonafide.workload: calendar }` so the docker workload attestor matches the registration entry.
  - A read-only mount of `spire-agent-sockets` at `/run/spire/sockets`.
- Per SWI-6 acceptance criterion, the calendar workload **holds** a SPIFFE identity but does not present it at the wire in this slice. No change to `sdks/resource-py/` or `apps/demo-calendar/`. The calendar code is not extended in this slice — verifying SVID issuance happens via the SPIRE API in the smoke check, not via the application code path.
- Per the SWI-6 acceptance criterion "Removing the calendar registration entry causes the calendar workload to receive no SVID on its next Workload API call", the smoke block (T-17) does not exercise the calendar's SVID-removal path — the SWI-2 / SWI-8 path covers the equivalent property for the agent, and the calendar's identity is verified only by the SPIRE API. This matches the slice's stated intent.

**Verified when:** `docker compose exec -T calendar /opt/spire/bin/spire-agent api fetch jwt -audience http://calendar.bonafide.local:9000 -socketPath /run/spire/sockets/agent.sock` (or the equivalent invocation against the workload-api socket — exact binary may differ depending on the calendar image; falling back to a one-shot `docker run --rm ... ghcr.io/spiffe/spire-agent:1.10.0 api fetch jwt ...` is acceptable) returns a JWT-SVID whose `sub` is `spiffe://bonafide.local/service/calendar`. No code in `apps/demo-calendar/` references the workload-api socket — `grep -r "spire\\|workloadapi\\|jwt-svid" apps/demo-calendar/` returns no matches.

---

## T-17: [ ] Append the SWI block to `scripts/smoke.sh` and assert end-to-end

**Satisfies:** SWI-8 (the slice's end-to-end requirement), and depends on every prior task

- Extend `scripts/smoke.sh` per `design.md` "Smoke harness — SWI block", inserted after the existing TEC block (between `#--- end TEC block ---` and EOF). Use `#--- SWI block ---` and `#--- end SWI block ---` markers so later slices append cleanly per `CLAUDE.md` "Thin vertical slices ... Each slice extends `scripts/smoke.sh` by one block".
- The block performs the following assertions in order:
  1. **TEC block still passes:** the SWI block is appended below; running `scripts/smoke.sh` end-to-end exercises both. SWI-8 acceptance criterion: "All prior smoke-check blocks from `token-exchange-core` continue to pass unchanged."
  2. **Re-mint the user JWT** (the TEC block leaves it in `$USER_JWT` already; if scoping is unclear, re-mint via `docker compose run --rm demo-human python -m demo_human --email alice@example.com`).
  3. **Capture the agent's actor_token** via `ACTOR_TOKEN=$(docker compose run --rm demo-agent python -m demo_agent --user-jwt "$USER_JWT" --print-actor-token-and-exit)` (T-15).
  4. **Decode (without verification) and assert `iss` and `sub`:** decode the payload via `python -c 'import sys, json, base64; payload=sys.argv[1].split(".")[1]; payload+="=" * (-len(payload) % 4); print(base64.urlsafe_b64decode(payload).decode())' "$ACTOR_TOKEN"` (python is already on the path via `python-jose`/the demo SDKs — `jwt-cli` is not on the stack pins per `CLAUDE.md`). Pipe to `jq -r .iss` and `jq -r .sub`. SWI-8 acceptance criteria: `test "$ISS" = "https://bonafide.local"` AND `test "$SUB" = "spiffe://bonafide.local/agent/planner"`.
  5. **End-to-end exchange succeeds:** run `docker compose run --rm demo-agent python -m demo_agent --user-jwt "$USER_JWT"` without the print flag and assert it exits zero and that the resulting JSON `.acted_by == "spiffe://bonafide.local/agent/planner"` per CONTRACT.md §6.1 / SWI-8 acceptance criterion "The smoke check asserts that the minted task token's `act.sub`, when an exchange does succeed, equals the SPIFFE ID carried in the `actor_token`'s `sub`".
  6. **Registration-removal fail-closed assertion:** delete the planner registration via `docker compose exec -T spire-server spire-server entry delete -spiffeID spiffe://bonafide.local/agent/planner`; `sleep 2` for the workload-api stream to propagate; run `docker compose run --rm demo-agent python -m demo_agent --user-jwt "$USER_JWT" 2>&1 || true` and assert the output matches `no svid` OR `401` OR `access_denied`. SWI-8 acceptance criterion: "after removing the agent's workload registration entry from SPIRE Server, asserts that the next token-exchange attempt by the agent fails closed (no task token is returned)."
  7. **Restore the entry** for repeatability: invoke `./deploy/spire/registrations.sh` (idempotent; T-04) so the smoke can be re-run without a manual reset.
- The block emits the literal log lines `[smoke:SWI] verifying actor_token is SPIRE-issued...`, `[smoke:SWI] removing planner registration -> next exchange must fail closed...`, `[smoke:SWI] re-create the entry for the next slice/run...`, and `[smoke:SWI] OK` per `design.md` "Smoke harness — SWI block".

**Verified when:** From a clean checkout, `./scripts/bootstrap.sh && ./scripts/smoke.sh` exits zero with no manual steps in between. The smoke output contains the lines `[smoke:TEC] OK` (TEC block unchanged) AND `[smoke:SWI] OK`. `grep -c 'SWI block' scripts/smoke.sh` returns 2 (start + end markers). A re-run of `./scripts/smoke.sh` (no bootstrap in between) also exits zero — the SWI block's step 7 left the registration in place.

---

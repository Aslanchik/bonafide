# spire-workload-identity: Design

## Overview

This slice deletes the M1 dev-key stub and replaces it with real SPIRE-issued JWT-SVIDs. The interface `trust.IssuerTrust` that TEC introduced gets a new implementation backed by go-spiffe v2's Workload API client; the file `deploy/authz/actor-trust.yaml` and the directory `deploy/spire-stub/` go away. Two new containers (`spire-server`, `spire-agent`) join the compose stack, and three workload registration entries are created at bootstrap time. The agent SDK swaps its on-disk-key actor_token signer for a Workload API fetch via pyspiffe. Everything in the TEC slice — the act-chain builder, the policy gate, the JWKS, the file-backed audit emitter, the calendar app, the resource SDK middleware — is unchanged.

The slice also exposes SPIRE's OIDC discovery provider at a known endpoint inside the compose network. Nothing in this slice consumes it; it exists so that VSA's fallback (Vault JWT auth) is one wiring change away.

For wire formats and the higher-level component map see `CONTRACT.md` §1 (SPIFFE ID grammar), `CONTRACT.md` §7 (the `actor_token` parameter), and `DESIGN.md` §2.4.

---

## Stack (additions only)

| Concern | Choice | Why |
|---|---|---|
| SPIRE Server | `ghcr.io/spiffe/spire-server:1.10+` | latest stable |
| SPIRE Agent | `ghcr.io/spiffe/spire-agent:1.10+` | matches server |
| Go Workload API client | `github.com/spiffe/go-spiffe/v2` (v2.4+) | `workloadapi.NewClient`, `FetchJWTBundles` — used inside `trust.IssuerTrust`'s new SPIRE-backed impl |
| Python Workload API client | `pyspiffe` | Resolves the SDK's identity-fetch path |
| Node attestor (dev) | `x509pop` | Simplest local-dev attestor; selectors by image+UID via workload attestor |
| Workload attestor (dev) | `docker` | Selects by image name and Unix UID |

`pyspiffe` is current and maintained. If a runtime check at the start of this slice's build reveals it is incompatible with Python 3.12 or with the SPIRE 1.10 wire protocol, the fallback is a thin gRPC client built against the proto files in `spiffe/spire-api-sdk`. The decision is made at the start of T-NN that introduces the agent-side SVID fetch; we do not preemptively write the gRPC version.

---

## Repo additions and deletions

```
+ deploy/spire/
+   ├── spire-server.conf
+   ├── spire-agent.conf
+   ├── data/                          # SPIRE Server's local datastore (gitignored)
+   └── registrations.sh               # idempotent: create the three entries
+
+ services/authz/internal/trust/
+   └── workloadapi.go                 # NEW impl of trust.IssuerTrust using go-spiffe v2

  services/authz/internal/trust/trust.go    # interface unchanged; M1 YAML impl removed

+ sdks/agent-py/bonafide_agent/
+   └── identity_spire.py              # NEW: replaces identity.py's local-key signing path

  sdks/agent-py/bonafide_agent/identity.py  # DELETED — M1 stub
- deploy/spire-stub/                   # DELETED
- deploy/authz/actor-trust.yaml        # DELETED

  scripts/bootstrap.sh                 # extended: SPIRE bring-up + registration entries
  scripts/smoke.sh                     # SWI block appended
  docker-compose.yml                   # spire-server + spire-agent services added
```

The agent SDK's `client.py` is unchanged at the call sites (`exchange()`, `fetch_connection()`, `call()`); only the `identity` module under it swaps.

---

## SPIRE topology

### Trust domain

`bonafide.local`. Single domain; no federation.

### `spire-server.conf` (compose container `spire-server`)

```hcl
server {
  bind_address     = "0.0.0.0"
  bind_port        = "8081"
  trust_domain     = "bonafide.local"
  data_dir         = "/opt/spire/data"
  log_level        = "INFO"
  ca_ttl           = "168h"     // 7 days; dev only
  default_x509_svid_ttl = "1h"
  default_jwt_svid_ttl  = "5m"  // DESIGN.md §4 ceiling
}

plugins {
  DataStore "sql" {
    plugin_data {
      database_type = "sqlite3"
      connection_string = "/opt/spire/data/datastore.sqlite3"
    }
  }
  NodeAttestor "x509pop" {
    plugin_data {
      ca_bundle_path = "/etc/spire/server/agent-ca.crt"
    }
  }
  KeyManager "disk" {
    plugin_data {
      keys_path = "/opt/spire/data/keys.json"
    }
  }
}
```

`default_jwt_svid_ttl = 5m` enforces the JWT-SVID TTL ceiling at the issuer (DESIGN.md §4); the agent SDK never extends it.

### `spire-agent.conf` (compose container `spire-agent`)

```hcl
agent {
  data_dir        = "/opt/spire/data"
  log_level       = "INFO"
  server_address  = "spire-server"
  server_port     = "8081"
  socket_path     = "/run/spire/sockets/agent.sock"   // exposed via named volume to workloads
  trust_bundle_path = "/etc/spire/agent/bootstrap.crt"
  trust_domain    = "bonafide.local"
}

plugins {
  NodeAttestor "x509pop" {
    plugin_data {
      private_key_path = "/etc/spire/agent/agent.key"
      certificate_path = "/etc/spire/agent/agent.crt"
    }
  }
  WorkloadAttestor "docker" {
    plugin_data {
      docker_socket_path = "/var/run/docker.sock"
    }
  }
  KeyManager "disk" {
    plugin_data {
      directory = "/opt/spire/data/keys"
    }
  }
}
```

The workload API socket (`/run/spire/sockets/agent.sock`) is shared into the `authz`, `calendar`, and `agent` containers via a named docker volume.

### Workload registration entries (`registrations.sh`)

Idempotent script. Run at the end of `bootstrap.sh` after `spire-server` is healthy.

```bash
#!/usr/bin/env bash
set -euo pipefail

server="docker compose exec -T spire-server spire-server"

# Helper: create or update an entry.
ensure_entry() {
    local spiffe_id="$1"; shift
    local parent_id="spiffe://bonafide.local/spire/agent/x509pop/$(get_agent_id_digest)"
    if $server entry show -spiffeID "$spiffe_id" -parentID "$parent_id" 2>/dev/null | grep -q "$spiffe_id"; then
        return 0
    fi
    $server entry create \
        -parentID "$parent_id" \
        -spiffeID "$spiffe_id" \
        -selector "docker:image_id:$(get_image_id "$@")" \
        -selector "docker:label:bonafide.workload=$1" \
        -jwtSVIDTTL 300 \
        -x509SVIDTTL 3600
}

ensure_entry spiffe://bonafide.local/agent/planner   bonafide/demo-agent:latest
ensure_entry spiffe://bonafide.local/service/authz   bonafide/authz:latest
ensure_entry spiffe://bonafide.local/service/calendar bonafide/demo-calendar:latest
```

Selectors pin both the docker image ID and a `bonafide.workload=...` label set by each service's Dockerfile. The label is the explicit handshake; the image ID makes it tamper-evident.

JWT-SVID TTL is 300 s (5 min) — the DESIGN.md §4 ceiling.

### SPIRE OIDC discovery provider

SPIRE Server's built-in HTTP listener serves `/.well-known/openid-configuration` and JWKS for the trust domain. Wired in `spire-server.conf` (snippet above is the minimum; the full config adds):

```hcl
federation {
  bundle_endpoint {
    address = "0.0.0.0"
    port    = 8443
  }
}
```

Plus the `oidc-discovery-provider` companion binary running as a sidecar in the `spire-server` container exposing `http://spire-server:8081/.well-known/openid-configuration` and the corresponding JWKS at `http://spire-server:8081/keys`. The issuer URI advertised is `https://bonafide.local` (the trust-domain-derived issuer). VSA reads this URI verbatim if it falls back from native SPIFFE auth to JWT auth.

No bonafide workload consumes the OIDC discovery in this slice; it is verified only by `curl` against the endpoint in the smoke check.

---

## Authz: `trust.IssuerTrust` swap

### Interface (unchanged from TEC)

```go
// services/authz/internal/trust/trust.go (unchanged)
package trust

import "context"

// IssuerTrust verifies an actor_token. The implementation must:
//   - validate the token's signature against a trust source the implementation controls
//   - return the decoded claims if and only if the signature is valid AND the token's exp is in the future
//   - return a non-nil error otherwise; the error string is suitable for inclusion in an OAuth error_description
type IssuerTrust interface {
    Verify(ctx context.Context, rawJWT string) (*ActorTokenClaims, error)
}
```

### M1 impl (deleted)

The YAML-backed `staticTrust` impl from TEC is removed. The file `actor-trust.yaml` is no longer loaded.

### SWI impl

```go
// services/authz/internal/trust/workloadapi.go
package trust

import (
    "context"
    "fmt"

    "github.com/spiffe/go-spiffe/v2/spiffeid"
    "github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
    "github.com/spiffe/go-spiffe/v2/workloadapi"
)

type workloadAPITrust struct {
    client      *workloadapi.Client
    trustDomain spiffeid.TrustDomain
    audience    string  // authz's own URL; the actor_token must have this in aud
}

// NewFromWorkloadAPI dials the local SPIFFE Workload API socket and returns a
// trust.IssuerTrust backed by JWT bundles fetched from SPIRE. Caller closes
// the returned impl to release the workload-api connection.
func NewFromWorkloadAPI(ctx context.Context, socketPath, audience string) (*workloadAPITrust, error) {
    client, err := workloadapi.New(ctx, workloadapi.WithAddr("unix://"+socketPath))
    if err != nil {
        return nil, fmt.Errorf("workloadapi dial: %w", err)
    }
    td, err := spiffeid.TrustDomainFromString("bonafide.local")
    if err != nil {
        return nil, err
    }
    return &workloadAPITrust{client: client, trustDomain: td, audience: audience}, nil
}

func (w *workloadAPITrust) Verify(ctx context.Context, rawJWT string) (*ActorTokenClaims, error) {
    // FetchJWTBundles is cached internally by the workload-api client with
    // stream updates from SPIRE Agent; callers do not need to memoize.
    bundles, err := w.client.FetchJWTBundles(ctx)
    if err != nil {
        return nil, fmt.Errorf("fetch jwt bundles: %w", err)
    }
    svid, err := jwtsvid.ParseAndValidate(rawJWT, bundles, []string{w.audience})
    if err != nil {
        return nil, fmt.Errorf("jwt-svid validate: %w", err)  // surfaces sig / aud / exp failures uniformly
    }
    // Trust-domain check: SVID must be issued by spiffe://bonafide.local.
    if !svid.ID.MemberOf(w.trustDomain) {
        return nil, fmt.Errorf("trust domain mismatch: %s", svid.ID.TrustDomain())
    }
    // SPIFFE ID grammar check (CONTRACT.md §1).
    if !isAcceptedRole(svid.ID.Path()) {
        return nil, fmt.Errorf("unexpected SPIFFE path: %s", svid.ID.Path())
    }
    return &ActorTokenClaims{
        Iss: svid.Claims["iss"].(string),
        Sub: svid.ID.String(),
        Aud: w.audience,
        Exp: svid.Expiry.Unix(),
    }, nil
}

func isAcceptedRole(path string) bool {
    // /agent/<name> or /service/<name>; never /human/*.
    return strings.HasPrefix(path, "/agent/") || strings.HasPrefix(path, "/service/")
}
```

Three validations every Verify call performs, all of them fail-closed per CLAUDE.md:
1. Signature against current SPIRE JWT bundles.
2. `aud` equals the authz server's own issuer URL (passed at construction time).
3. SPIFFE ID belongs to the `bonafide.local` trust domain and uses an accepted role (`agent` or `service`).

The exchange handler's call site does not change: it invokes `trust.IssuerTrust.Verify(ctx, actorToken)` and threads the resulting `ActorTokenClaims` into the policy gate's `PolicyInput`.

### Authz container

The authz Dockerfile gains `bonafide.workload=authz` as a label, and the compose service mounts `/run/spire/sockets:/run/spire/sockets:ro` from the shared volume. Authz waits for the workload-api socket at startup (3-second budget); if absent, it exits non-zero with a clear log line.

---

## Agent SDK: identity swap

### Interface

The `BonafideAgent` constructor's signature changes. Where TEC took `key_path` and `kid`, SWI takes `spiffe_socket`:

```python
# sdks/agent-py/bonafide_agent/client.py — constructor change
class BonafideAgent:
    def __init__(self, *, authz_token_url: str, spiffe_id: str,
                 spiffe_socket: str = "/run/spire/sockets/agent.sock",
                 vault_addr: str, vault_token: str, vault_kv_path: str):
        self._spiffe_socket = spiffe_socket
        ...
```

The rest of the SDK is unchanged: `exchange()`, `fetch_connection()`, `call()` keep their signatures and semantics.

### New `identity_spire.py`

```python
# sdks/agent-py/bonafide_agent/identity_spire.py
from pyspiffe.workloadapi import default_jwt_source

class SpireActorTokenSigner:
    """Fetches a JWT-SVID from the local SPIFFE Workload API and uses it as actor_token.

    Replaces the M1 local-key signer. SAN reuses this class verbatim for both
    the planner and the tool agent — each agent's container has its own
    SPIRE workload registration entry, so the same code returns a different
    SVID per container.
    """

    def __init__(self, *, spiffe_socket: str, audience: str):
        self._source = default_jwt_source.DefaultJwtSource(
            workload_api_client=workload_api_client(spiffe_socket),
            audiences=[audience],
        )
        self._audience = audience

    def fetch_actor_token(self) -> str:
        # Returns a fresh JWT-SVID; never cached past exp.
        svid = self._source.get_jwt_svid()
        return svid.token  # raw JWT string

    def close(self):
        self._source.close()
```

The signer is **not** asked to "sign" anything — SPIRE Agent has the private key; pyspiffe brokers the SVID. The signer's only job is to obtain a fresh SVID per exchange and refuse to return one whose `exp` has already passed (pyspiffe enforces this internally; the SDK does not implement its own clock).

If pyspiffe turns out to be incompatible, the same class is reimplemented against the raw gRPC Workload API (proto from `spiffe/spire-api-sdk`) with no caller-visible change.

### TEC's `identity.py` (deleted)

```python
# sdks/agent-py/bonafide_agent/identity.py — DELETED
```

The on-disk-key signing path is gone. The Vault KV stub read in `client.fetch_connection()` is untouched — VSA owns that swap.

---

## Calendar container

The calendar container gets a `bonafide.workload=calendar` label and a workload-api socket mount. In this slice the calendar **holds** a SPIFFE ID (`spiffe://bonafide.local/service/calendar`) but does not present it to anyone. SAN-6 will keep this contract; mTLS at the resource is out of scope for the MVP.

The calendar's `BONAFIDE_RESOURCE_AUDIENCE` (the expected `aud` on the task token) is unchanged: still `http://calendar.bonafide.local:9000`.

---

## docker-compose additions

```yaml
services:
  spire-server:
    image: ghcr.io/spiffe/spire-server:1.10.0
    command: ["server", "run", "-config", "/etc/spire/server/server.conf"]
    volumes:
      - ./deploy/spire/spire-server.conf:/etc/spire/server/server.conf:ro
      - spire-server-data:/opt/spire/data
    ports: ["8081:8081"]

  spire-agent:
    image: ghcr.io/spiffe/spire-agent:1.10.0
    command: ["agent", "run", "-config", "/etc/spire/agent/agent.conf"]
    volumes:
      - ./deploy/spire/spire-agent.conf:/etc/spire/agent/agent.conf:ro
      - spire-agent-sockets:/run/spire/sockets
      - /var/run/docker.sock:/var/run/docker.sock:ro
    depends_on: [spire-server]

  authz:
    # ... existing config from TEC ...
    labels: { bonafide.workload: authz }
    volumes:
      - spire-agent-sockets:/run/spire/sockets:ro
      # ... other TEC volumes ...

  calendar:
    # ... existing config from TEC ...
    labels: { bonafide.workload: calendar }
    volumes:
      - spire-agent-sockets:/run/spire/sockets:ro

  # agent (one-shot) gets the same label + volume in scripts/smoke.sh's docker run flags.

volumes:
  spire-server-data:
  spire-agent-sockets:
```

The `spire-agent-sockets` named volume is the workload-api UDS shared among authz, calendar, and agent containers. Read-only mounts everywhere; SPIRE Agent is the only writer.

---

## Bootstrap script additions

`scripts/bootstrap.sh` (additions in order):

```bash
# After docker compose up --wait (TEC step):

# 1. Generate the SPIRE Agent's bootstrap CA + cert pair if missing.
#    (x509pop attestor needs a CA the server trusts and a cert the agent presents.)
./deploy/spire/generate-agent-ca.sh           # idempotent

# 2. Wait for spire-server to be healthy.
docker compose exec -T spire-server spire-server healthcheck

# 3. Create registration entries.
./deploy/spire/registrations.sh

# 4. Smoke-verify the OIDC discovery endpoint.
curl -fsSL http://spire-server:8081/.well-known/openid-configuration > /dev/null
```

The dev key generation (TEC steps 1-2) is preserved for the user JWT signing key only; the per-agent dev keys generated by TEC are deleted. If `deploy/spire-stub/` still exists from a TEC checkout, the bootstrap removes it with a one-line warning.

---

## Smoke harness — SWI block (TEC-11 plus SWI-8)

Appended after the TEC block:

```bash
#--- SWI block -----------------------------------------------------------------
echo "[smoke:SWI] verifying actor_token is SPIRE-issued..."

# Re-mint a user JWT (TEC block left it set in $USER_JWT).
# Run demo-agent and capture its actor_token (the SDK exposes it via a debug flag).
ACTOR_TOKEN=$(docker compose run --rm demo-agent python -m demo_agent \
    --user-jwt "$USER_JWT" --print-actor-token-and-exit)

# Decode without verification and check iss + sub.
ISS=$(echo "$ACTOR_TOKEN" | jwt-cli decode --json | jq -r '.payload.iss')
SUB=$(echo "$ACTOR_TOKEN" | jwt-cli decode --json | jq -r '.payload.sub')

test "$ISS" = "https://bonafide.local"  # SPIRE OIDC discovery issuer
test "$SUB" = "spiffe://bonafide.local/agent/planner"

echo "[smoke:SWI] removing planner registration -> next exchange must fail closed..."
docker compose exec -T spire-server spire-server entry delete \
    -spiffeID spiffe://bonafide.local/agent/planner

# Give the workload-api stream a moment to propagate the removal.
sleep 2

OUT=$(docker compose run --rm demo-agent python -m demo_agent \
    --user-jwt "$USER_JWT" 2>&1 || true)
echo "$OUT" | grep -qi "no svid" || echo "$OUT" | grep -q "401\|access_denied"

echo "[smoke:SWI] re-create the entry for the next slice/run..."
./deploy/spire/registrations.sh

echo "[smoke:SWI] OK"
#--- end SWI block --------------------------------------------------------------
```

The TEC block still runs first and must still pass; the SWI block depends on a successful TEC outcome being reproducible.

---

## Open decisions resolved here

- **pyspiffe vs. raw gRPC for Python.** Default: pyspiffe. The task that adds `identity_spire.py` opens with a one-line `pip install pyspiffe` smoke test; on a failure the same class is reimplemented against the SPIRE Workload API proto. The decision is recorded as a one-paragraph note in this file's "Files created" log when build lands.
- **SPIRE node attestor for dev: `x509pop`.** Simplest CA-rooted attestor; one bootstrap CA + per-agent cert. Easier than `join_token` (which would require manual token regeneration after each compose down/up) and more realistic than `disk`/`uds`-only attestors. The agent's cert is generated by `deploy/spire/generate-agent-ca.sh` and is gitignored.
- **SPIRE workload attestor for dev: `docker`.** Selectors by `docker:image_id:<sha>` and `docker:label:bonafide.workload=<name>`. The image ID is tamper-evident; the label is the human-readable handshake. K8s-style attestors are out of scope.
- **JWT-SVID TTL: 5 minutes (`default_jwt_svid_ttl = 5m`).** Matches DESIGN.md §4 directly. The agent SDK never re-uses an SVID past its `exp`; pyspiffe enforces this and the SDK does not implement its own clock.
- **Agent-SDK endpoint discovery (DESIGN.md §6 open decision): config-driven.** Endpoint URLs continue to come from environment variables. SPIRE federation registry-based discovery is a post-MVP idea; no slice in the MVP owns it.
- **OIDC discovery endpoint URL.** `http://spire-server:8081/.well-known/openid-configuration` inside the compose network. VSA's contingency-path config copies this URL verbatim into Vault's JWT auth `oidc_discovery_url`.
- **What happens to `policy.yaml` and the policy gate.** Untouched. The in-memory map still keys on actor SPIFFE ID strings, and now those strings come from SPIRE-issued SVIDs instead of dev-trusted ones. OPE owns the Rego swap.
- **No retroactive change to `act_chain.go`.** TEC's tests still cover depth-1 and depth-2 (with placeholder SPIFFE IDs). SWI does not extend the test file. SAN does.

---

## Files created / modified / deleted (lands in T-NN, owned by tasks.md)

| File | Change |
|---|---|
| `deploy/spire/spire-server.conf` | New |
| `deploy/spire/spire-agent.conf` | New |
| `deploy/spire/registrations.sh` | New |
| `deploy/spire/generate-agent-ca.sh` | New |
| `services/authz/internal/trust/workloadapi.go` | New |
| `services/authz/internal/trust/trust.go` (interface) | Unchanged |
| `services/authz/internal/trust/static.go` (TEC YAML impl) | Deleted |
| `services/authz/cmd/authz/main.go` | Updated to construct `trust.NewFromWorkloadAPI(...)` instead of the YAML impl |
| `sdks/agent-py/bonafide_agent/identity_spire.py` | New |
| `sdks/agent-py/bonafide_agent/identity.py` | Deleted |
| `sdks/agent-py/bonafide_agent/client.py` | Constructor swap (`key_path` → `spiffe_socket`) |
| `apps/demo-agent/demo_agent/__main__.py` | Updated to pass `spiffe_socket` instead of key path; `--print-actor-token-and-exit` added for smoke |
| `deploy/spire-stub/` | Deleted |
| `deploy/authz/actor-trust.yaml` | Deleted |
| `docker-compose.yml` | spire-server + spire-agent added; workload labels + socket mounts on authz/calendar/agent |
| `scripts/bootstrap.sh` | SPIRE bring-up + registrations + OIDC discovery smoke step appended |
| `scripts/smoke.sh` | SWI block appended |
| `.gitignore` | Adds `deploy/spire/data/*` (datastore) and the agent CA materials |

---

## Out of scope for this slice (see requirements.md for the slice-wide list)

- Any rewiring of the policy gate. OPE owns the Rego swap; the policy gate still consults the YAML map in this slice.
- Vault wiring. VSA owns the SPIFFE auth method.
- Audit shape changes. The file-backed emitter from TEC still runs.
- Resource-side mTLS. The calendar holds an SVID but presents nothing at the wire.
- Multi-trust-domain federation. Single trust domain only.
- SPIRE upgrades during compose lifetime. Restart is the only "update" mechanism.

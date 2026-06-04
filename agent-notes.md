# Agent failure notes

Failure modes encountered while building bonafide. Documented before being fixed, so the fix doesn't erase the evidence. One section per incident.

Format:

```
## YYYY-MM-DD — <one-line description>

**Symptom.** What we observed.
**Diagnosis.** What was actually wrong.
**Fix.** What changed.
**Carries forward.** What this taught us about how to spec or test the next slice.
```

---

## 2026-06-03 — `go mod why` does not traverse build-tagged tools.go files

**Symptom.** TEC T-02 specified `go mod why github.com/zitadel/oidc/v3` as the verification that the dependency is direct. With the canonical Go idiom for pre-import dependency pinning — a `tools.go` file under `//go:build tools` blank-importing the five planned data-plane deps — the command reports `(main module does not need package github.com/zitadel/oidc/v3)` even though `go.mod` lists zitadel/oidc in the un-indirected `require` block.

**Diagnosis.** `go mod why` evaluates the import graph under the *default* build context. Files guarded by a non-default build tag (`tools`) are excluded from that traversal. The deps are correctly direct in `go.mod` — `go mod tidy` honors the tagged imports when computing the require set — but `go mod why` cannot see the trace. The same constraint blocks every other pre-import pinning approach short of importing the deps under a default tag (which forces side-effecting `init()` from logrus / OTel / etc. into the runtime).

**Fix.** Amended T-02's `Verified when:` clause to use `go list -m -f '{{.Indirect}}' github.com/zitadel/oidc/v3` which prints `false` for direct deps and `true` for transitives — the semantic check the spec intended. tools.go stays; it shrinks as T-05 (testify), T-06 (go-jose), T-10 (chi+uuid), T-11 (zitadel/oidc) bring in real callers, and is deleted entirely no later than T-13.

**Carries forward.** Any future verification command that "queries the module graph" needs to be tested against the canonical idiom for the slice's state at the time of the check, not the end-state. Specifically: verifications written before T-13 should prefer `go list -m` over `go mod why` for direct/indirect questions. The same caution applies to Python `pip show` vs `importlib.metadata` for unimported deps in SDK slices.

---

## 2026-06-04 — `BONAFIDE_AUTHZ_ISSUER` drift between CONTRACT.md and design.md

**Symptom.** T-14's verification clause flagged that `CONTRACT.md` §§4, 5 require `iss = aud = https://authz.bonafide.local` for every bonafide-minted JWT, while `specs/token-exchange-core/design.md` "Configuration" tables showed the env-var default as `http://authz.bonafide.local:8080`. T-13 landed the design.md form into `internal/config` as the runtime default; if T-14 used the design.md form, every minted user JWT would fail CONTRACT.md §4.

**Diagnosis.** `iss` and `aud` are wire-format claim values, not transport URLs. `CONTRACT.md` is the authoritative source per `CLAUDE.md` ("`CONTRACT.md` is sacred"). The design.md tables conflated the canonical issuer identifier with the dev-mode HTTP listener URL. The two are independent: `BONAFIDE_AUTHZ_TOKEN_URL` and `BONAFIDE_AUTHZ_JWKS_URL` already exist as separate env vars in design.md for the actual transport, so the issuer var can be the canonical form without breaking dev bring-up.

**Fix.** Updated `internal/config` default for `BONAFIDE_AUTHZ_ISSUER` to `https://authz.bonafide.local` (matching CONTRACT.md §§4, 5). Updated `design.md` "Configuration" tables to advertise the canonical form. The HTTP listener still binds at `:8080`; the discovery doc still serves from the dev port but advertises canonical URLs for `issuer` and `jwks_uri` (which is exactly how production OIDC works behind a reverse proxy). The resource SDK reads `BONAFIDE_AUTHZ_JWKS_URL` directly, not via discovery, so dev bring-up is unaffected.

**Carries forward.** Whenever a design.md table specifies a "default value" for a wire-format identifier (issuer, audience, SPIFFE trust domain root), cross-check it against `CONTRACT.md` before landing. Wire-format identifiers and transport URLs are conceptually separate; conflating them in env-var defaults silently bakes wire bugs into the runtime.


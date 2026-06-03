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


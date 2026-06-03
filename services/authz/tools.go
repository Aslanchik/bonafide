//go:build tools

// Package authz at the module root carries the build-tagged tools.go file
// that pins the Go data-plane dependency set from
// specs/token-exchange-core/design.md "Go data plane" before the real
// callers come online (go-jose in T-06, chi+uuid in T-10, zitadel/oidc in
// T-11). Each blank import is removed when its real caller lands; the
// file vanishes once every dependency has a non-tooling import (no later
// than T-13). testify was removed in T-05 when act_chain_test.go brought
// it in as a real test-file importer.
package authz

import (
	_ "github.com/go-chi/chi/v5"
	_ "github.com/go-jose/go-jose/v4"
	_ "github.com/google/uuid"
	_ "github.com/zitadel/oidc/v3/pkg/op"
)

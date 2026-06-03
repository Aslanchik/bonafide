// Package policy is the policy-gate seam. In M1 it owns a YAML-backed
// allow-table (MapGate); Slice 4 (opa-policy-engine) deletes MapGate and
// the embedded scope regex and replaces them with an OPA-Rego engine
// behind the same Gate interface. The exchange handler depends only on
// the interface.
//
// Fail-closed posture (CLAUDE.md "Safety constraints"):
//
//   - A scope that does not match CONTRACT.md §2's grammar is denied with
//     Reason="unknown_scope" before any allow-entry comparison runs.
//   - Anything not matched by an explicit allow entry is denied with
//     Reason="no_matching_allow_entry". There is no wildcard, no
//     hierarchy, no permissive default.
//   - A MapGate with zero entries denies every request — and the loader
//     accepts an empty `allow:` list so a fresh deploy is locked down,
//     not opened up.
package policy

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	yaml "gopkg.in/yaml.v3"

	"bonafide.local/services/authz/internal/exchange"
)

// scopePattern is the M1 mirror of CONTRACT.md §2's scope grammar.
// OPE-T07 deletes this regex along with MapGate; the Rego policy
// becomes the single source of truth for scope validity.
var scopePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*:(read|write|admin):[^\s:]+$`)

// Gate is the contract the token-exchange handler depends on. M1's
// MapGate satisfies it; OPE's RegoGate satisfies it without any
// caller change.
type Gate interface {
	Decide(input exchange.PolicyInput) exchange.PolicyDecision
}

type allowEntry struct {
	Actor         string `yaml:"actor"`
	SubjectPrefix string `yaml:"subject_prefix"`
	Scope         string `yaml:"scope"`
	Audience      string `yaml:"audience"`
}

// MapGate is the M1 in-memory allow-table. Loaded once at startup;
// safe for concurrent reads; never mutated thereafter.
type MapGate struct {
	entries []allowEntry
}

type yamlFile struct {
	Allow []allowEntry `yaml:"allow"`
}

// LoadMapGate reads policy.yaml at path and returns a MapGate.
// A missing file is a startup error (fail closed); the caller exits
// non-zero before serving traffic.
func LoadMapGate(path string) (*MapGate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("policy: read %q: %w", path, err)
	}
	var doc yamlFile
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("policy: parse YAML at %q: %w", path, err)
	}
	for i, e := range doc.Allow {
		if e.Actor == "" || e.SubjectPrefix == "" || e.Scope == "" || e.Audience == "" {
			return nil, fmt.Errorf("policy: entry %d in %q is missing actor/subject_prefix/scope/audience", i, path)
		}
	}
	return &MapGate{entries: doc.Allow}, nil
}

// NewMapGate constructs a MapGate from in-memory entries. Used by
// tests; production loads from disk via LoadMapGate.
func NewMapGate(entries []allowEntry) *MapGate {
	cp := make([]allowEntry, len(entries))
	copy(cp, entries)
	return &MapGate{entries: cp}
}

// Decide implements the M1 policy gate. The grammar precheck on
// input.Scope runs before any allow-entry comparison so a malformed
// scope always returns unknown_scope (never silently denied as
// no_matching_allow_entry, which would mask a client bug).
func (g *MapGate) Decide(input exchange.PolicyInput) exchange.PolicyDecision {
	if !scopePattern.MatchString(input.Scope) {
		return exchange.PolicyDecision{Allowed: false, Reason: "unknown_scope"}
	}
	for _, e := range g.entries {
		if e.Actor != input.Actor {
			continue
		}
		if !strings.HasPrefix(input.Subject, e.SubjectPrefix) {
			continue
		}
		if e.Scope != input.Scope {
			continue
		}
		if e.Audience != input.Audience {
			continue
		}
		return exchange.PolicyDecision{Allowed: true, ScopeGrant: input.Scope}
	}
	return exchange.PolicyDecision{Allowed: false, Reason: "no_matching_allow_entry"}
}

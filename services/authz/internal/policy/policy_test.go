package policy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"bonafide.local/services/authz/internal/exchange"
)

const (
	planner   = "spiffe://bonafide.local/agent/planner"
	humanPfx  = "spiffe://bonafide.local/human/"
	aliceSub  = "spiffe://bonafide.local/human/alice@example.com"
	calScope  = "calendar:read:alice@example.com"
	calAud    = "http://calendar.bonafide.local:9000"
)

func exampleEntries() []allowEntry {
	return []allowEntry{{
		Actor:         planner,
		SubjectPrefix: humanPfx,
		Scope:         calScope,
		Audience:      calAud,
	}}
}

func inputFor(actor, subject, scope, audience string) exchange.PolicyInput {
	return exchange.PolicyInput{
		Subject:  subject,
		Actor:    actor,
		Scope:    scope,
		Audience: audience,
	}
}

func TestDecide_ExampleAllowEntry_AllowsWithScopeGrant(t *testing.T) {
	g := NewMapGate(exampleEntries())
	d := g.Decide(inputFor(planner, aliceSub, calScope, calAud))
	require.True(t, d.Allowed)
	require.Equal(t, calScope, d.ScopeGrant)
	require.Empty(t, d.Reason)
}

func TestDecide_NonMatchingActor_Denies(t *testing.T) {
	g := NewMapGate(exampleEntries())
	d := g.Decide(inputFor("spiffe://bonafide.local/agent/other", aliceSub, calScope, calAud))
	require.False(t, d.Allowed)
	require.Equal(t, "no_matching_allow_entry", d.Reason)
	require.Empty(t, d.ScopeGrant)
}

func TestDecide_NonMatchingAudience_Denies(t *testing.T) {
	g := NewMapGate(exampleEntries())
	d := g.Decide(inputFor(planner, aliceSub, calScope, "http://other.example.com"))
	require.False(t, d.Allowed)
	require.Equal(t, "no_matching_allow_entry", d.Reason)
}

func TestDecide_NonMatchingScope_Denies(t *testing.T) {
	g := NewMapGate(exampleEntries())
	d := g.Decide(inputFor(planner, aliceSub, "calendar:write:alice@example.com", calAud))
	require.False(t, d.Allowed)
	require.Equal(t, "no_matching_allow_entry", d.Reason)
}

func TestDecide_NonMatchingSubjectPrefix_Denies(t *testing.T) {
	g := NewMapGate(exampleEntries())
	d := g.Decide(inputFor(planner, "spiffe://bonafide.local/service/bob", calScope, calAud))
	require.False(t, d.Allowed)
	require.Equal(t, "no_matching_allow_entry", d.Reason)
}

func TestDecide_MalformedScope_ReturnsUnknownScope(t *testing.T) {
	g := NewMapGate(exampleEntries())
	cases := []string{
		"",
		"calendar",
		"calendar:read",
		"calendar:delete:alice@example.com",
		"Calendar:read:alice@example.com",
		"calendar:read:alice with space",
		"calendar:read:alice:with:colons",
	}
	for _, scope := range cases {
		t.Run(scope, func(t *testing.T) {
			d := g.Decide(inputFor(planner, aliceSub, scope, calAud))
			require.False(t, d.Allowed)
			require.Equal(t, "unknown_scope", d.Reason)
		})
	}
}

func TestDecide_EmptyMapGate_DeniesEveryRequest(t *testing.T) {
	g := NewMapGate(nil)
	d := g.Decide(inputFor(planner, aliceSub, calScope, calAud))
	require.False(t, d.Allowed)
	require.Equal(t, "no_matching_allow_entry", d.Reason)
}

func TestLoadMapGate_ReadsExampleEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	body := `allow:
  - actor: ` + planner + `
    subject_prefix: ` + humanPfx + `
    scope: ` + calScope + `
    audience: ` + calAud + `
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	g, err := LoadMapGate(path)
	require.NoError(t, err)
	d := g.Decide(inputFor(planner, aliceSub, calScope, calAud))
	require.True(t, d.Allowed)
	require.Equal(t, calScope, d.ScopeGrant)
}

func TestLoadMapGate_MissingFile_ReturnsError(t *testing.T) {
	_, err := LoadMapGate(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
}

func TestLoadMapGate_EmptyAllowList_LoadsButDeniesAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	require.NoError(t, os.WriteFile(path, []byte("allow: []\n"), 0o600))
	g, err := LoadMapGate(path)
	require.NoError(t, err)
	d := g.Decide(inputFor(planner, aliceSub, calScope, calAud))
	require.False(t, d.Allowed)
	require.Equal(t, "no_matching_allow_entry", d.Reason)
}

func TestLoadMapGate_MissingField_ReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	body := `allow:
  - actor: ` + planner + `
    subject_prefix: ` + humanPfx + `
    scope: ` + calScope + `
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	_, err := LoadMapGate(path)
	require.Error(t, err)
}

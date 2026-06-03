package exchange

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBuildAct is the canonical table-driven test of the act-chain
// nesting rule (CONTRACT.md §6.1). The test must NEVER be deleted —
// CLAUDE.md "Safety constraints" pins it in the repo. SAN-3 appends
// depth-2 demo cases against real SPIFFE IDs; this file is not
// otherwise modified after T-05.
func TestBuildAct(t *testing.T) {
	const (
		planner = "spiffe://bonafide.local/agent/planner"
		tool    = "spiffe://bonafide.local/agent/tool"
		tool2   = "spiffe://bonafide.local/agent/tool2"
	)

	tests := []struct {
		name       string
		subjectAct *Act
		current    string
		wantAct    *Act
		wantJSON   string
	}{
		{
			name:       "first hop, no prior act (TEC happy path)",
			subjectAct: nil,
			current:    planner,
			wantAct:    &Act{Sub: planner},
			wantJSON:   `{"sub":"spiffe://bonafide.local/agent/planner"}`,
		},
		{
			name:       "depth-2 nest (SAN happy path; locked in at TEC)",
			subjectAct: &Act{Sub: planner},
			current:    tool,
			wantAct:    &Act{Sub: tool, Act: &Act{Sub: planner}},
			wantJSON:   `{"sub":"spiffe://bonafide.local/agent/tool","act":{"sub":"spiffe://bonafide.local/agent/planner"}}`,
		},
		{
			name:       "depth-3 nest (asserts unbounded recursion)",
			subjectAct: &Act{Sub: tool, Act: &Act{Sub: planner}},
			current:    tool2,
			wantAct:    &Act{Sub: tool2, Act: &Act{Sub: tool, Act: &Act{Sub: planner}}},
			wantJSON:   `{"sub":"spiffe://bonafide.local/agent/tool2","act":{"sub":"spiffe://bonafide.local/agent/tool","act":{"sub":"spiffe://bonafide.local/agent/planner"}}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildAct(tc.current, tc.subjectAct)
			require.Equal(t, tc.wantAct, got, "struct shape must match")

			gotJSON, err := json.Marshal(got)
			require.NoError(t, err)
			require.Equal(t, tc.wantJSON, string(gotJSON), "wire shape must match CONTRACT.md §6.1 byte-for-byte")
		})
	}

	t.Run("defensive copy — mutating input subjectAct after BuildAct has no effect on output", func(t *testing.T) {
		subject := &Act{Sub: "spiffe://bonafide.local/agent/planner"}
		got := BuildAct("spiffe://bonafide.local/agent/tool", subject)

		subject.Sub = "spiffe://bonafide.local/agent/MUTATED"
		subject.Act = &Act{Sub: "spiffe://bonafide.local/agent/INJECTED"}

		require.Equal(t, "spiffe://bonafide.local/agent/planner", got.Act.Sub,
			"cloneAct must deep-copy: post-mint input mutation reached the output")
		require.Nil(t, got.Act.Act,
			"cloneAct must deep-copy: post-mint input injection reached the output")
	})
}

// TestFlattenChain_DepthTwo locks the audit-shape contract
// (CONTRACT.md §9 resulting_chain) at depth 2: outermost actor first,
// followed inward.
func TestFlattenChain_DepthTwo(t *testing.T) {
	act := &Act{
		Sub: "spiffe://bonafide.local/agent/tool",
		Act: &Act{Sub: "spiffe://bonafide.local/agent/planner"},
	}
	require.Equal(t, []string{
		"spiffe://bonafide.local/agent/tool",
		"spiffe://bonafide.local/agent/planner",
	}, FlattenChain(act))
}

// TestChainDepth_DepthTwo locks the policy-cap input at depth 2 — OPE
// compares this value to max_chain_depth before allowing the mint, so
// a regression here would silently widen the allowable chain.
func TestChainDepth_DepthTwo(t *testing.T) {
	subject := &Act{
		Sub: "spiffe://bonafide.local/agent/tool",
		Act: &Act{Sub: "spiffe://bonafide.local/agent/planner"},
	}
	require.Equal(t, 3, ChainDepth(subject))
}

// TestBuildActImpersonationGuardShape encodes a compile-time check on
// BuildAct's signature. Any future refactor that adds a parameter
// carrying the subject identity (e.g. subjectSub string) will fail to
// satisfy this type and surface the violation at build time, before
// any code can read or write to the subject. Protects CONTRACT.md §6.3
// by construction.
func TestBuildActImpersonationGuardShape(t *testing.T) {
	var _ func(currentActor string, subjectAct *Act) *Act = BuildAct
}

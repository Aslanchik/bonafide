// act_chain.go is the canonical implementation of the act-claim nesting rule
// defined in CONTRACT.md §6.1. It is the most important function in this
// codebase, per CLAUDE.md "The act-chain builder is the most important code
// in the project." Any change here requires a parallel update to the
// table-driven tests in act_chain_test.go; any change to the nesting rule
// requires a CONTRACT.md amendment first.
//
// Safety contract carried by this file:
//
//   - The act chain in minted task tokens must always NEST, never overwrite
//     (CLAUDE.md "Safety constraints"). BuildAct enforces this by setting
//     Act.Sub to the current actor and Act.Act to a defensive copy of the
//     subject_token's prior act subtree — never reading or mutating the
//     subject identity.
//
//   - The subject_token's `sub` is never mutated across an exchange
//     (CONTRACT.md §5, §6.3 impersonation guard). BuildAct accepts only
//     `currentActor` and `subjectAct`; the function has no parameter or
//     return value that carries the subject identity, so the safety
//     property holds by construction.
//
// SAN-3 extends the test coverage for depth-2 nesting; this function is not
// modified at that point.
package exchange

// BuildAct returns the act claim for a newly minted task token, per
// CONTRACT.md §6.1's nesting rule:
//
//   - new.act.sub = currentActor (the SPIFFE ID of the agent presenting
//     actor_token on THIS exchange)
//   - new.act.act = subjectAct   (the entire prior act subtree, deep-copied;
//     nil if the subject_token has no act)
//
// The function never reads, returns, or modifies the subject identity. The
// caller is responsible for setting the new token's top-level sub equal to
// the subject_token's top-level sub (also unmodified).
func BuildAct(currentActor string, subjectAct *Act) *Act {
	return &Act{
		Sub: currentActor,
		Act: cloneAct(subjectAct),
	}
}

// cloneAct returns a deep, value-independent copy of the supplied act
// subtree. Used by BuildAct so the subject_token's structure cannot be
// reached through the new token's act field — a guard against aliasing
// bugs that would surface as cross-token mutation under future code paths.
func cloneAct(a *Act) *Act {
	if a == nil {
		return nil
	}
	return &Act{Sub: a.Sub, Act: cloneAct(a.Act)}
}

// ChainDepth returns the depth the new task token's act chain would have
// after this exchange — 1 (the new mint hop) plus the length of the
// subject_token's existing act chain. The policy gate compares this value
// against `max_chain_depth` (OPE Slice 4 onwards; default 4) to enforce
// the chain-depth cap.
//
// Examples:
//
//	ChainDepth(nil)                                          == 1
//	ChainDepth(&Act{Sub: "planner"})                         == 2
//	ChainDepth(&Act{Sub: "tool", Act: &Act{Sub: "planner"}}) == 3
func ChainDepth(subjectAct *Act) int {
	n := 1
	for a := subjectAct; a != nil; a = a.Act {
		n++
	}
	return n
}

// FlattenChain walks the supplied act subtree from current-actor outward
// and returns the chain as a current-first slice. Used by the audit
// emitter to populate the `resulting_chain` field of an audit event
// (CONTRACT.md §9). For a nil act the result is nil (no chain present).
//
// Examples:
//
//	FlattenChain(nil)                                              == nil
//	FlattenChain(&Act{Sub: "tool", Act: &Act{Sub: "planner"}})     == []string{"tool", "planner"}
func FlattenChain(act *Act) []string {
	var chain []string
	for a := act; a != nil; a = a.Act {
		chain = append(chain, a.Sub)
	}
	return chain
}

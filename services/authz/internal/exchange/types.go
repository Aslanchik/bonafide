// Package exchange owns the RFC 8693 token-exchange handler, its supporting
// types, and the canonical act-chain builder. The wire formats declared here
// trace to CONTRACT.md §§4, 5, and 6.1; the field names in JSON tags are
// authoritative and must not drift from that document.
package exchange

// Act is the act claim — recursive type implementing CONTRACT.md §6.1's
// nesting rule. A nil Act on a parent serialises to no `act` field
// (CONTRACT.md §6.1 rule 4); construction goes through BuildAct in
// act_chain.go (T-04) so the nesting is uniform across every mint path.
type Act struct {
	Sub string `json:"sub"`
	Act *Act   `json:"act,omitempty"`
}

// TaskTokenClaims is the body of a minted task token (CONTRACT.md §5).
// Sub is always the human and is never mutated across an exchange. Act is
// required on a task token (CONTRACT.md §5) — the point of the format is
// the chain — so the tag carries no omitempty.
type TaskTokenClaims struct {
	Iss      string `json:"iss"`
	Sub      string `json:"sub"`
	Aud      string `json:"aud"`
	Iat      int64  `json:"iat"`
	Exp      int64  `json:"exp"`
	Jti      string `json:"jti"`
	Scope    string `json:"scope"`
	Act      *Act   `json:"act"`
	ClientID string `json:"client_id,omitempty"`
}

// UserJWTClaims is the body of a subject_token a user CLI mints
// (CONTRACT.md §4). Sub is `spiffe://bonafide.local/human/{email}`; email
// is optional and mirrored for human-readability.
type UserJWTClaims struct {
	Iss   string `json:"iss"`
	Sub   string `json:"sub"`
	Aud   string `json:"aud"`
	Iat   int64  `json:"iat"`
	Exp   int64  `json:"exp"`
	Jti   string `json:"jti"`
	Email string `json:"email,omitempty"`
}

// ActorTokenClaims is the body of an actor_token presented at the exchange
// in M1 (self-signed by the agent's per-workload Ed25519 key). SWI replaces
// the verification path with SPIRE's FetchJWTBundles but the struct shape
// stays compatible because the exchange handler only reads iss/sub/aud/exp.
type ActorTokenClaims struct {
	Iss string `json:"iss"`
	Sub string `json:"sub"`
	Aud string `json:"aud"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

// PolicyInput is the contract the policy gate sees on every exchange.
// OPE freezes this shape in Rego (OPE-T02); the in-memory map gate in M1
// consumes the same struct so the swap in Slice 4 is a one-line wiring
// change.
type PolicyInput struct {
	Subject       string
	SubjectClaims UserJWTClaims
	Actor         string
	ActorClaims   ActorTokenClaims
	Scope         string
	Audience      string
	ExistingChain []string
}

// PolicyDecision is the policy gate's verdict. Reason is populated only
// when Allowed is false and is surfaced verbatim in the OAuth
// error_description per RFC 6749 §5.2 and in the audit event.
type PolicyDecision struct {
	Allowed    bool
	ScopeGrant string
	Reason     string
}

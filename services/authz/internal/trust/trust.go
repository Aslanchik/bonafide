// Package trust owns the actor_token verification seam. In M1 the
// implementation is a YAML-backed static map of per-workload public
// keys (see static.go); SWI replaces it with a SPIRE-backed
// FetchJWTBundles implementation behind the same IssuerTrust
// interface. The exchange handler depends only on the interface;
// neither the handler nor act_chain.go is touched at the swap.
package trust

import "bonafide.local/services/authz/internal/exchange"

// IssuerTrust verifies an actor_token (RFC 8693 actor_token parameter)
// presented at the token-exchange endpoint and returns its claims on
// success. A non-nil error indicates the token must not be accepted;
// the exchange handler responds 400 `invalid_request` and never logs
// the rejected token's payload (CLAUDE.md "Fail closed").
type IssuerTrust interface {
	Verify(actorJWT string) (claims exchange.ActorTokenClaims, err error)
}

package exchange

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"

	"bonafide.local/services/authz/internal/audit"
	"bonafide.local/services/authz/internal/httputil"
	"bonafide.local/services/authz/internal/keys"
)

// Gate is the contract the handler needs from a policy backend.
// policy.MapGate (M1) and the future OPE RegoGate (Slice 4) both
// satisfy this interface structurally; declaring it here keeps
// exchange free of a dependency on policy, which imports exchange
// for PolicyInput/PolicyDecision.
type Gate interface {
	Decide(input PolicyInput) PolicyDecision
}

// ActorVerifier is the contract the handler needs from an
// actor_token trust backend. trust.YAMLTrust (M1) and the SWI SPIRE
// implementation (Slice 2) both satisfy it; declaring it here breaks
// the same cycle as Gate.
type ActorVerifier interface {
	Verify(actorJWT string) (ActorTokenClaims, error)
}

// RFC 8693 / CONTRACT.md §7 protocol constants. Stringified once here
// so a typo cannot diverge from the wire format.
const (
	grantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	tokenTypeJWT           = "urn:ietf:params:oauth:token-type:jwt"
	tokenTypeBearer        = "Bearer"
)

// taskTokenAbsoluteCeilingSeconds is the CONTRACT.md §5 task-token
// TTL ceiling. The handler caps Exp at this regardless of configuration
// so a misconfigured BONAFIDE_AUTHZ_TASK_TOKEN_TTL_SECONDS can never
// extend a token past the spec maximum (CLAUDE.md "No code path may
// extend these").
const taskTokenAbsoluteCeilingSeconds = 300

// Settings carries the handler's per-process configuration. T-13's
// config loader populates it; tests construct it directly.
//
// Now is the time source used for iat/exp computation and for the
// strict subject_token exp check (no leeway per DESIGN.md §4). It is
// injectable so tests can drive the clock without sleeping. A zero
// value means time.Now.
type Settings struct {
	Issuer              string
	TaskTokenTTLSeconds int
	Now                 func() time.Time
}

// Handler returns the http.HandlerFunc for POST /token (CONTRACT.md
// §§7, 8). The function captures its dependencies — the four
// interfaces and the settings — once at construction so the request
// hot path performs no lookups, no locks, and no allocations beyond
// those that are intrinsic to parsing the request and signing the
// response.
//
// The 14-step flow tracks design.md "Exchange handler flow" exactly;
// every error path returns through httputil.WriteOAuthError so the
// 5xx surface stays minimal (CONTRACT.md §7: "The handler never
// responds with HTTP 5xx for a malformed request").
func Handler(gate Gate, verifier ActorVerifier, signer *keys.Signer, emitter audit.Emitter, settings Settings) http.HandlerFunc {
	now := settings.Now
	if now == nil {
		now = time.Now
	}
	ttlSeconds := settings.TaskTokenTTLSeconds
	if ttlSeconds <= 0 || ttlSeconds > taskTokenAbsoluteCeilingSeconds {
		ttlSeconds = taskTokenAbsoluteCeilingSeconds
	}

	return func(w http.ResponseWriter, r *http.Request) {
		started := now()

		if err := r.ParseForm(); err != nil {
			httputil.WriteOAuthError(w, httputil.OAuthInvalidRequest, "malformed form body")
			return
		}

		// Step 1+2: grant_type and required parameters.
		if got := r.PostFormValue("grant_type"); got != grantTypeTokenExchange {
			httputil.WriteOAuthError(w, httputil.OAuthInvalidRequest, "grant_type must be "+grantTypeTokenExchange)
			return
		}
		required := []string{
			"subject_token", "subject_token_type",
			"actor_token", "actor_token_type",
			"requested_token_type", "audience", "scope",
		}
		for _, name := range required {
			if r.PostFormValue(name) == "" {
				httputil.WriteOAuthError(w, httputil.OAuthInvalidRequest, "missing required parameter: "+name)
				return
			}
		}

		// Step 3+4: token-type-URI checks.
		if r.PostFormValue("requested_token_type") != tokenTypeJWT {
			httputil.WriteOAuthError(w, httputil.OAuthInvalidRequest, "requested_token_type must be "+tokenTypeJWT)
			return
		}
		if r.PostFormValue("subject_token_type") != tokenTypeJWT {
			httputil.WriteOAuthError(w, httputil.OAuthInvalidRequest, "subject_token_type must be "+tokenTypeJWT)
			return
		}
		if r.PostFormValue("actor_token_type") != tokenTypeJWT {
			httputil.WriteOAuthError(w, httputil.OAuthInvalidRequest, "actor_token_type must be "+tokenTypeJWT)
			return
		}

		subjectJWT := r.PostFormValue("subject_token")
		actorJWT := r.PostFormValue("actor_token")
		audience := r.PostFormValue("audience")
		requestedScope := r.PostFormValue("scope")

		// Step 5: decode + verify subject_token against the authz signer
		// (the authz signed it via the demo-human CLI; design.md "JWT
		// signing key"). Strict exp; no leeway per DESIGN.md §4.
		subjectClaims, err := verifySubjectToken(subjectJWT, signer, settings.Issuer, now())
		if err != nil {
			httputil.WriteOAuthError(w, httputil.OAuthInvalidGrant, err.Error())
			return
		}

		// Step 6: subject_token must not carry an `act` claim
		// (CONTRACT.md §4). This is a request-shape error, not a
		// signature error → invalid_request.
		if subjectClaims.hasAct {
			httputil.WriteOAuthError(w, httputil.OAuthInvalidRequest, "subject_token must not carry act on first hop")
			return
		}

		// Step 7: verify actor_token via the trust seam.
		actorClaims, err := verifier.Verify(actorJWT)
		if err != nil {
			httputil.WriteOAuthError(w, httputil.OAuthInvalidRequest, "actor_token verification failed")
			return
		}

		// Step 8: policy. UserJWTClaims has no Act field by design
		// (CONTRACT.md §4 forbids it); the hasAct check above guarantees
		// the rejected-act case never reaches here, so existingChain is
		// always empty in TEC. SAN keeps the variable; sub-agent flows
		// populate it from the subject task token in Slice 6.
		var existingChain []string
		decision := gate.Decide(PolicyInput{
			Subject:       subjectClaims.UserJWTClaims.Sub,
			SubjectClaims: subjectClaims.UserJWTClaims,
			Actor:         actorClaims.Sub,
			ActorClaims:   actorClaims,
			Scope:         requestedScope,
			Audience:      audience,
			ExistingChain: existingChain,
		})
		if !decision.Allowed {
			emitDeniedAudit(emitter, settings.Issuer, subjectClaims.UserJWTClaims.Sub, actorClaims.Sub,
				audience, requestedScope, decision.Reason, existingChain, now())
			code := httputil.OAuthAccessDenied
			if decision.Reason == "unknown_scope" {
				code = httputil.OAuthInvalidScope
			}
			httputil.WriteOAuthError(w, code, decision.Reason)
			slog.Info("event=token_exchange",
				"outcome", "denied",
				"subject", subjectClaims.UserJWTClaims.Sub,
				"actor", actorClaims.Sub,
				"scope", requestedScope,
				"aud", audience,
				"reason", decision.Reason,
				"duration_ms", time.Since(started).Milliseconds(),
			)
			return
		}

		// Steps 9–12: build the act chain, build claims, sign. The
		// subject act is nil on every TEC mint because the user JWT
		// must not carry act (CONTRACT.md §4); BuildAct handles nil
		// uniformly.
		taskAct := BuildAct(actorClaims.Sub, nil)
		iat := now().Unix()
		exp := iat + int64(ttlSeconds)
		jti := uuid.NewString()
		claims := TaskTokenClaims{
			Iss:      settings.Issuer,
			Sub:      subjectClaims.UserJWTClaims.Sub,
			Aud:      audience,
			Iat:      iat,
			Exp:      exp,
			Jti:      jti,
			Scope:    decision.ScopeGrant,
			Act:      taskAct,
			ClientID: actorClaims.Sub,
		}
		claimsMap, err := structToMap(claims)
		if err != nil {
			httputil.WriteServerError(w, fmt.Errorf("marshal task token claims: %w", err))
			return
		}
		signed, err := signer.Sign(map[string]any{"typ": "JWT"}, claimsMap)
		if err != nil {
			httputil.WriteServerError(w, fmt.Errorf("sign task token: %w", err))
			return
		}

		// Step 13: minted audit event.
		emitMintedAudit(emitter, settings.Issuer, claims, existingChain, now())

		// Step 14: RFC 8693 §2.2 response body.
		httputil.WriteJSON(w, http.StatusOK, map[string]any{
			"access_token":      signed,
			"issued_token_type": tokenTypeJWT,
			"token_type":        tokenTypeBearer,
			"expires_in":        exp - iat,
			"scope":             decision.ScopeGrant,
		})

		slog.Info("event=token_exchange",
			"outcome", "minted",
			"subject", claims.Sub,
			"actor", actorClaims.Sub,
			"scope", claims.Scope,
			"aud", claims.Aud,
			"jti", claims.Jti,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	}
}

// subjectVerifyResult bundles the decoded subject_token claims with a
// boolean for whether the raw JSON carried an `act` claim. The raw
// flag exists because UserJWTClaims has no Act field by design
// (CONTRACT.md §4 forbids it) — but the handler must still detect the
// forbidden-claim case to return the precise CONTRACT.md §4 error
// rather than silently dropping the field.
type subjectVerifyResult struct {
	UserJWTClaims UserJWTClaims
	hasAct        bool
}

// verifySubjectToken parses the subject_token, checks alg=EdDSA, kid
// matches the authz signer's kid, verifies the Ed25519 signature with
// the signer's public key, decodes the claims, and validates iss/aud/
// exp/iat. Every failure path returns an error suitable for use as
// the RFC 6749 error_description; none of them is a 5xx.
func verifySubjectToken(jwt string, signer *keys.Signer, expectedIssuer string, now time.Time) (subjectVerifyResult, error) {
	var zero subjectVerifyResult
	sig, err := jose.ParseSigned(jwt, []jose.SignatureAlgorithm{jose.EdDSA})
	if err != nil {
		return zero, fmt.Errorf("subject_token: %s", err)
	}
	if len(sig.Signatures) != 1 {
		return zero, errors.New("subject_token must carry exactly one signature")
	}
	if sig.Signatures[0].Header.KeyID != signer.KID() {
		return zero, fmt.Errorf("subject_token kid %q does not match authz signing key", sig.Signatures[0].Header.KeyID)
	}
	payload, err := sig.Verify(signer.PublicKey())
	if err != nil {
		return zero, fmt.Errorf("subject_token signature mismatch: %s", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return zero, fmt.Errorf("subject_token claims decode: %s", err)
	}
	_, hasAct := raw["act"]
	var claims UserJWTClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return zero, fmt.Errorf("subject_token claims decode: %s", err)
	}
	if claims.Iss == "" || claims.Aud == "" || claims.Sub == "" || claims.Iat == 0 || claims.Exp == 0 {
		return zero, errors.New("subject_token is missing one of iss/sub/aud/iat/exp")
	}
	if claims.Iss != expectedIssuer {
		return zero, fmt.Errorf("subject_token iss %q does not match expected %q", claims.Iss, expectedIssuer)
	}
	if claims.Aud != expectedIssuer {
		return zero, fmt.Errorf("subject_token aud %q does not match expected %q", claims.Aud, expectedIssuer)
	}
	if now.Unix() >= claims.Exp {
		return zero, errors.New("subject_token expired")
	}
	return subjectVerifyResult{UserJWTClaims: claims, hasAct: hasAct}, nil
}

// structToMap is a thin reflection-free converter via JSON. The
// signer takes a map so its caller can inject typ/kid header fields;
// the typed TaskTokenClaims is the source of truth for field names.
func structToMap(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func emitMintedAudit(emitter audit.Emitter, issuer string, claims TaskTokenClaims, existing []string, now time.Time) {
	if existing == nil {
		existing = []string{}
	}
	scopeGranted := claims.Scope
	jti := claims.Jti
	tokenExp := time.Unix(claims.Exp, 0).UTC().Format(time.RFC3339Nano)
	emitter.Emit(audit.Event{
		SchemaVersion:  audit.SchemaVersion,
		EventID:        claims.Jti,
		OccurredAt:     now.UTC().Format(time.RFC3339Nano),
		Outcome:        "minted",
		Issuer:         issuer,
		Subject:        claims.Sub,
		Actor:          claims.ClientID,
		ExistingChain:  existing,
		ResultingChain: FlattenChain(claims.Act),
		Audience:       claims.Aud,
		ScopeRequested: claims.Scope,
		ScopeGranted:   &scopeGranted,
		PolicyReason:   nil,
		TokenJTI:       &jti,
		TokenExp:       &tokenExp,
	})
}

func emitDeniedAudit(emitter audit.Emitter, issuer, subject, actor, audience, scope, reason string, existing []string, now time.Time) {
	if existing == nil {
		existing = []string{}
	}
	emitter.Emit(audit.Event{
		SchemaVersion:  audit.SchemaVersion,
		EventID:        uuid.NewString(),
		OccurredAt:     now.UTC().Format(time.RFC3339Nano),
		Outcome:        "denied",
		Issuer:         issuer,
		Subject:        subject,
		Actor:          actor,
		ExistingChain:  existing,
		ResultingChain: nil,
		Audience:       audience,
		ScopeRequested: scope,
		ScopeGranted:   nil,
		PolicyReason:   &reason,
		TokenJTI:       nil,
		TokenExp:       nil,
	})
}

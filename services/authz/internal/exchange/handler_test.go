package exchange

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"bonafide.local/services/authz/internal/audit"
	"bonafide.local/services/authz/internal/keys"
)

const (
	testIssuer     = "https://authz.bonafide.local"
	testAudience   = "http://calendar.bonafide.local:9000"
	testHumanSub   = "spiffe://bonafide.local/human/alice@example.com"
	testAgentSub   = "spiffe://bonafide.local/agent/planner"
	testScope      = "calendar:read:alice@example.com"
	testFixedEpoch = int64(1_750_000_000) // fixed clock for deterministic exp/iat
)

// fakeGate is a hand-rolled stub of the Gate interface. The handler
// depends only on the interface; tests use this stub to drive every
// allow/deny branch without dragging in the policy package's YAML
// loader. The handler does not import policy, so there is no risk of
// the stub diverging from the production gate's contract on the
// fields the handler inspects (Allowed, ScopeGrant, Reason).
type fakeGate struct {
	allow      bool
	scopeGrant string
	reason     string
}

func (g fakeGate) Decide(_ PolicyInput) PolicyDecision {
	return PolicyDecision{Allowed: g.allow, ScopeGrant: g.scopeGrant, Reason: g.reason}
}

type fakeVerifier struct {
	claims ActorTokenClaims
	err    error
}

func (v fakeVerifier) Verify(_ string) (ActorTokenClaims, error) {
	return v.claims, v.err
}

// memoryEmitter is an in-memory audit.Emitter for assertions. Tests
// do not exercise the FileEmitter's drop semantics here; that
// behaviour is covered in audit/file_test.go.
type memoryEmitter struct {
	mu     sync.Mutex
	events []audit.Event
}

func (e *memoryEmitter) Emit(ev audit.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
}

func (e *memoryEmitter) Snapshot() []audit.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]audit.Event, len(e.events))
	copy(out, e.events)
	return out
}

// loadTestSigner generates a fresh Ed25519 keypair, writes the PKCS#8
// PEM to a temp file, and returns a Signer loaded from it. The same
// key is used to sign subject_tokens in tests because the production
// handler verifies subject_tokens against the authz signer (the demo-
// human CLI is a sibling of the authz binary that uses the same key).
func loadTestSigner(t *testing.T) *keys.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "signing.key")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600))
	signer, err := keys.LoadSigner(path)
	require.NoError(t, err)
	return signer
}

// signSubjectToken mints a user-JWT-shaped token signed by the authz
// signer (CONTRACT.md §4 + design.md "JWT signing key"). The extras
// map lets a test override or augment claims (e.g. inject an `act` to
// trigger the §4 rejection). Pass extras["__omit"]=key to drop a
// required claim.
func signSubjectToken(t *testing.T, signer *keys.Signer, sub string, extras map[string]any) string {
	t.Helper()
	claims := map[string]any{
		"iss": testIssuer,
		"sub": sub,
		"aud": testIssuer,
		"iat": testFixedEpoch,
		"exp": testFixedEpoch + 600,
		"jti": uuid.NewString(),
	}
	if extras != nil {
		if omit, ok := extras["__omit"].(string); ok {
			delete(claims, omit)
		}
		for k, v := range extras {
			if k == "__omit" {
				continue
			}
			if v == nil {
				delete(claims, k)
				continue
			}
			claims[k] = v
		}
	}
	jwt, err := signer.Sign(map[string]any{"typ": "JWT"}, claims)
	require.NoError(t, err)
	return jwt
}

// newHandler wires up a Handler with a fixed clock at testFixedEpoch.
// Sub-tests adjust the gate, verifier, or emitter as needed.
func newHandler(t *testing.T, signer *keys.Signer, gate Gate, verifier ActorVerifier, emitter audit.Emitter) http.HandlerFunc {
	t.Helper()
	return Handler(gate, verifier, signer, emitter, Settings{
		Issuer:              testIssuer,
		TaskTokenTTLSeconds: 300,
		Now: func() time.Time {
			return time.Unix(testFixedEpoch, 0).UTC()
		},
	})
}

func validFormValues(subjectJWT, actorJWT string) url.Values {
	return url.Values{
		"grant_type":           {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"subject_token":        {subjectJWT},
		"subject_token_type":   {"urn:ietf:params:oauth:token-type:jwt"},
		"actor_token":          {actorJWT},
		"actor_token_type":     {"urn:ietf:params:oauth:token-type:jwt"},
		"requested_token_type": {"urn:ietf:params:oauth:token-type:jwt"},
		"audience":             {testAudience},
		"scope":                {testScope},
	}
}

func post(handler http.HandlerFunc, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

func decodeOAuthError(t *testing.T, body io.Reader) map[string]string {
	t.Helper()
	raw, err := io.ReadAll(body)
	require.NoError(t, err)
	var got map[string]string
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Len(t, got, 2, "RFC 6749 §5.2 body must have exactly two keys")
	return got
}

// allowGate is the default fake-policy that accepts every well-formed
// request. Used by tests that aren't focused on the policy branch.
var allowGate = fakeGate{allow: true, scopeGrant: testScope}

// validActorClaims is the fake-verifier output for every happy-path
// test. Tests that target the verifier branch override the verifier.
var validActorClaims = ActorTokenClaims{
	Iss: testAgentSub,
	Sub: testAgentSub,
	Aud: testIssuer,
	Iat: testFixedEpoch,
	Exp: testFixedEpoch + 60,
}

func TestHandler_RequiredParameters(t *testing.T) {
	signer := loadTestSigner(t)
	subject := signSubjectToken(t, signer, testHumanSub, nil)
	emitter := &memoryEmitter{}
	handler := newHandler(t, signer, allowGate, fakeVerifier{claims: validActorClaims}, emitter)

	cases := []struct {
		name     string
		mutation func(url.Values)
		wantCode string
	}{
		{"missing grant_type", func(f url.Values) { f.Del("grant_type") }, "invalid_request"},
		{"wrong grant_type", func(f url.Values) { f.Set("grant_type", "client_credentials") }, "invalid_request"},
		{"missing subject_token", func(f url.Values) { f.Del("subject_token") }, "invalid_request"},
		{"missing subject_token_type", func(f url.Values) { f.Del("subject_token_type") }, "invalid_request"},
		{"missing actor_token", func(f url.Values) { f.Del("actor_token") }, "invalid_request"},
		{"missing actor_token_type", func(f url.Values) { f.Del("actor_token_type") }, "invalid_request"},
		{"missing requested_token_type", func(f url.Values) { f.Del("requested_token_type") }, "invalid_request"},
		{"missing audience", func(f url.Values) { f.Del("audience") }, "invalid_request"},
		{"missing scope", func(f url.Values) { f.Del("scope") }, "invalid_request"},
		{"wrong requested_token_type opaque", func(f url.Values) {
			f.Set("requested_token_type", "urn:ietf:params:oauth:token-type:access_token")
		}, "invalid_request"},
		{"wrong subject_token_type", func(f url.Values) {
			f.Set("subject_token_type", "urn:ietf:params:oauth:token-type:access_token")
		}, "invalid_request"},
		{"wrong actor_token_type", func(f url.Values) {
			f.Set("actor_token_type", "urn:ietf:params:oauth:token-type:saml2")
		}, "invalid_request"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := validFormValues(subject, "any-actor-jwt")
			tc.mutation(form)
			rr := post(handler, form)
			require.Equal(t, http.StatusBadRequest, rr.Code)
			body := decodeOAuthError(t, rr.Body)
			require.Equal(t, tc.wantCode, body["error"])
		})
	}
}

func TestHandler_SubjectTokenFailures(t *testing.T) {
	signer := loadTestSigner(t)
	emitter := &memoryEmitter{}
	handler := newHandler(t, signer, allowGate, fakeVerifier{claims: validActorClaims}, emitter)

	otherSigner := loadTestSigner(t) // for the "wrong signature" case
	expired := signSubjectToken(t, signer, testHumanSub, map[string]any{
		"exp": testFixedEpoch - 1,
	})
	wrongIss := signSubjectToken(t, signer, testHumanSub, map[string]any{"iss": "https://other.example.com"})
	wrongAud := signSubjectToken(t, signer, testHumanSub, map[string]any{"aud": "https://other.example.com"})
	missingExp := signSubjectToken(t, signer, testHumanSub, map[string]any{"__omit": "exp"})
	wrongSig := signSubjectToken(t, otherSigner, testHumanSub, nil)

	cases := []struct {
		name        string
		subjectJWT  string
		wantCode    string
		wantDescSub string
	}{
		{"unparseable JWT", "not-a-jwt", "invalid_grant", "subject_token"},
		{"wrong signature", wrongSig, "invalid_grant", "kid"},
		{"expired exp", expired, "invalid_grant", "expired"},
		{"wrong iss", wrongIss, "invalid_grant", "iss"},
		{"wrong aud", wrongAud, "invalid_grant", "aud"},
		{"missing exp", missingExp, "invalid_grant", "missing"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := validFormValues(tc.subjectJWT, "any-actor-jwt")
			rr := post(handler, form)
			require.Equal(t, http.StatusBadRequest, rr.Code)
			body := decodeOAuthError(t, rr.Body)
			require.Equal(t, tc.wantCode, body["error"])
			require.Contains(t, body["error_description"], tc.wantDescSub)
		})
	}
}

func TestHandler_SubjectTokenWithAct_Rejected(t *testing.T) {
	signer := loadTestSigner(t)
	emitter := &memoryEmitter{}
	handler := newHandler(t, signer, allowGate, fakeVerifier{claims: validActorClaims}, emitter)

	withAct := signSubjectToken(t, signer, testHumanSub, map[string]any{
		"act": map[string]any{"sub": testAgentSub},
	})
	rr := post(handler, validFormValues(withAct, "any-actor-jwt"))
	require.Equal(t, http.StatusBadRequest, rr.Code)
	body := decodeOAuthError(t, rr.Body)
	require.Equal(t, "invalid_request", body["error"])
	require.Equal(t, "subject_token must not carry act on first hop", body["error_description"])
}

func TestHandler_ActorTokenVerificationFails(t *testing.T) {
	signer := loadTestSigner(t)
	emitter := &memoryEmitter{}
	verifier := fakeVerifier{err: errors.New("unknown kid")}
	handler := newHandler(t, signer, allowGate, verifier, emitter)

	subject := signSubjectToken(t, signer, testHumanSub, nil)
	rr := post(handler, validFormValues(subject, "any-actor-jwt"))
	require.Equal(t, http.StatusBadRequest, rr.Code)
	body := decodeOAuthError(t, rr.Body)
	require.Equal(t, "invalid_request", body["error"])
	require.Equal(t, "actor_token verification failed", body["error_description"])
}

func TestHandler_PolicyDenied(t *testing.T) {
	signer := loadTestSigner(t)
	subject := signSubjectToken(t, signer, testHumanSub, nil)

	cases := []struct {
		name     string
		gate     fakeGate
		wantCode string
	}{
		{
			name:     "unknown_scope maps to invalid_scope",
			gate:     fakeGate{allow: false, reason: "unknown_scope"},
			wantCode: "invalid_scope",
		},
		{
			name:     "no matching allow entry maps to access_denied",
			gate:     fakeGate{allow: false, reason: "no_matching_allow_entry"},
			wantCode: "access_denied",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emitter := &memoryEmitter{}
			handler := newHandler(t, signer, tc.gate, fakeVerifier{claims: validActorClaims}, emitter)
			rr := post(handler, validFormValues(subject, "any-actor-jwt"))
			require.Equal(t, http.StatusBadRequest, rr.Code)
			body := decodeOAuthError(t, rr.Body)
			require.Equal(t, tc.wantCode, body["error"])
			require.Equal(t, tc.gate.reason, body["error_description"])

			// A denied request emits an audit event with no token fields.
			events := emitter.Snapshot()
			require.Len(t, events, 1)
			require.Equal(t, "denied", events[0].Outcome)
			require.Nil(t, events[0].ScopeGranted)
			require.Nil(t, events[0].TokenJTI)
			require.Nil(t, events[0].TokenExp)
			require.NotNil(t, events[0].PolicyReason)
			require.Equal(t, tc.gate.reason, *events[0].PolicyReason)
			require.NotNil(t, events[0].ExistingChain, "existing_chain must be [], never null, per CONTRACT.md §9")
			require.Equal(t, []string{}, events[0].ExistingChain)
		})
	}
}

func TestHandler_HappyPath(t *testing.T) {
	signer := loadTestSigner(t)
	emitter := &memoryEmitter{}
	handler := newHandler(t, signer, allowGate, fakeVerifier{claims: validActorClaims}, emitter)

	subject := signSubjectToken(t, signer, testHumanSub, nil)
	rr := post(handler, validFormValues(subject, "any-actor-jwt"))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))

	require.Equal(t, "urn:ietf:params:oauth:token-type:jwt", body["issued_token_type"])
	require.Equal(t, "Bearer", body["token_type"])
	require.LessOrEqual(t, int64(body["expires_in"].(float64)), int64(300))
	require.Equal(t, testScope, body["scope"])
	_, hasRefresh := body["refresh_token"]
	require.False(t, hasRefresh, "RFC 8693 §2.2 response must not include refresh_token")

	accessToken, ok := body["access_token"].(string)
	require.True(t, ok)
	header, claims := decodeJWT(t, accessToken)
	require.Equal(t, "EdDSA", header["alg"])
	require.Equal(t, signer.KID(), header["kid"])
	require.Equal(t, testIssuer, claims["iss"])
	require.Equal(t, testHumanSub, claims["sub"])
	require.Equal(t, testAudience, claims["aud"])
	require.Equal(t, testScope, claims["scope"])
	act, ok := claims["act"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, testAgentSub, act["sub"])
	_, hasNestedAct := act["act"]
	require.False(t, hasNestedAct, "first-hop act must not carry a nested act (CONTRACT.md §6.1 rule 4)")

	events := emitter.Snapshot()
	require.Len(t, events, 1)
	ev := events[0]
	require.Equal(t, "minted", ev.Outcome)
	require.Equal(t, claims["jti"], ev.EventID)
	require.NotNil(t, ev.ScopeGranted)
	require.Equal(t, testScope, *ev.ScopeGranted)
	require.NotNil(t, ev.TokenJTI)
	require.NotNil(t, ev.TokenExp)
	require.Equal(t, []string{testAgentSub}, ev.ResultingChain)
	require.Equal(t, []string{}, ev.ExistingChain)
}

// decodeJWT splits a compact JWT and returns (header, claims). Used by
// the happy-path test and by T-12. The signature is not verified — the
// caller already trusts the signer it minted with.
func decodeJWT(t *testing.T, jwt string) (map[string]any, map[string]any) {
	t.Helper()
	parts := strings.Split(jwt, ".")
	require.Len(t, parts, 3, "compact JWT must have header.payload.signature")
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var header, claims map[string]any
	require.NoError(t, json.Unmarshal(headerBytes, &header))
	require.NoError(t, json.Unmarshal(claimsBytes, &claims))
	return header, claims
}

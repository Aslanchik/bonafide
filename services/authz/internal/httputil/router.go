// Package httputil owns the authz HTTP plumbing — the chi router shape,
// the RFC 6749 §5.2 error helper that every 400 path on POST /token
// goes through, and the lone 5xx path. Keeping these in one place means
// the contract surface (CONTRACT.md §7) is enforced by type, not by
// caller discipline: the OAuth error codes are an enum, and the only
// way to write 5xx from the exchange handler is WriteServerError.
//
// T-10 lands the shape; T-13 constructs concrete handlers and passes
// them via Handlers. The route set is frozen by CONTRACT.md §§7, 11
// and the OIDC discovery contract.
package httputil

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// OAuthErrorCode is the RFC 6749 §5.2 / CONTRACT.md §7 enum of legal
// error codes the exchange handler may return. The type exists so
// callers cannot pass an arbitrary string; the four constants below
// are the entire legal set.
type OAuthErrorCode string

const (
	OAuthInvalidRequest OAuthErrorCode = "invalid_request"
	OAuthInvalidGrant   OAuthErrorCode = "invalid_grant"
	OAuthInvalidScope   OAuthErrorCode = "invalid_scope"
	OAuthAccessDenied   OAuthErrorCode = "access_denied"
)

// Handlers carries the four route implementations NewRouter mounts.
// T-13 constructs this struct from the wired-up exchange handler,
// JWKS publisher, OIDC discovery document, and healthz probe. T-10
// only needs the shape; callers in tests pass placeholder funcs.
type Handlers struct {
	OIDCDiscovery http.HandlerFunc
	JWKS          http.HandlerFunc
	TokenExchange http.HandlerFunc
	Health        http.HandlerFunc
}

// NewRouter returns a chi.Router with the four routes the authz data
// plane exposes: the two OIDC discovery endpoints (CONTRACT.md §11),
// the RFC 8693 token-exchange endpoint (CONTRACT.md §7), and a liveness
// probe. Route paths are frozen — they appear verbatim in the OIDC
// discovery document and the smoke harness.
func NewRouter(h Handlers) chi.Router {
	r := chi.NewRouter()
	r.Get("/.well-known/openid-configuration", h.OIDCDiscovery)
	r.Get("/.well-known/jwks.json", h.JWKS)
	r.Post("/token", h.TokenExchange)
	r.Get("/healthz", h.Health)
	return r
}

// WriteOAuthError writes a CONTRACT.md §7 / RFC 6749 §5.2 error
// response: HTTP 400, Content-Type: application/json, body
// {"error": "<code>", "error_description": "<description>"} with
// exactly those two fields. The code argument is the typed enum, so
// the compiler refuses an off-contract value.
func WriteOAuthError(w http.ResponseWriter, code OAuthErrorCode, description string) {
	body := map[string]string{
		"error":             string(code),
		"error_description": description,
	}
	WriteJSON(w, http.StatusBadRequest, body)
}

// WriteJSON marshals body and writes it with the given status and
// Content-Type: application/json. A marshal failure falls through to
// WriteServerError so the response is always well-formed.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	payload, err := json.Marshal(body)
	if err != nil {
		WriteServerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

// WriteServerError is the only path in the data plane that writes a
// 5xx response. CONTRACT.md §7 reserves 5xx for genuine server faults
// (signing failure, audit goroutine crash, panic) — never for a
// malformed request. The body is the same RFC 6749 shape so clients
// have a single parser; the code "server_error" is documented to
// callers as "retry, then escalate".
func WriteServerError(w http.ResponseWriter, err error) {
	slog.Error("event=server_error", "err", err.Error())
	body := map[string]string{
		"error":             "server_error",
		"error_description": "internal server error",
	}
	payload, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write(payload)
}

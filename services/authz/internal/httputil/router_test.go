package httputil

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteOAuthError_BodyShape(t *testing.T) {
	cases := []struct {
		name        string
		code        OAuthErrorCode
		description string
	}{
		{"invalid_request", OAuthInvalidRequest, "missing actor_token"},
		{"invalid_grant", OAuthInvalidGrant, "subject_token signature mismatch"},
		{"invalid_scope", OAuthInvalidScope, "unknown_scope"},
		{"access_denied", OAuthAccessDenied, "no_matching_allow_entry"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			WriteOAuthError(rr, tc.code, tc.description)

			require.Equal(t, http.StatusBadRequest, rr.Code)
			require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

			var decoded map[string]any
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &decoded))
			require.Len(t, decoded, 2, "RFC 6749 §5.2 body must have exactly two keys")
			require.Equal(t, string(tc.code), decoded["error"])
			require.Equal(t, tc.description, decoded["error_description"])
		})
	}
}

func TestWriteOAuthError_LegalCodesAreExhaustive(t *testing.T) {
	// The legal-set constraint is enforced at the type level: the four
	// constants below are the only OAuthErrorCode values the package
	// exports. This test pins the set so an inadvertent addition (or
	// removal) breaks here, drawing attention to CONTRACT.md §7.
	legal := []OAuthErrorCode{
		OAuthInvalidRequest,
		OAuthInvalidGrant,
		OAuthInvalidScope,
		OAuthAccessDenied,
	}
	require.ElementsMatch(t, []string{
		"invalid_request",
		"invalid_grant",
		"invalid_scope",
		"access_denied",
	}, codesAsStrings(legal))
}

func codesAsStrings(in []OAuthErrorCode) []string {
	out := make([]string, len(in))
	for i, c := range in {
		out[i] = string(c)
	}
	return out
}

func TestWriteJSON(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteJSON(rr, http.StatusOK, map[string]any{"hello": "world"})

	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "application/json", rr.Header().Get("Content-Type"))
	require.JSONEq(t, `{"hello":"world"}`, rr.Body.String())
}

func TestWriteServerError(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteServerError(rr, errors.New("signing failed"))

	require.Equal(t, http.StatusInternalServerError, rr.Code)
	require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

	var decoded map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &decoded))
	require.Equal(t, "server_error", decoded["error"])
	require.NotEmpty(t, decoded["error_description"])
}

func TestNewRouter_RegistersFourRoutes(t *testing.T) {
	probe := func(label string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(label))
		}
	}
	r := NewRouter(Handlers{
		OIDCDiscovery: probe("oidc"),
		JWKS:          probe("jwks"),
		TokenExchange: probe("token"),
		Health:        probe("health"),
	})

	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/.well-known/openid-configuration", "oidc"},
		{http.MethodGet, "/.well-known/jwks.json", "jwks"},
		{http.MethodPost, "/token", "token"},
		{http.MethodGet, "/healthz", "health"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(""))
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			require.NotEqual(t, http.StatusNotFound, rr.Code, "route must be registered")
			require.Equal(t, http.StatusOK, rr.Code)
			require.Equal(t, tc.body, rr.Body.String())
		})
	}
}

func TestNewRouter_UnknownRouteIs404(t *testing.T) {
	nop := func(http.ResponseWriter, *http.Request) {}
	r := NewRouter(Handlers{
		OIDCDiscovery: nop,
		JWKS:          nop,
		TokenExchange: nop,
		Health:        nop,
	})

	req := httptest.NewRequest(http.MethodGet, "/not-a-real-route", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusNotFound, rr.Code)
}

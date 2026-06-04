package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unsetAuthzEnv clears every BONAFIDE_AUTHZ_* variable for the duration
// of a test and restores the prior values via t.Cleanup. We use direct
// os.Unsetenv rather than t.Setenv("X", "") because envOr() treats an
// empty string as a set value.
func unsetAuthzEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"BONAFIDE_AUTHZ_LISTEN",
		"BONAFIDE_AUTHZ_ISSUER",
		"BONAFIDE_AUTHZ_SIGNING_KEY_PATH",
		"BONAFIDE_AUTHZ_ACTOR_TRUST_PATH",
		"BONAFIDE_AUTHZ_POLICY_PATH",
		"BONAFIDE_AUTHZ_AUDIT_PATH",
		"BONAFIDE_AUTHZ_TASK_TOKEN_TTL_SECONDS",
	} {
		prev, had := os.LookupEnv(k)
		os.Unsetenv(k)
		k := k
		if had {
			t.Cleanup(func() { os.Setenv(k, prev) })
		} else {
			t.Cleanup(func() { os.Unsetenv(k) })
		}
	}
}

func TestLoad_Defaults(t *testing.T) {
	unsetAuthzEnv(t)
	s, err := Load()
	require.NoError(t, err)
	assert.Equal(t, ":8080", s.Listen)
	assert.Equal(t, "http://authz.bonafide.local:8080", s.Issuer)
	assert.Equal(t, "/etc/authz/signing.key", s.SigningKeyPath)
	assert.Equal(t, "/etc/authz/actor-trust.yaml", s.ActorTrustPath)
	assert.Equal(t, "/etc/authz/policy.yaml", s.PolicyPath)
	assert.Equal(t, "/var/log/bonafide/audit.log", s.AuditPath)
	assert.Equal(t, 300, s.TaskTokenTTLSeconds)
}

func TestLoad_TTLOverride(t *testing.T) {
	unsetAuthzEnv(t)
	t.Setenv("BONAFIDE_AUTHZ_TASK_TOKEN_TTL_SECONDS", "120")
	s, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 120, s.TaskTokenTTLSeconds)
}

func TestLoad_TTLTooHigh(t *testing.T) {
	unsetAuthzEnv(t)
	t.Setenv("BONAFIDE_AUTHZ_TASK_TOKEN_TTL_SECONDS", "301")
	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds CONTRACT.md §5 ceiling")
}

func TestLoad_TTLNonInteger(t *testing.T) {
	unsetAuthzEnv(t)
	t.Setenv("BONAFIDE_AUTHZ_TASK_TOKEN_TTL_SECONDS", "five-minutes")
	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not an integer")
}

func TestLoad_TTLZero(t *testing.T) {
	unsetAuthzEnv(t)
	t.Setenv("BONAFIDE_AUTHZ_TASK_TOKEN_TTL_SECONDS", "0")
	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be > 0")
}

func TestLoad_EmptyIssuerRejected(t *testing.T) {
	unsetAuthzEnv(t)
	t.Setenv("BONAFIDE_AUTHZ_ISSUER", "")
	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BONAFIDE_AUTHZ_ISSUER must not be empty")
}

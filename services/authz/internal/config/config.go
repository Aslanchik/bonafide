// Package config owns the authz binary's startup-time settings. Every
// knob is read from the process environment per design.md "Configuration
// → services/authz (Go)"; there are no flags and no config files (the
// YAML stubs at deploy/authz/ are structured data, not config).
//
// Load fails closed: any required path that cannot be read, or a
// TaskTokenTTLSeconds above the CONTRACT.md §5 / DESIGN.md §4 ceiling,
// produces a non-nil error so cmd/authz/main exits non-zero before
// binding the listener (CLAUDE.md "Safety constraints": fail closed,
// no code path may extend the TTL ceilings).
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

// Defaults mirror design.md "Configuration → services/authz (Go)".
const (
	defaultListen              = ":8080"
	defaultIssuer              = "https://authz.bonafide.local" // CONTRACT.md §§4, 5 canonical form; see agent-notes.md 2026-06-04
	defaultSigningKeyPath      = "/etc/authz/signing.key"
	defaultActorTrustPath      = "/etc/authz/actor-trust.yaml"
	defaultPolicyPath          = "/etc/authz/policy.yaml"
	defaultAuditPath           = "/var/log/bonafide/audit.log"
	defaultTaskTokenTTLSeconds = 300

	// taskTokenTTLCeilingSeconds is the CONTRACT.md §5 / DESIGN.md §4
	// task-token TTL ceiling. Configuration above this is rejected at
	// load time so a misconfigured deployment cannot extend the cap.
	taskTokenTTLCeilingSeconds = 300
)

// Settings is the immutable, validated configuration the authz binary
// runs with. Constructed once by Load; never mutated.
type Settings struct {
	Listen              string
	Issuer              string
	SigningKeyPath      string
	ActorTrustPath      string
	PolicyPath          string
	AuditPath           string
	TaskTokenTTLSeconds int
}

// Load reads the BONAFIDE_AUTHZ_* environment variables, applies
// defaults from design.md, and validates the result. The returned
// Settings is safe for concurrent reads.
func Load() (Settings, error) {
	s := Settings{
		Listen:              envOr("BONAFIDE_AUTHZ_LISTEN", defaultListen),
		Issuer:              envOr("BONAFIDE_AUTHZ_ISSUER", defaultIssuer),
		SigningKeyPath:      envOr("BONAFIDE_AUTHZ_SIGNING_KEY_PATH", defaultSigningKeyPath),
		ActorTrustPath:      envOr("BONAFIDE_AUTHZ_ACTOR_TRUST_PATH", defaultActorTrustPath),
		PolicyPath:          envOr("BONAFIDE_AUTHZ_POLICY_PATH", defaultPolicyPath),
		AuditPath:           envOr("BONAFIDE_AUTHZ_AUDIT_PATH", defaultAuditPath),
		TaskTokenTTLSeconds: defaultTaskTokenTTLSeconds,
	}

	if raw := os.Getenv("BONAFIDE_AUTHZ_TASK_TOKEN_TTL_SECONDS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return Settings{}, fmt.Errorf("config: BONAFIDE_AUTHZ_TASK_TOKEN_TTL_SECONDS %q is not an integer: %w", raw, err)
		}
		if n <= 0 {
			return Settings{}, fmt.Errorf("config: BONAFIDE_AUTHZ_TASK_TOKEN_TTL_SECONDS %d must be > 0", n)
		}
		if n > taskTokenTTLCeilingSeconds {
			return Settings{}, fmt.Errorf("config: BONAFIDE_AUTHZ_TASK_TOKEN_TTL_SECONDS %d exceeds CONTRACT.md §5 ceiling of %d", n, taskTokenTTLCeilingSeconds)
		}
		s.TaskTokenTTLSeconds = n
	}

	if s.Issuer == "" {
		return Settings{}, errors.New("config: BONAFIDE_AUTHZ_ISSUER must not be empty")
	}
	if s.Listen == "" {
		return Settings{}, errors.New("config: BONAFIDE_AUTHZ_LISTEN must not be empty")
	}

	return s, nil
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

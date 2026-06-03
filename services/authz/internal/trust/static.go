package trust

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	yaml "gopkg.in/yaml.v3"

	"bonafide.local/services/authz/internal/exchange"
)

// spiffeIDPattern matches the SPIFFE grammar defined in CONTRACT.md §1
// for the single bonafide trust domain. SWI deletes this regex (and
// the whole file) when SPIRE owns the verification path.
var spiffeIDPattern = regexp.MustCompile(`^spiffe://bonafide\.local/(human|agent|service)/[a-z0-9-]+$`)

// trustEntry is the in-memory shape of one row in actor-trust.yaml.
type trustEntry struct {
	spiffeID  string
	publicKey ed25519.PublicKey
}

// YAMLTrust is the M1 static implementation of IssuerTrust. The
// {kid → entry} map is loaded once at startup; the struct is then
// immutable and safe for concurrent reads. SWI deletes this file and
// replaces it with a SPIRE FetchJWTBundles-backed implementation.
type YAMLTrust struct {
	byKID    map[string]trustEntry
	audience string
	now      func() time.Time
}

type yamlFile struct {
	Trusts []struct {
		SpiffeID     string `yaml:"spiffe_id"`
		KID          string `yaml:"kid"`
		PublicKeyPEM string `yaml:"public_key_pem"`
	} `yaml:"trusts"`
}

// LoadYAMLTrust reads actor-trust.yaml at path, parses every entry's
// public_key_pem as an Ed25519 SPKI-PEM, and returns a YAMLTrust
// keyed by JOSE kid. expectedAudience is the value the actor_token's
// aud claim must equal — typically BONAFIDE_AUTHZ_ISSUER.
//
// Failures (missing file, malformed YAML, unparseable PEM, non-Ed25519
// key, duplicate kid) surface as a non-nil error so the caller exits
// non-zero before serving traffic (fail closed).
func LoadYAMLTrust(path, expectedAudience string) (*YAMLTrust, error) {
	if expectedAudience == "" {
		return nil, errors.New("trust: expectedAudience is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("trust: read %q: %w", path, err)
	}
	var doc yamlFile
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("trust: parse YAML at %q: %w", path, err)
	}
	byKID := make(map[string]trustEntry, len(doc.Trusts))
	for i, row := range doc.Trusts {
		if row.SpiffeID == "" || row.KID == "" || row.PublicKeyPEM == "" {
			return nil, fmt.Errorf("trust: entry %d in %q is missing spiffe_id/kid/public_key_pem", i, path)
		}
		if !spiffeIDPattern.MatchString(row.SpiffeID) {
			return nil, fmt.Errorf("trust: entry %d spiffe_id %q does not match CONTRACT.md §1 grammar", i, row.SpiffeID)
		}
		block, _ := pem.Decode([]byte(row.PublicKeyPEM))
		if block == nil {
			return nil, fmt.Errorf("trust: entry %d (%s) has no PEM block in public_key_pem", i, row.SpiffeID)
		}
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("trust: entry %d (%s) PKIX parse: %w", i, row.SpiffeID, err)
		}
		pub, ok := parsed.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("trust: entry %d (%s) is not an Ed25519 public key (got %T)", i, row.SpiffeID, parsed)
		}
		if _, dupe := byKID[row.KID]; dupe {
			return nil, fmt.Errorf("trust: duplicate kid %q in %q", row.KID, path)
		}
		byKID[row.KID] = trustEntry{spiffeID: row.SpiffeID, publicKey: pub}
	}
	return &YAMLTrust{byKID: byKID, audience: expectedAudience, now: time.Now}, nil
}

// Verify decodes the actor_token's JOSE header to find the kid, looks
// up the matching trust entry, verifies the Ed25519 signature with
// that entry's public key, and validates the embedded claims against
// CONTRACT.md §§1 and 3. The returned ActorTokenClaims is the zero
// value on every error path.
func (t *YAMLTrust) Verify(actorJWT string) (exchange.ActorTokenClaims, error) {
	var zero exchange.ActorTokenClaims
	sig, err := jose.ParseSigned(actorJWT, []jose.SignatureAlgorithm{jose.EdDSA})
	if err != nil {
		return zero, fmt.Errorf("trust: parse actor_token: %w", err)
	}
	if len(sig.Signatures) != 1 {
		return zero, fmt.Errorf("trust: actor_token must carry exactly one signature, got %d", len(sig.Signatures))
	}
	kid := sig.Signatures[0].Header.KeyID
	if kid == "" {
		return zero, errors.New("trust: actor_token JOSE header is missing kid")
	}
	entry, ok := t.byKID[kid]
	if !ok {
		return zero, fmt.Errorf("trust: unknown kid %q", kid)
	}
	payload, err := sig.Verify(entry.publicKey)
	if err != nil {
		return zero, fmt.Errorf("trust: signature mismatch for kid %q: %w", kid, err)
	}
	var claims exchange.ActorTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return zero, fmt.Errorf("trust: decode actor_token claims: %w", err)
	}
	if claims.Iss == "" || claims.Sub == "" || claims.Aud == "" || claims.Iat == 0 || claims.Exp == 0 {
		return zero, errors.New("trust: actor_token is missing one of iss/sub/aud/iat/exp")
	}
	if !spiffeIDPattern.MatchString(claims.Sub) {
		return zero, fmt.Errorf("trust: actor_token sub %q does not match CONTRACT.md §1 grammar", claims.Sub)
	}
	if claims.Sub != entry.spiffeID {
		return zero, fmt.Errorf("trust: actor_token sub %q does not match registered SPIFFE ID for kid %q", claims.Sub, kid)
	}
	if claims.Aud != t.audience {
		return zero, fmt.Errorf("trust: actor_token aud %q does not match expected %q", claims.Aud, t.audience)
	}
	if t.now().Unix() >= claims.Exp {
		return zero, fmt.Errorf("trust: actor_token expired at %d", claims.Exp)
	}
	return claims, nil
}

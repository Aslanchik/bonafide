// Package keys owns the authz server's Ed25519 signing key and its
// derived JWKS publication. The signer carries an immutable key pair
// loaded at process start; the kid is derived from the SHA-256 of the
// public key per design.md "JWKS publication". The package never reads
// the disk after LoadSigner returns, so a missing-key startup is fatal
// (the caller exits non-zero per CLAUDE.md "Safety constraints").
//
// Wire formats follow CONTRACT.md §3 (alg=EdDSA on every minted JWT)
// and CONTRACT.md §11 (JWKS carries only Ed25519 keys).
package keys

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"

	jose "github.com/go-jose/go-jose/v4"
)

// Signer carries the Ed25519 keypair the authz server uses to sign
// every JWT it mints (user JWTs in T-14, task tokens in T-11). A Signer
// is constructed once by LoadSigner at process start and is safe for
// concurrent use; the underlying go-jose Signer is stateless and the
// kid is precomputed.
type Signer struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
	kid  string
}

// LoadSigner reads a PEM-encoded PKCS#8 Ed25519 private key from path,
// derives the JWKS kid, and returns a Signer ready to sign. Any failure
// (missing file, malformed PEM, non-Ed25519 key) returns a non-nil
// error; the caller is expected to exit non-zero on error so the
// server fails closed before serving any traffic.
func LoadSigner(path string) (*Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("keys: read signing key %q: %w", path, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("keys: %q does not contain a PEM block", path)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("keys: parse PKCS#8 from %q: %w", path, err)
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("keys: %q is not an Ed25519 private key (got %T)", path, parsed)
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("keys: derived public key is not Ed25519 (got %T)", priv.Public())
	}
	return &Signer{
		priv: priv,
		pub:  pub,
		kid:  deriveKID(pub),
	}, nil
}

// KID returns the JWKS key identifier the JOSE header must carry on
// every JWT this signer produces. The value is the first 12 characters
// of base64url(sha256(public_key)), per design.md "JWKS publication".
func (s *Signer) KID() string {
	return s.kid
}

// PublicKey returns the Ed25519 public half of the signer's key. The
// exchange handler uses it to verify subject_tokens — the authz CLI
// (demo-human, T-14) signs user JWTs with this same key, so the
// signer is the authoritative verifier for them.
func (s *Signer) PublicKey() ed25519.PublicKey {
	return s.pub
}

// Sign signs the supplied claims as a JWT with alg=EdDSA and the
// signer's kid in the JOSE header. The header argument carries
// additional JOSE header fields (typ, etc.); attempts to override
// alg or kid through it are ignored — both come from the signer and
// must not be caller-overridable.
func (s *Signer) Sign(header, claims map[string]any) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("keys: marshal claims: %w", err)
	}
	opts := (&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), s.kid)
	for k, v := range header {
		if k == "alg" || k == "kid" {
			continue
		}
		opts = opts.WithHeader(jose.HeaderKey(k), v)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.EdDSA, Key: s.priv},
		opts,
	)
	if err != nil {
		return "", fmt.Errorf("keys: build signer: %w", err)
	}
	sig, err := signer.Sign(payload)
	if err != nil {
		return "", fmt.Errorf("keys: sign payload: %w", err)
	}
	return sig.CompactSerialize()
}

// JWKSDocument returns the published JWKS document — a single Ed25519
// public key with the signer's kid, alg=EdDSA, use=sig. The shape
// follows RFC 7517 / RFC 8037 with no bonafide-specific extensions
// (CONTRACT.md §11).
func (s *Signer) JWKSDocument() (json.RawMessage, error) {
	jwk := jose.JSONWebKey{
		Key:       s.pub,
		KeyID:     s.kid,
		Algorithm: "EdDSA",
		Use:       "sig",
	}
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}
	return json.Marshal(set)
}

// deriveKID computes the kid per design.md "JWKS publication": the
// first 12 characters of the base64url-no-padding encoding of the
// SHA-256 of the public-key bytes.
func deriveKID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return base64.RawURLEncoding.EncodeToString(sum[:])[:12]
}

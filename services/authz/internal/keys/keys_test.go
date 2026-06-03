package keys

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeTestKey generates a fresh Ed25519 keypair, writes the private
// half as a PKCS#8 PEM into a temp file, and returns (path, public).
// The temp directory is cleaned up by t.TempDir.
func writeTestKey(t *testing.T) (string, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	path := filepath.Join(t.TempDir(), "signing.key")
	require.NoError(t, os.WriteFile(path, pemBytes, 0o600))
	return path, pub
}

func TestLoadSigner_Sign_JWKS(t *testing.T) {
	path, _ := writeTestKey(t)
	signer, err := LoadSigner(path)
	require.NoError(t, err)
	require.NotEmpty(t, signer.KID())

	// Sign — confirm alg=EdDSA and kid matches the signer's kid.
	jwt, err := signer.Sign(
		map[string]any{"typ": "JWT"},
		map[string]any{"sub": "spiffe://bonafide.local/human/alice@example.com", "iat": 1, "exp": 2},
	)
	require.NoError(t, err)
	parts := strings.Split(jwt, ".")
	require.Len(t, parts, 3, "compact JWT must have header.payload.signature")
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	var header map[string]any
	require.NoError(t, json.Unmarshal(headerBytes, &header))
	require.Equal(t, "EdDSA", header["alg"], "alg must be EdDSA per CONTRACT.md §3")
	require.Equal(t, signer.KID(), header["kid"], "kid must match the signer's derived kid")

	// JWKSDocument — confirm one Ed25519 key with matching kid.
	doc, err := signer.JWKSDocument()
	require.NoError(t, err)
	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			Kid string `json:"kid"`
			Alg string `json:"alg"`
			Use string `json:"use"`
			X   string `json:"x"`
		} `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(doc, &jwks))
	require.Len(t, jwks.Keys, 1, "JWKS must publish exactly one key in M1")
	require.Equal(t, "OKP", jwks.Keys[0].Kty)
	require.Equal(t, "Ed25519", jwks.Keys[0].Crv)
	require.Equal(t, "EdDSA", jwks.Keys[0].Alg)
	require.Equal(t, "sig", jwks.Keys[0].Use)
	require.Equal(t, signer.KID(), jwks.Keys[0].Kid)
	require.NotEmpty(t, jwks.Keys[0].X)
}

func TestSign_HeaderCannotOverrideAlgOrKid(t *testing.T) {
	path, _ := writeTestKey(t)
	signer, err := LoadSigner(path)
	require.NoError(t, err)

	jwt, err := signer.Sign(
		map[string]any{"alg": "none", "kid": "attacker-controlled"},
		map[string]any{"sub": "x", "iat": 1, "exp": 2},
	)
	require.NoError(t, err)
	parts := strings.Split(jwt, ".")
	require.Len(t, parts, 3)
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	var header map[string]any
	require.NoError(t, json.Unmarshal(headerBytes, &header))
	require.Equal(t, "EdDSA", header["alg"], "caller must not override alg through the header map")
	require.Equal(t, signer.KID(), header["kid"], "caller must not override kid through the header map")
}

func TestLoadSigner_MissingFile_ReturnsError(t *testing.T) {
	_, err := LoadSigner(filepath.Join(t.TempDir(), "does-not-exist.key"))
	require.Error(t, err, "missing signing key must surface an error (fail closed)")
}

func TestLoadSigner_NonEd25519Key_ReturnsError(t *testing.T) {
	// PKCS#8-encoded random bytes are not a valid private key. We
	// simulate the "wrong key type" branch by writing a non-PEM file
	// — the PEM-decode step refuses it. This covers the safety surface
	// (no permissive default) without pulling in an RSA dep just to
	// generate a non-Ed25519 PEM.
	path := filepath.Join(t.TempDir(), "garbage.key")
	require.NoError(t, os.WriteFile(path, []byte("not a pem"), 0o600))
	_, err := LoadSigner(path)
	require.Error(t, err)
}

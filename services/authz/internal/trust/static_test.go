package trust

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"
)

const (
	testAudience = "https://authz.bonafide.local"
	testSpiffeID = "spiffe://bonafide.local/agent/planner"
	testKID      = "planner-dev-key-1"
)

// fixture builds an Ed25519 keypair, writes a YAMLTrust file with one
// entry under testKID/testSpiffeID, and returns a loaded YAMLTrust
// plus a signer the test can use to mint actor tokens with that kid.
func fixture(t *testing.T) (*YAMLTrust, jose.Signer, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	der, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	yamlPath := filepath.Join(t.TempDir(), "actor-trust.yaml")
	yamlBody := fmt.Sprintf(`trusts:
  - spiffe_id: %s
    kid: %s
    public_key_pem: |
%s`, testSpiffeID, testKID, indent(pubPEM, "      "))
	require.NoError(t, os.WriteFile(yamlPath, []byte(yamlBody), 0o600))

	trust, err := LoadYAMLTrust(yamlPath, testAudience)
	require.NoError(t, err)

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.EdDSA, Key: priv},
		(&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), testKID),
	)
	require.NoError(t, err)
	return trust, signer, priv
}

func indent(s, pad string) string {
	out := ""
	for _, line := range splitLines(s) {
		if line == "" {
			out += "\n"
			continue
		}
		out += pad + line + "\n"
	}
	return out
}

func splitLines(s string) []string {
	var lines []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			lines = append(lines, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func mintActorToken(t *testing.T, signer jose.Signer, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	sig, err := signer.Sign(payload)
	require.NoError(t, err)
	compact, err := sig.CompactSerialize()
	require.NoError(t, err)
	return compact
}

func validClaims() map[string]any {
	now := time.Now().Unix()
	return map[string]any{
		"iss": "https://planner.bonafide.local",
		"sub": testSpiffeID,
		"aud": testAudience,
		"iat": now,
		"exp": now + 300,
	}
}

func TestVerify_ValidActorToken_ReturnsClaims(t *testing.T) {
	trust, signer, _ := fixture(t)
	jwt := mintActorToken(t, signer, validClaims())
	claims, err := trust.Verify(jwt)
	require.NoError(t, err)
	require.Equal(t, testSpiffeID, claims.Sub)
	require.Equal(t, testAudience, claims.Aud)
}

func TestVerify_UnknownKID_Rejects(t *testing.T) {
	trust, _, priv := fixture(t)
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.EdDSA, Key: priv},
		(&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), "different-kid"),
	)
	require.NoError(t, err)
	jwt := mintActorToken(t, signer, validClaims())
	_, err = trust.Verify(jwt)
	require.Error(t, err)
}

func TestVerify_WrongSignature_Rejects(t *testing.T) {
	trust, _, _ := fixture(t)
	// Mint with a different private key but the registered kid — the
	// trust map's public key cannot verify this signature.
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.EdDSA, Key: otherPriv},
		(&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), testKID),
	)
	require.NoError(t, err)
	jwt := mintActorToken(t, signer, validClaims())
	_, err = trust.Verify(jwt)
	require.Error(t, err)
}

func TestVerify_Expired_Rejects(t *testing.T) {
	trust, signer, _ := fixture(t)
	claims := validClaims()
	claims["iat"] = time.Now().Unix() - 1000
	claims["exp"] = time.Now().Unix() - 1
	jwt := mintActorToken(t, signer, claims)
	_, err := trust.Verify(jwt)
	require.Error(t, err)
}

func TestVerify_MissingAud_Rejects(t *testing.T) {
	trust, signer, _ := fixture(t)
	claims := validClaims()
	delete(claims, "aud")
	jwt := mintActorToken(t, signer, claims)
	_, err := trust.Verify(jwt)
	require.Error(t, err)
}

func TestVerify_WrongAud_Rejects(t *testing.T) {
	trust, signer, _ := fixture(t)
	claims := validClaims()
	claims["aud"] = "https://other.example.com"
	jwt := mintActorToken(t, signer, claims)
	_, err := trust.Verify(jwt)
	require.Error(t, err)
}

func TestVerify_SubNotMatchingRegisteredSpiffeID_Rejects(t *testing.T) {
	trust, signer, _ := fixture(t)
	claims := validClaims()
	claims["sub"] = "spiffe://bonafide.local/agent/other"
	jwt := mintActorToken(t, signer, claims)
	_, err := trust.Verify(jwt)
	require.Error(t, err)
}

func TestVerify_SubNotMatchingSPIFFEGrammar_Rejects(t *testing.T) {
	trust, signer, _ := fixture(t)
	claims := validClaims()
	claims["sub"] = "https://not-a-spiffe-id"
	jwt := mintActorToken(t, signer, claims)
	_, err := trust.Verify(jwt)
	require.Error(t, err)
}

func TestLoadYAMLTrust_MissingFile_ReturnsError(t *testing.T) {
	_, err := LoadYAMLTrust(filepath.Join(t.TempDir(), "missing.yaml"), testAudience)
	require.Error(t, err)
}

func TestLoadYAMLTrust_EmptyExpectedAudience_ReturnsError(t *testing.T) {
	_, err := LoadYAMLTrust("/dev/null", "")
	require.Error(t, err)
}

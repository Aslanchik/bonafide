"""Verifies demo-human against T-14's acceptance criteria:

* The minted JWT decodes to the CONTRACT.md §4 claim set
  (iss=aud=canonical issuer, sub=spiffe://bonafide.local/human/<email>,
  exp-iat <= 900, alg=EdDSA in header).
* The JWT's signature verifies against the JWKS the authz publishes
  for the same key (so the exchange handler's subject_token kid check
  works).
* No code path produces an `act` claim (grep + a positive test that
  the decoded claims have no `act` key).
"""

from __future__ import annotations

import json
import pathlib
import subprocess
import sys

import jwt
import pytest
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

DEMO_HUMAN_DIR = pathlib.Path(__file__).resolve().parents[1]
SRC_ROOT = DEMO_HUMAN_DIR / "demo_human"


@pytest.fixture()
def signing_key(tmp_path: pathlib.Path) -> pathlib.Path:
    priv = Ed25519PrivateKey.generate()
    pem = priv.private_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PrivateFormat.PKCS8,
        encryption_algorithm=serialization.NoEncryption(),
    )
    path = tmp_path / "signing.key"
    path.write_bytes(pem)
    return path


def _run_cli(signing_key: pathlib.Path, *extra: str) -> str:
    env = {
        "BONAFIDE_DEMO_HUMAN_SIGNING_KEY_PATH": str(signing_key),
        "PATH": "/usr/bin:/bin",
    }
    result = subprocess.run(
        [sys.executable, "-m", "demo_human", "--email", "alice@example.com", *extra],
        capture_output=True,
        text=True,
        env=env,
        cwd=str(DEMO_HUMAN_DIR),
        check=True,
    )
    # One JWT on stdout with a single trailing newline.
    out = result.stdout
    assert out.endswith("\n"), "CLI must print a single trailing newline"
    assert out.count("\n") == 1, "CLI must print exactly one line"
    return out.strip()


def _publish_jwks(signing_key: pathlib.Path) -> dict:
    """Construct the JWKS the authz server would publish for this key,
    mirroring services/authz/internal/keys.JWKSDocument so the round-
    trip is realistic."""
    import base64
    import hashlib

    pem = signing_key.read_bytes()
    priv = serialization.load_pem_private_key(pem, password=None)
    raw_pub = priv.public_key().public_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PublicFormat.Raw,
    )
    digest = hashlib.sha256(raw_pub).digest()
    kid = base64.urlsafe_b64encode(digest).rstrip(b"=").decode("ascii")[:12]
    return {
        "keys": [
            {
                "kty": "OKP",
                "crv": "Ed25519",
                "alg": "EdDSA",
                "use": "sig",
                "kid": kid,
                "x": base64.urlsafe_b64encode(raw_pub).rstrip(b"=").decode("ascii"),
            }
        ]
    }


def test_minted_claims_match_contract(signing_key: pathlib.Path) -> None:
    token = _run_cli(signing_key)

    header = jwt.get_unverified_header(token)
    assert header["alg"] == "EdDSA"
    assert header["kid"], "kid must be set so JWKS lookup resolves"

    # CONTRACT.md §4 claim set, validated against the published JWKS.
    jwks = _publish_jwks(signing_key)
    assert header["kid"] == jwks["keys"][0]["kid"], "CLI kid must equal authz JWKS kid"

    issuer = "https://authz.bonafide.local"
    pyjwk = jwt.PyJWK(jwks["keys"][0])
    claims = jwt.decode(
        token,
        pyjwk.key,
        algorithms=["EdDSA"],
        issuer=issuer,
        audience=issuer,
        leeway=0,
    )

    assert claims["iss"] == issuer
    assert claims["sub"] == "spiffe://bonafide.local/human/alice@example.com"
    assert claims["aud"] == issuer
    assert claims["exp"] - claims["iat"] <= 900
    assert claims["exp"] - claims["iat"] > 0
    assert "jti" in claims and len(claims["jti"]) > 0
    assert claims.get("email") == "alice@example.com"

    # CONTRACT.md §4: subject_tokens MUST NOT carry act.
    assert "act" not in claims


def test_ttl_clamped_to_900(signing_key: pathlib.Path) -> None:
    token = _run_cli(signing_key, "--ttl", "100000")
    claims = jwt.decode(token, options={"verify_signature": False})
    assert claims["exp"] - claims["iat"] == 900


def test_no_act_code_path_in_source() -> None:
    """A grep of demo_human/ produces no occurrence of `"act"` as a
    claim being set — the only way to satisfy TEC-2's
    'CLI never emits a JWT carrying act' criterion permanently."""
    pattern = '"act"'
    for path in SRC_ROOT.rglob("*.py"):
        content = path.read_text()
        # The doc-comment in __main__.py mentions `act` as a forbidden
        # claim; the test allows the substring `act` (e.g. "act claim",
        # "act_chain") but not the literal JSON key form `"act"`.
        assert pattern not in content, f"{path} references the literal claim key {pattern}"

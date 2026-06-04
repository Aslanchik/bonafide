"""demo-human — bonafide M1 stub for the user-JWT issuer.

Mints a CONTRACT.md §4 user JWT (the subject_token of the eventual
RFC 8693 exchange) signed with the same Ed25519 key the authz server
publishes in its JWKS, so the authz server can verify it without
needing a separate trust map for humans. There is no --act flag and no
code path that adds an `act` claim — CONTRACT.md §4 forbids it on the
first hop and TEC-2's acceptance criterion makes that forbidden-ness a
property of this binary, not just of the exchange handler.

Run:

    BONAFIDE_DEMO_HUMAN_SIGNING_KEY_PATH=/path/to/signing.key \\
    python -m demo_human --email alice@example.com [--ttl 900]
"""

from __future__ import annotations

import base64
import hashlib
import sys
import time
import uuid

import jwt
import typer
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from pydantic_settings import BaseSettings, SettingsConfigDict

# CONTRACT.md §4 hard ceiling — exp - iat must be ≤ 900s. Mirrors
# DESIGN.md §4 and CLAUDE.md "All credentials short-lived". The CLI
# silently clamps any --ttl value to this ceiling so a misconfiguration
# cannot widen the user JWT lifetime past the spec maximum.
USER_JWT_TTL_CEILING_SECONDS = 900

# CONTRACT.md §1 SPIFFE prefix for human identities.
HUMAN_SPIFFE_PREFIX = "spiffe://bonafide.local/human/"


class Settings(BaseSettings):
    """Env-driven config — no flags, no config file.

    The signing key path is required; everything else has a default that
    matches CONTRACT.md §§4, 5. Defaults are intentionally the canonical
    wire identifiers, not dev transport URLs (see agent-notes.md
    2026-06-04 on the issuer drift resolution).
    """

    model_config = SettingsConfigDict(env_prefix="", case_sensitive=True)

    BONAFIDE_DEMO_HUMAN_SIGNING_KEY_PATH: str
    BONAFIDE_AUTHZ_ISSUER: str = "https://authz.bonafide.local"


def mint(
    email: str = typer.Option(..., "--email", help="User email; becomes the {name} portion of the SPIFFE sub."),
    ttl: int = typer.Option(
        USER_JWT_TTL_CEILING_SECONDS,
        "--ttl",
        min=1,
        help=f"Token lifetime in seconds; clamped to {USER_JWT_TTL_CEILING_SECONDS}s (CONTRACT.md §4).",
    ),
) -> None:
    """Mint a CONTRACT.md §4 user JWT and print it to stdout."""
    settings = Settings()
    private_key = _load_ed25519_private_key(settings.BONAFIDE_DEMO_HUMAN_SIGNING_KEY_PATH)
    kid = _derive_kid(private_key)

    now = int(time.time())
    exp = now + min(ttl, USER_JWT_TTL_CEILING_SECONDS)

    claims = {
        "iss": settings.BONAFIDE_AUTHZ_ISSUER,
        "sub": f"{HUMAN_SPIFFE_PREFIX}{email}",
        "aud": settings.BONAFIDE_AUTHZ_ISSUER,
        "iat": now,
        "exp": exp,
        "jti": str(uuid.uuid4()),
        "email": email,
    }

    token = jwt.encode(
        claims,
        private_key,
        algorithm="EdDSA",
        headers={"kid": kid},
    )

    sys.stdout.write(token + "\n")


def _load_ed25519_private_key(path: str) -> Ed25519PrivateKey:
    """Load a PKCS#8 PEM Ed25519 private key. Any other key type is an
    error — the authz server's JWKS publishes only Ed25519 per
    CONTRACT.md §11, and a mismatch would silently produce JWTs that
    fail verification."""
    with open(path, "rb") as fh:
        raw = fh.read()
    key = serialization.load_pem_private_key(raw, password=None)
    if not isinstance(key, Ed25519PrivateKey):
        raise typer.BadParameter(
            f"{path} is not an Ed25519 private key (got {type(key).__name__})"
        )
    return key


def _derive_kid(private_key: Ed25519PrivateKey) -> str:
    """Mirror services/authz/internal/keys.deriveKID: first 12 chars of
    base64url-no-padding(sha256(raw_public_key)). The kid value is what
    the authz JWKS publishes, so demo-human must derive the same string
    or the exchange handler's subject_token kid check will reject the
    minted JWT."""
    raw_pub = private_key.public_key().public_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PublicFormat.Raw,
    )
    digest = hashlib.sha256(raw_pub).digest()
    return base64.urlsafe_b64encode(digest).rstrip(b"=").decode("ascii")[:12]


def app() -> None:
    """Single-command entry point — typer.run treats `mint` as the
    program itself so `python -m demo_human --email ...` works without
    a subcommand name."""
    typer.run(mint)


if __name__ == "__main__":
    app()

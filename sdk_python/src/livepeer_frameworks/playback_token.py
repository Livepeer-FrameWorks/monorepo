"""Local signing of viewer playback tokens for streams, VOD assets, and clips
with a JWT playback policy. The token is an ES256 JWS carrying the claims the
gateway's playback verifier checks: kid in the header (one of the policy's
allowed kids), exp (required), and optionally nbf, aud (matched against
requiredAudience), and custom claims (matched exactly against
requiredClaimsJson)."""

from __future__ import annotations

import base64
import json
import time
from collections.abc import Mapping, Sequence
from datetime import datetime
from typing import Any

from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives.asymmetric.utils import decode_dss_signature

_REGISTERED_CLAIMS = frozenset({"exp", "iat", "nbf", "aud", "sub"})


def _unix(value: datetime | int | float) -> int:
    if isinstance(value, datetime):
        return int(value.timestamp())
    return int(value)


def _b64url(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode("ascii")


def canonical_json(value: Any) -> str:
    """JSON with object keys sorted at every level and no whitespace, so every SDK signs the same bytes."""
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def playback_token_signing_input(
    kid: str,
    *,
    expires_at: datetime | int | None = None,
    expires_in: int | None = None,
    issued_at: datetime | int | None = None,
    not_before: datetime | int | None = None,
    subject: str | None = None,
    audience: str | Sequence[str] | None = None,
    claims: Mapping[str, Any] | None = None,
) -> str:
    """The header and payload segments of a playback token, before signing."""
    if not kid:
        raise ValueError("kid is required")
    iat = int(time.time()) if issued_at is None else _unix(issued_at)
    if expires_at is not None:
        exp = _unix(expires_at)
    elif expires_in is not None:
        exp = iat + int(expires_in)
    else:
        raise ValueError("expires_at or expires_in is required: the playback verifier rejects tokens without exp")
    payload: dict[str, Any] = {}
    for name, value in (claims or {}).items():
        if name in _REGISTERED_CLAIMS:
            raise ValueError(f"claims may not set {name}; use the matching argument")
        payload[name] = value
    payload["exp"] = exp
    payload["iat"] = iat
    if not_before is not None:
        payload["nbf"] = _unix(not_before)
    if subject is not None:
        payload["sub"] = subject
    if audience is not None:
        aud = [audience] if isinstance(audience, str) else list(audience)
        payload["aud"] = aud[0] if len(aud) == 1 else aud
    header = {"alg": "ES256", "kid": kid, "typ": "JWT"}
    return f"{_b64url(canonical_json(header).encode())}.{_b64url(canonical_json(payload).encode())}"


def sign_playback_token(
    private_key_pem: str,
    kid: str,
    *,
    expires_at: datetime | int | None = None,
    expires_in: int | None = None,
    issued_at: datetime | int | None = None,
    not_before: datetime | int | None = None,
    subject: str | None = None,
    audience: str | Sequence[str] | None = None,
    claims: Mapping[str, Any] | None = None,
) -> str:
    """Signs a viewer playback token with a tenant signing key (the PKCS#8
    PEM createSigningKey returned) and its kid. Set expires_at or expires_in;
    issued_at defaults to now. claims may not set exp, iat, nbf, aud, or sub."""
    signing_input = playback_token_signing_input(
        kid,
        expires_at=expires_at,
        expires_in=expires_in,
        issued_at=issued_at,
        not_before=not_before,
        subject=subject,
        audience=audience,
        claims=claims,
    )
    key = serialization.load_pem_private_key(private_key_pem.encode(), password=None)
    if not isinstance(key, ec.EllipticCurvePrivateKey) or key.curve.name != "secp256r1":
        raise ValueError("private_key_pem must be a P-256 EC key, as createSigningKey returns it")
    der = key.sign(signing_input.encode("ascii"), ec.ECDSA(hashes.SHA256()))
    # JWS carries the raw r||s signature, not the DER encoding cryptography returns.
    r, s = decode_dss_signature(der)
    signature = r.to_bytes(32, "big") + s.to_bytes(32, "big")
    return f"{signing_input}.{_b64url(signature)}"

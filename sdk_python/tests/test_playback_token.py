"""sdk_conformance/playback_jwt.json."""

from __future__ import annotations

import base64
from typing import Any

import pytest
from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives.asymmetric.utils import encode_dss_signature

from conftest import load_fixture
from livepeer_frameworks import sign_playback_token
from livepeer_frameworks.playback_token import playback_token_signing_input

FIXTURE = load_fixture("playback_jwt.json")
KEY = FIXTURE["key"]

_NAMES = {
    "expiresAt": "expires_at",
    "expiresIn": "expires_in",
    "issuedAt": "issued_at",
    "notBefore": "not_before",
    "subject": "subject",
    "audience": "audience",
    "claims": "claims",
}


def _kwargs(fixture_input: dict[str, Any]) -> dict[str, Any]:
    return {_NAMES[k]: v for k, v in fixture_input.items()}


def _b64(segment: str) -> bytes:
    return base64.urlsafe_b64decode(segment + "=" * (-len(segment) % 4))


def verifies_under_fixture_key(token: str) -> bool:
    header, payload, signature = token.split(".")
    raw = _b64(signature)
    if len(raw) != 64:
        return False
    public = serialization.load_pem_public_key(KEY["publicKeyPem"].encode())
    assert isinstance(public, ec.EllipticCurvePublicKey)
    der = encode_dss_signature(int.from_bytes(raw[:32], "big"), int.from_bytes(raw[32:], "big"))
    try:
        public.verify(der, f"{header}.{payload}".encode(), ec.ECDSA(hashes.SHA256()))
    except InvalidSignature:
        return False
    return True


@pytest.mark.parametrize("case", FIXTURE["cases"], ids=lambda c: c["name"])
def test_signing_input_and_signature(case: dict[str, Any]) -> None:
    token = sign_playback_token(KEY["privateKeyPem"], KEY["kid"], **_kwargs(case["input"]))
    header, payload, _ = token.split(".")
    assert f"{header}.{payload}" == case["signingInput"]
    assert verifies_under_fixture_key(token)


@pytest.mark.parametrize("case", FIXTURE["rejected"], ids=lambda c: c["name"])
def test_rejected(case: dict[str, Any]) -> None:
    with pytest.raises(ValueError):
        playback_token_signing_input(KEY["kid"], **_kwargs(case["input"]))


def test_python_token_in_fixture() -> None:
    token = FIXTURE["tokens"].get("python")
    if token is None:
        pytest.skip("sdk_conformance/playback_jwt.json has no tokens.python yet")
    header, payload, _ = token.split(".")
    assert f"{header}.{payload}" == FIXTURE["cases"][1]["signingInput"]
    assert verifies_under_fixture_key(token)

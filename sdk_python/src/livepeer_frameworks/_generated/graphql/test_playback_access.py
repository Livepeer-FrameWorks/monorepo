from typing import Annotated, Literal, Optional, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, NotFoundError, ValidationError


class TestPlaybackAccess(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    test_playback_access: Annotated[
        Union[
            "TestPlaybackAccessTestPlaybackAccessPlaybackAccessDecision",
            "TestPlaybackAccessTestPlaybackAccessValidationError",
            "TestPlaybackAccessTestPlaybackAccessNotFoundError",
            "TestPlaybackAccessTestPlaybackAccessAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="testPlaybackAccess",
        description="Run the same evaluator the live USER_NEW path uses against a caller-\nsupplied JWT (or webhook test request) without registering a viewer\nsession. Mutation, not query, because webhook mode (fireWebhook=true)\nfires a real outbound HTTPS request to the customer URL.\nTenant ownership of the playback target is validated server-side.",
    )
    "Run the same evaluator the live USER_NEW path uses against a caller-\nsupplied JWT (or webhook test request) without registering a viewer\nsession. Mutation, not query, because webhook mode (fireWebhook=true)\nfires a real outbound HTTPS request to the customer URL.\nTenant ownership of the playback target is validated server-side."


class TestPlaybackAccessTestPlaybackAccessPlaybackAccessDecision(BaseModel):
    typename__: Literal["PlaybackAccessDecision"] = Field(alias="__typename")
    allowed: bool
    policy_type: str = Field(
        alias="policyType",
        description="Resolved policy type: 'public' | 'jwt' | 'webhook' | '' when no policy was found.",
    )
    "Resolved policy type: 'public' | 'jwt' | 'webhook' | '' when no policy was found."
    reason: Optional[str] = Field(
        description="Empty on allow; deny reason token (e.g. 'jwt-expired', 'webhook-deny-403') otherwise."
    )
    "Empty on allow; deny reason token (e.g. 'jwt-expired', 'webhook-deny-403') otherwise."
    detail: Optional[str] = Field(
        description="Free-form context for the operator (verifier error string, HTTP status as text)."
    )
    "Free-form context for the operator (verifier error string, HTTP status as text)."
    kid: Optional[str] = Field(
        description="JWT key ID claimed by the token (extracted before verification)."
    )
    "JWT key ID claimed by the token (extracted before verification)."
    claims_json: Optional[str] = Field(
        alias="claimsJson",
        description="JSON-encoded JWT claims map. On allow, the verified claims. On deny, an\nunverified parse of the payload — useful for diagnosing aud / required-\nclaim mismatches but never trustworthy as an auth signal.",
    )
    "JSON-encoded JWT claims map. On allow, the verified claims. On deny, an\nunverified parse of the payload — useful for diagnosing aud / required-\nclaim mismatches but never trustworthy as an auth signal."
    webhook_status: Optional[int] = Field(
        alias="webhookStatus",
        description="HTTP status code from the customer webhook (0 if no call was made).",
    )
    "HTTP status code from the customer webhook (0 if no call was made)."
    webhook_latency_ms: Optional[int] = Field(
        alias="webhookLatencyMs",
        description="End-to-end RTT for the webhook call in milliseconds (0 if no call was made).",
    )
    "End-to-end RTT for the webhook call in milliseconds (0 if no call was made)."
    resolved_internal_name: Optional[str] = Field(
        alias="resolvedInternalName",
        description="Internal MistServer name the evaluator resolved against. Always populated when a target was found.",
    )
    "Internal MistServer name the evaluator resolved against. Always populated when a target was found."


class TestPlaybackAccessTestPlaybackAccessValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class TestPlaybackAccessTestPlaybackAccessNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class TestPlaybackAccessTestPlaybackAccessAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


TestPlaybackAccess.model_rebuild()

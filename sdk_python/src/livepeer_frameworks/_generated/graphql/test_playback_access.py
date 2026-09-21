from typing import Annotated, Literal, Optional, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorFields, NotFoundErrorFields, ValidationErrorFields


class TestPlaybackAccess(BaseModel):
    test_playback_access: Annotated[
        Union[
            "TestPlaybackAccessTestPlaybackAccessPlaybackAccessDecision",
            "TestPlaybackAccessTestPlaybackAccessValidationError",
            "TestPlaybackAccessTestPlaybackAccessNotFoundError",
            "TestPlaybackAccessTestPlaybackAccessAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="testPlaybackAccess")


class TestPlaybackAccessTestPlaybackAccessPlaybackAccessDecision(BaseModel):
    typename__: Literal["PlaybackAccessDecision"] = Field(alias="__typename")
    allowed: bool
    policy_type: str = Field(alias="policyType")
    reason: Optional[str]
    detail: Optional[str]
    kid: Optional[str]
    claims_json: Optional[str] = Field(alias="claimsJson")
    webhook_status: Optional[int] = Field(alias="webhookStatus")
    webhook_latency_ms: Optional[int] = Field(alias="webhookLatencyMs")
    resolved_internal_name: Optional[str] = Field(alias="resolvedInternalName")


class TestPlaybackAccessTestPlaybackAccessValidationError(ValidationErrorFields):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class TestPlaybackAccessTestPlaybackAccessNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class TestPlaybackAccessTestPlaybackAccessAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


TestPlaybackAccess.model_rebuild()

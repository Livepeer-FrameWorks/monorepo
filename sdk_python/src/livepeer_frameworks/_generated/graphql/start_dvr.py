from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, DVRRequest, NotFoundError, ValidationError


class StartDVR(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    start_dvr: Annotated[
        Union[
            "StartDVRStartDvrDVRRequest",
            "StartDVRStartDvrValidationError",
            "StartDVRStartDvrNotFoundError",
            "StartDVRStartDvrAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="startDVR",
        description="Start DVR recording for a live stream.\nDVR creates one continuous archive session. Live seekback is bounded by\nthe resolved DVR policy; archive playback uses virtual chapters.",
    )
    "Start DVR recording for a live stream.\nDVR creates one continuous archive session. Live seekback is bounded by\nthe resolved DVR policy; archive playback uses virtual chapters."


class StartDVRStartDvrDVRRequest(DVRRequest):
    typename__: Literal["DVRRequest"] = Field(alias="__typename")


class StartDVRStartDvrValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class StartDVRStartDvrNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class StartDVRStartDvrAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


StartDVR.model_rebuild()

from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, DVRRequest, NotFoundError, ValidationError


class StartDVR(BaseModel):
    start_dvr: Annotated[
        Union[
            "StartDVRStartDvrDVRRequest",
            "StartDVRStartDvrValidationError",
            "StartDVRStartDvrNotFoundError",
            "StartDVRStartDvrAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="startDVR")


class StartDVRStartDvrDVRRequest(DVRRequest):
    typename__: Literal["DVRRequest"] = Field(alias="__typename")


class StartDVRStartDvrValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class StartDVRStartDvrNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class StartDVRStartDvrAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


StartDVR.model_rebuild()

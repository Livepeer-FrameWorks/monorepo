from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorFields,
    DVRRequestFields,
    NotFoundErrorFields,
    ValidationErrorFields,
)


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


class StartDVRStartDvrDVRRequest(DVRRequestFields):
    typename__: Literal["DVRRequest"] = Field(alias="__typename")


class StartDVRStartDvrValidationError(ValidationErrorFields):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class StartDVRStartDvrNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class StartDVRStartDvrAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


StartDVR.model_rebuild()

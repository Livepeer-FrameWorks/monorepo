from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorFields, DeleteSuccessFields, NotFoundErrorFields


class StopDVR(BaseModel):
    stop_dvr: Annotated[
        Union[
            "StopDVRStopDvrDeleteSuccess",
            "StopDVRStopDvrNotFoundError",
            "StopDVRStopDvrAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="stopDVR")


class StopDVRStopDvrDeleteSuccess(DeleteSuccessFields):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class StopDVRStopDvrNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class StopDVRStopDvrAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


StopDVR.model_rebuild()

from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorFields, DeleteSuccessFields, NotFoundErrorFields


class DeleteDVR(BaseModel):
    delete_dvr: Annotated[
        Union[
            "DeleteDVRDeleteDvrDeleteSuccess",
            "DeleteDVRDeleteDvrNotFoundError",
            "DeleteDVRDeleteDvrAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="deleteDVR")


class DeleteDVRDeleteDvrDeleteSuccess(DeleteSuccessFields):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class DeleteDVRDeleteDvrNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class DeleteDVRDeleteDvrAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


DeleteDVR.model_rebuild()

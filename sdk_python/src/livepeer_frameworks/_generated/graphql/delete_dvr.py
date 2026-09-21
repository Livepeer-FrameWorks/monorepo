from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthError, DeleteSuccess, NotFoundError


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


class DeleteDVRDeleteDvrDeleteSuccess(DeleteSuccess):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class DeleteDVRDeleteDvrNotFoundError(NotFoundError):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class DeleteDVRDeleteDvrAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


DeleteDVR.model_rebuild()

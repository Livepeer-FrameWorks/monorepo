from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorFields,
    NotFoundErrorFields,
    StreamKeyFields,
    ValidationErrorFields,
)


class CreateStreamKey(BaseModel):
    create_stream_key: Annotated[
        Union[
            "CreateStreamKeyCreateStreamKeyStreamKey",
            "CreateStreamKeyCreateStreamKeyValidationError",
            "CreateStreamKeyCreateStreamKeyNotFoundError",
            "CreateStreamKeyCreateStreamKeyAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="createStreamKey")


class CreateStreamKeyCreateStreamKeyStreamKey(StreamKeyFields):
    typename__: Literal["StreamKey"] = Field(alias="__typename")


class CreateStreamKeyCreateStreamKeyValidationError(ValidationErrorFields):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateStreamKeyCreateStreamKeyNotFoundError(NotFoundErrorFields):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class CreateStreamKeyCreateStreamKeyAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateStreamKey.model_rebuild()

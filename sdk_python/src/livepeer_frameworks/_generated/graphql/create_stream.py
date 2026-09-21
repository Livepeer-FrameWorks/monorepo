from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthError,
    Stream,
    StreamMetrics,
    StreamPlaybackPolicy,
    StreamPullSource,
    ValidationError,
)


class CreateStream(BaseModel):
    create_stream: Annotated[
        Union[
            "CreateStreamCreateStreamStream",
            "CreateStreamCreateStreamValidationError",
            "CreateStreamCreateStreamAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="createStream")


class CreateStreamCreateStreamStream(Stream):
    typename__: Literal["Stream"] = Field(alias="__typename")


class CreateStreamCreateStreamValidationError(ValidationError):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateStreamCreateStreamAuthError(AuthError):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateStream.model_rebuild()

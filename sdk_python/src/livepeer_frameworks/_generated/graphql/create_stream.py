from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthErrorFields,
    StreamFields,
    StreamFieldsMetrics,
    StreamFieldsPlaybackPolicy,
    StreamFieldsPullSource,
    ValidationErrorFields,
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


class CreateStreamCreateStreamStream(StreamFields):
    typename__: Literal["Stream"] = Field(alias="__typename")


class CreateStreamCreateStreamValidationError(ValidationErrorFields):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateStreamCreateStreamAuthError(AuthErrorFields):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateStream.model_rebuild()

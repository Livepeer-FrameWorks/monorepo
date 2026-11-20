from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    IngestDisconnectSuccessDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
)


class DisconnectIngest(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    disconnect_ingest: Annotated[
        Union[
            "DisconnectIngestDisconnectIngestIngestDisconnectSuccess",
            "DisconnectIngestDisconnectIngestValidationError",
            "DisconnectIngestDisconnectIngestNotFoundError",
            "DisconnectIngestDisconnectIngestAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="disconnectIngest",
        description="Queue a stop for the current push publisher. Pull streams are stopped with pullSource.enabled=false.",
    )
    "Queue a stop for the current push publisher. Pull streams are stopped with pullSource.enabled=false."


class DisconnectIngestDisconnectIngestIngestDisconnectSuccess(
    IngestDisconnectSuccessDefault
):
    typename__: Literal["IngestDisconnectSuccess"] = Field(alias="__typename")


class DisconnectIngestDisconnectIngestValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class DisconnectIngestDisconnectIngestNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class DisconnectIngestDisconnectIngestAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


DisconnectIngest.model_rebuild()

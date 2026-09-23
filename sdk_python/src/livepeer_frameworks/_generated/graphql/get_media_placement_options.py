from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthErrorDefault,
    MediaPlacementErrorInMediaPlacementOptionsResultDefault,
    MediaPlacementErrorInMediaPlacementOptionsResultDefaultFields,
    MediaPlacementOptionsConnectionDefault,
    MediaPlacementOptionsConnectionDefaultNodes,
    MediaPlacementOptionsConnectionDefaultPageInfo,
    NotFoundErrorDefault,
)


class GetMediaPlacementOptions(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    media_placement_options: Annotated[
        Union[
            "GetMediaPlacementOptionsMediaPlacementOptionsMediaPlacementOptionsConnection",
            "GetMediaPlacementOptionsMediaPlacementOptionsMediaPlacementError",
            "GetMediaPlacementOptionsMediaPlacementOptionsAuthError",
            "GetMediaPlacementOptionsMediaPlacementOptionsNotFoundError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="mediaPlacementOptions")


class GetMediaPlacementOptionsMediaPlacementOptionsMediaPlacementOptionsConnection(
    MediaPlacementOptionsConnectionDefault
):
    typename__: Literal["MediaPlacementOptionsConnection"] = Field(alias="__typename")


class GetMediaPlacementOptionsMediaPlacementOptionsMediaPlacementError(
    MediaPlacementErrorInMediaPlacementOptionsResultDefault
):
    typename__: Literal["MediaPlacementError"] = Field(alias="__typename")


class GetMediaPlacementOptionsMediaPlacementOptionsAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


class GetMediaPlacementOptionsMediaPlacementOptionsNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


GetMediaPlacementOptions.model_rebuild()

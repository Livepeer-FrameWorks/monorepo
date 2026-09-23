from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthErrorDefault,
    MediaPlacementErrorInMediaPlacementPreviewResultDefault,
    MediaPlacementErrorInMediaPlacementPreviewResultDefaultFields,
    MediaPlacementPreviewDefault,
    MediaPlacementPreviewDefaultCandidates,
    MediaPlacementPreviewDefaultScope,
    MediaPlacementPreviewDefaultSelected,
    MediaPlacementPreviewDefaultTransitions,
    NotFoundErrorDefault,
)


class GetPreviewMediaPlacement(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    preview_media_placement: Annotated[
        Union[
            "GetPreviewMediaPlacementPreviewMediaPlacementMediaPlacementPreview",
            "GetPreviewMediaPlacementPreviewMediaPlacementMediaPlacementError",
            "GetPreviewMediaPlacementPreviewMediaPlacementAuthError",
            "GetPreviewMediaPlacementPreviewMediaPlacementNotFoundError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="previewMediaPlacement",
        description="Read-only evaluation. Never claims ingest, reserves capacity, or starts a source pull.",
    )
    "Read-only evaluation. Never claims ingest, reserves capacity, or starts a source pull."


class GetPreviewMediaPlacementPreviewMediaPlacementMediaPlacementPreview(
    MediaPlacementPreviewDefault
):
    typename__: Literal["MediaPlacementPreview"] = Field(alias="__typename")


class GetPreviewMediaPlacementPreviewMediaPlacementMediaPlacementError(
    MediaPlacementErrorInMediaPlacementPreviewResultDefault
):
    typename__: Literal["MediaPlacementError"] = Field(alias="__typename")


class GetPreviewMediaPlacementPreviewMediaPlacementAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


class GetPreviewMediaPlacementPreviewMediaPlacementNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


GetPreviewMediaPlacement.model_rebuild()

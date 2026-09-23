from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthErrorDefault,
    MediaPlacementErrorInMediaPlacementReviewResultDefault,
    MediaPlacementErrorInMediaPlacementReviewResultDefaultFields,
    MediaPlacementReviewDefault,
    MediaPlacementReviewDefaultDifferences,
    MediaPlacementReviewDefaultImpact,
    MediaPlacementReviewDefaultWarnings,
    NotFoundErrorDefault,
)


class GetReviewClusterMediaConsentChange(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    review_cluster_media_consent_change: Annotated[
        Union[
            "GetReviewClusterMediaConsentChangeReviewClusterMediaConsentChangeMediaPlacementReview",
            "GetReviewClusterMediaConsentChangeReviewClusterMediaConsentChangeMediaPlacementError",
            "GetReviewClusterMediaConsentChangeReviewClusterMediaConsentChangeAuthError",
            "GetReviewClusterMediaConsentChangeReviewClusterMediaConsentChangeNotFoundError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="reviewClusterMediaConsentChange")


class GetReviewClusterMediaConsentChangeReviewClusterMediaConsentChangeMediaPlacementReview(
    MediaPlacementReviewDefault
):
    typename__: Literal["MediaPlacementReview"] = Field(alias="__typename")


class GetReviewClusterMediaConsentChangeReviewClusterMediaConsentChangeMediaPlacementError(
    MediaPlacementErrorInMediaPlacementReviewResultDefault
):
    typename__: Literal["MediaPlacementError"] = Field(alias="__typename")


class GetReviewClusterMediaConsentChangeReviewClusterMediaConsentChangeAuthError(
    AuthErrorDefault
):
    typename__: Literal["AuthError"] = Field(alias="__typename")


class GetReviewClusterMediaConsentChangeReviewClusterMediaConsentChangeNotFoundError(
    NotFoundErrorDefault
):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


GetReviewClusterMediaConsentChange.model_rebuild()

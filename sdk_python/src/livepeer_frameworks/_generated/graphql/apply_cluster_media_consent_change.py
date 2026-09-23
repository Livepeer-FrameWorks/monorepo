from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthErrorDefault,
    MediaCapacityConsentChangeDefault,
    MediaCapacityConsentChangeDefaultRollout,
    MediaPlacementErrorInMediaCapacityConsentChangeResultDefault,
    MediaPlacementErrorInMediaCapacityConsentChangeResultDefaultFields,
    NotFoundErrorDefault,
)


class ApplyClusterMediaConsentChange(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    apply_cluster_media_consent_change: Annotated[
        Union[
            "ApplyClusterMediaConsentChangeApplyClusterMediaConsentChangeMediaCapacityConsentChange",
            "ApplyClusterMediaConsentChangeApplyClusterMediaConsentChangeMediaPlacementError",
            "ApplyClusterMediaConsentChangeApplyClusterMediaConsentChangeAuthError",
            "ApplyClusterMediaConsentChangeApplyClusterMediaConsentChangeNotFoundError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="applyClusterMediaConsentChange")


class ApplyClusterMediaConsentChangeApplyClusterMediaConsentChangeMediaCapacityConsentChange(
    MediaCapacityConsentChangeDefault
):
    typename__: Literal["MediaCapacityConsentChange"] = Field(alias="__typename")


class ApplyClusterMediaConsentChangeApplyClusterMediaConsentChangeMediaPlacementError(
    MediaPlacementErrorInMediaCapacityConsentChangeResultDefault
):
    typename__: Literal["MediaPlacementError"] = Field(alias="__typename")


class ApplyClusterMediaConsentChangeApplyClusterMediaConsentChangeAuthError(
    AuthErrorDefault
):
    typename__: Literal["AuthError"] = Field(alias="__typename")


class ApplyClusterMediaConsentChangeApplyClusterMediaConsentChangeNotFoundError(
    NotFoundErrorDefault
):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


ApplyClusterMediaConsentChange.model_rebuild()

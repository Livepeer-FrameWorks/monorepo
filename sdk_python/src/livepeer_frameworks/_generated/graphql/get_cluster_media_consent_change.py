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


class GetClusterMediaConsentChange(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    cluster_media_consent_change: Annotated[
        Union[
            "GetClusterMediaConsentChangeClusterMediaConsentChangeMediaCapacityConsentChange",
            "GetClusterMediaConsentChangeClusterMediaConsentChangeMediaPlacementError",
            "GetClusterMediaConsentChangeClusterMediaConsentChangeAuthError",
            "GetClusterMediaConsentChangeClusterMediaConsentChangeNotFoundError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="clusterMediaConsentChange")


class GetClusterMediaConsentChangeClusterMediaConsentChangeMediaCapacityConsentChange(
    MediaCapacityConsentChangeDefault
):
    typename__: Literal["MediaCapacityConsentChange"] = Field(alias="__typename")


class GetClusterMediaConsentChangeClusterMediaConsentChangeMediaPlacementError(
    MediaPlacementErrorInMediaCapacityConsentChangeResultDefault
):
    typename__: Literal["MediaPlacementError"] = Field(alias="__typename")


class GetClusterMediaConsentChangeClusterMediaConsentChangeAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


class GetClusterMediaConsentChangeClusterMediaConsentChangeNotFoundError(
    NotFoundErrorDefault
):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


GetClusterMediaConsentChange.model_rebuild()

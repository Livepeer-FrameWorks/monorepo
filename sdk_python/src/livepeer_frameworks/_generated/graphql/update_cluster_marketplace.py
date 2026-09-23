from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    ClusterDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
)


class UpdateClusterMarketplace(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    update_cluster_marketplace: Annotated[
        Union[
            "UpdateClusterMarketplaceUpdateClusterMarketplaceCluster",
            "UpdateClusterMarketplaceUpdateClusterMarketplaceValidationError",
            "UpdateClusterMarketplaceUpdateClusterMarketplaceNotFoundError",
            "UpdateClusterMarketplaceUpdateClusterMarketplaceAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="updateClusterMarketplace",
        description="Update marketplace settings for a cluster.",
    )
    "Update marketplace settings for a cluster."


class UpdateClusterMarketplaceUpdateClusterMarketplaceCluster(ClusterDefault):
    typename__: Literal["Cluster"] = Field(alias="__typename")


class UpdateClusterMarketplaceUpdateClusterMarketplaceValidationError(
    ValidationErrorDefault
):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class UpdateClusterMarketplaceUpdateClusterMarketplaceNotFoundError(
    NotFoundErrorDefault
):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class UpdateClusterMarketplaceUpdateClusterMarketplaceAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


UpdateClusterMarketplace.model_rebuild()

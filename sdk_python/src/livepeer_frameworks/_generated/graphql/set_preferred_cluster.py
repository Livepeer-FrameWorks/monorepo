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


class SetPreferredCluster(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    set_preferred_cluster: Annotated[
        Union[
            "SetPreferredClusterSetPreferredClusterCluster",
            "SetPreferredClusterSetPreferredClusterValidationError",
            "SetPreferredClusterSetPreferredClusterNotFoundError",
            "SetPreferredClusterSetPreferredClusterAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="setPreferredCluster",
        description="Set the tenant's preferred cluster. Must be a subscribed cluster.\nUsed by routing services when choosing tenant-preferred infrastructure.",
    )
    "Set the tenant's preferred cluster. Must be a subscribed cluster.\nUsed by routing services when choosing tenant-preferred infrastructure."


class SetPreferredClusterSetPreferredClusterCluster(ClusterDefault):
    typename__: Literal["Cluster"] = Field(alias="__typename")


class SetPreferredClusterSetPreferredClusterValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class SetPreferredClusterSetPreferredClusterNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class SetPreferredClusterSetPreferredClusterAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


SetPreferredCluster.model_rebuild()

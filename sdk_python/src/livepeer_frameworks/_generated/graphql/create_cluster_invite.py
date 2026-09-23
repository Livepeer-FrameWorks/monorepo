from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    ClusterInviteDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
)


class CreateClusterInvite(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_cluster_invite: Annotated[
        Union[
            "CreateClusterInviteCreateClusterInviteClusterInvite",
            "CreateClusterInviteCreateClusterInviteValidationError",
            "CreateClusterInviteCreateClusterInviteNotFoundError",
            "CreateClusterInviteCreateClusterInviteAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="createClusterInvite", description="Create an invite for a cluster."
    )
    "Create an invite for a cluster."


class CreateClusterInviteCreateClusterInviteClusterInvite(ClusterInviteDefault):
    typename__: Literal["ClusterInvite"] = Field(alias="__typename")


class CreateClusterInviteCreateClusterInviteValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateClusterInviteCreateClusterInviteNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class CreateClusterInviteCreateClusterInviteAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateClusterInvite.model_rebuild()

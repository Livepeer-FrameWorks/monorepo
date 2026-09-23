from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    ClusterSubscriptionDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
)


class AcceptClusterInvite(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    accept_cluster_invite: Annotated[
        Union[
            "AcceptClusterInviteAcceptClusterInviteClusterSubscription",
            "AcceptClusterInviteAcceptClusterInviteValidationError",
            "AcceptClusterInviteAcceptClusterInviteNotFoundError",
            "AcceptClusterInviteAcceptClusterInviteAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="acceptClusterInvite", description="Accept a cluster invite.")
    "Accept a cluster invite."


class AcceptClusterInviteAcceptClusterInviteClusterSubscription(
    ClusterSubscriptionDefault
):
    typename__: Literal["ClusterSubscription"] = Field(alias="__typename")


class AcceptClusterInviteAcceptClusterInviteValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class AcceptClusterInviteAcceptClusterInviteNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class AcceptClusterInviteAcceptClusterInviteAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


AcceptClusterInvite.model_rebuild()

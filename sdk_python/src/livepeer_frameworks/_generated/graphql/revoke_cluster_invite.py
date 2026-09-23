from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorDefault, DeleteSuccessDefault, NotFoundErrorDefault


class RevokeClusterInvite(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    revoke_cluster_invite: Annotated[
        Union[
            "RevokeClusterInviteRevokeClusterInviteDeleteSuccess",
            "RevokeClusterInviteRevokeClusterInviteNotFoundError",
            "RevokeClusterInviteRevokeClusterInviteAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="revokeClusterInvite", description="Revoke a cluster invite.")
    "Revoke a cluster invite."


class RevokeClusterInviteRevokeClusterInviteDeleteSuccess(DeleteSuccessDefault):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class RevokeClusterInviteRevokeClusterInviteNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class RevokeClusterInviteRevokeClusterInviteAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


RevokeClusterInvite.model_rebuild()

from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorDefault, DeleteSuccessDefault, NotFoundErrorDefault


class UnlinkWallet(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    unlink_wallet: Annotated[
        Union[
            "UnlinkWalletUnlinkWalletDeleteSuccess",
            "UnlinkWalletUnlinkWalletNotFoundError",
            "UnlinkWalletUnlinkWalletAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="unlinkWallet",
        description="Unlink a wallet from the current user's account.\nCannot unlink the last wallet if user has no email.",
    )
    "Unlink a wallet from the current user's account.\nCannot unlink the last wallet if user has no email."


class UnlinkWalletUnlinkWalletDeleteSuccess(DeleteSuccessDefault):
    typename__: Literal["DeleteSuccess"] = Field(alias="__typename")


class UnlinkWalletUnlinkWalletNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class UnlinkWalletUnlinkWalletAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


UnlinkWallet.model_rebuild()

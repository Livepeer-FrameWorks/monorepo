from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import AuthErrorDefault, ValidationErrorDefault, WalletIdentityDefault


class LinkWallet(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    link_wallet: Annotated[
        Union[
            "LinkWalletLinkWalletWalletIdentity",
            "LinkWalletLinkWalletValidationError",
            "LinkWalletLinkWalletAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="linkWallet",
        description="Link an additional wallet to the current user's account.\nRequires an existing authenticated session.",
    )
    "Link an additional wallet to the current user's account.\nRequires an existing authenticated session."


class LinkWalletLinkWalletWalletIdentity(WalletIdentityDefault):
    typename__: Literal["WalletIdentity"] = Field(alias="__typename")


class LinkWalletLinkWalletValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class LinkWalletLinkWalletAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


LinkWallet.model_rebuild()

from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    ValidationErrorDefault,
    WalletLoginPayloadDefault,
    WalletLoginPayloadDefaultUser,  # noqa: F401
)


class WalletLogin(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    wallet_login: Annotated[
        Union[
            "WalletLoginWalletLoginWalletLoginPayload",
            "WalletLoginWalletLoginValidationError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="walletLogin",
        description="Authenticate using a signed message from an Ethereum wallet.\nReturns a JWT token for API access. Creates a new account if the wallet\nhas not been seen before (auto-provisioning).",
    )
    "Authenticate using a signed message from an Ethereum wallet.\nReturns a JWT token for API access. Creates a new account if the wallet\nhas not been seen before (auto-provisioning)."


class WalletLoginWalletLoginWalletLoginPayload(WalletLoginPayloadDefault):
    typename__: Literal["WalletLoginPayload"] = Field(alias="__typename")


class WalletLoginWalletLoginValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


WalletLogin.model_rebuild()

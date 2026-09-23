from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    CryptoTopupResultDefault,
    CryptoTopupResultDefaultConversion,  # noqa: F401
)


class CreateCryptoTopup(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_crypto_topup: "CreateCryptoTopupCreateCryptoTopup" = Field(
        alias="createCryptoTopup",
        description="Create a crypto deposit address for prepaid balance top-up.\nReturns an HD-derived address for the agent to send crypto.\nThis is the agent-friendly payment method - no human-in-the-loop required.",
    )
    "Create a crypto deposit address for prepaid balance top-up.\nReturns an HD-derived address for the agent to send crypto.\nThis is the agent-friendly payment method - no human-in-the-loop required."


class CreateCryptoTopupCreateCryptoTopup(CryptoTopupResultDefault):
    """Result from creating a crypto top-up.

    The price is locked at this response. Send exactly `expectedAmountToken` of
    `asset` to `depositAddress`; on confirmation, the balance is credited at
    `quotedPriceUsd` regardless of price drift inside the address TTL."""

    pass


CreateCryptoTopup.model_rebuild()

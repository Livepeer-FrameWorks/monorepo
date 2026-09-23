from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    CryptoTopupStatusDefault,
    CryptoTopupStatusDefaultConversion,  # noqa: F401
)


class CryptoTopupStatusMutation(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    crypto_topup_status: "CryptoTopupStatusMutationCryptoTopupStatus" = Field(
        alias="cryptoTopupStatus",
        description="Check the status of a crypto top-up (for polling).\nReturns current status, confirmations, and credited amount when complete.",
    )
    "Check the status of a crypto top-up (for polling).\nReturns current status, confirmations, and credited amount when complete."


class CryptoTopupStatusMutationCryptoTopupStatus(CryptoTopupStatusDefault):
    """Status of a crypto top-up (for polling).

    `status` cycles: pending → confirming → completed (or expired)."""

    pass


CryptoTopupStatusMutation.model_rebuild()

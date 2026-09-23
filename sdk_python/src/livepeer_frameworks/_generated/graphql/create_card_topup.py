from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    CardTopupResultDefault,
    CardTopupResultDefaultConversion,  # noqa: F401
)


class CreateCardTopup(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_card_topup: "CreateCardTopupCreateCardTopup" = Field(
        alias="createCardTopup",
        description="Create a card checkout session for prepaid balance top-up.\nReturns a URL to redirect the user to Stripe/Mollie checkout.\nAfter successful payment, balance is credited automatically via webhook.",
    )
    "Create a card checkout session for prepaid balance top-up.\nReturns a URL to redirect the user to Stripe/Mollie checkout.\nAfter successful payment, balance is credited automatically via webhook."


class CreateCardTopupCreateCardTopup(CardTopupResultDefault):
    """Result from creating a card top-up checkout session."""

    pass


CreateCardTopup.model_rebuild()

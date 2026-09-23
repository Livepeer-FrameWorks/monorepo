from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import PrepaidBalanceDefault


class GetPrepaidBalance(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    prepaid_balance: Optional["GetPrepaidBalancePrepaidBalance"] = Field(
        alias="prepaidBalance",
        description="Get the current prepaid balance for the tenant, held in EUR.\nOnly available for tenants with billing_model = 'prepaid'.",
    )
    "Get the current prepaid balance for the tenant, held in EUR.\nOnly available for tenants with billing_model = 'prepaid'."


class GetPrepaidBalancePrepaidBalance(PrepaidBalanceDefault):
    """Prepaid balance for wallet-based accounts.
    Wallet-only accounts MUST use prepaid billing (balance-based, pay first)."""

    pass


GetPrepaidBalance.model_rebuild()

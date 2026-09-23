from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    InvoicePaymentDefault,
    InvoicePaymentDefaultConversion,  # noqa: F401
)


class GetPayment(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    payment: Optional["GetPaymentPayment"] = Field(
        description="Fetch one invoice payment owned by the current tenant."
    )
    "Fetch one invoice payment owned by the current tenant."


class GetPaymentPayment(InvoicePaymentDefault):
    """A tenant-visible invoice payment record. Provider secrets and raw payloads are never exposed."""

    pass


GetPayment.model_rebuild()

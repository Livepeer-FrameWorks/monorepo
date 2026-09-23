from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    InvoiceDefault,
    InvoiceDefaultLineItems,  # noqa: F401
)


class GetInvoice(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    invoice: Optional["GetInvoiceInvoice"] = Field(
        description="Fetch a single invoice by ID."
    )
    "Fetch a single invoice by ID."


class GetInvoiceInvoice(InvoiceDefault):
    """A billing invoice for a subscription period.
    Includes base subscription and metered usage charges. Amounts are computed in
    EUR; a finalized invoice is presented and charged in the tenant's presentment
    currency at the ECB rate of its finalization date."""

    pass


GetInvoice.model_rebuild()

from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    InvoiceDefault,
    InvoiceDefaultLineItems,  # noqa: F401
    PageInfoDefault,
)


class GetInvoicesConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    invoices_connection: "GetInvoicesConnectionInvoicesConnection" = Field(
        alias="invoicesConnection", description="List invoices for the current tenant."
    )
    "List invoices for the current tenant."


class GetInvoicesConnectionInvoicesConnection(BaseModel):
    edges: list["GetInvoicesConnectionInvoicesConnectionEdges"]
    page_info: "GetInvoicesConnectionInvoicesConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetInvoicesConnectionInvoicesConnectionEdges(BaseModel):
    cursor: str
    node: "GetInvoicesConnectionInvoicesConnectionEdgesNode"


class GetInvoicesConnectionInvoicesConnectionEdgesNode(InvoiceDefault):
    """A billing invoice for a subscription period.
    Includes base subscription and metered usage charges. Amounts are computed in
    EUR; a finalized invoice is presented and charged in the tenant's presentment
    currency at the ECB rate of its finalization date."""

    pass


GetInvoicesConnectionInvoicesConnectionPageInfo = PageInfoDefault
GetInvoicesConnection.model_rebuild()
GetInvoicesConnectionInvoicesConnection.model_rebuild()
GetInvoicesConnectionInvoicesConnectionEdges.model_rebuild()

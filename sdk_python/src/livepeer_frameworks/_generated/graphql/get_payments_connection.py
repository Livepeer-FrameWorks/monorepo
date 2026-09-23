from pydantic import Field

from .base_model import BaseModel
from .fragments import (
    InvoicePaymentDefault,
    InvoicePaymentDefaultConversion,  # noqa: F401
    PageInfoDefault,
)


class GetPaymentsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    payments_connection: "GetPaymentsConnectionPaymentsConnection" = Field(
        alias="paymentsConnection",
        description="List invoice payments owned by the current tenant.",
    )
    "List invoice payments owned by the current tenant."


class GetPaymentsConnectionPaymentsConnection(BaseModel):
    edges: list["GetPaymentsConnectionPaymentsConnectionEdges"]
    page_info: "GetPaymentsConnectionPaymentsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetPaymentsConnectionPaymentsConnectionEdges(BaseModel):
    cursor: str
    node: "GetPaymentsConnectionPaymentsConnectionEdgesNode"


class GetPaymentsConnectionPaymentsConnectionEdgesNode(InvoicePaymentDefault):
    """A tenant-visible invoice payment record. Provider secrets and raw payloads are never exposed."""

    pass


GetPaymentsConnectionPaymentsConnectionPageInfo = PageInfoDefault
GetPaymentsConnection.model_rebuild()
GetPaymentsConnectionPaymentsConnection.model_rebuild()
GetPaymentsConnectionPaymentsConnectionEdges.model_rebuild()

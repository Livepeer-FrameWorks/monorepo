from pydantic import Field

from .base_model import BaseModel
from .fragments import BalanceTransactionDefault, PageInfoDefault


class GetBalanceTransactionsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    balance_transactions_connection: "GetBalanceTransactionsConnectionBalanceTransactionsConnection" = Field(
        alias="balanceTransactionsConnection",
        description="List balance transactions for the tenant with pagination.",
    )
    "List balance transactions for the tenant with pagination."


class GetBalanceTransactionsConnectionBalanceTransactionsConnection(BaseModel):
    edges: list["GetBalanceTransactionsConnectionBalanceTransactionsConnectionEdges"]
    page_info: "GetBalanceTransactionsConnectionBalanceTransactionsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetBalanceTransactionsConnectionBalanceTransactionsConnectionEdges(BaseModel):
    """Balance transaction connection for pagination."""

    cursor: str
    node: "GetBalanceTransactionsConnectionBalanceTransactionsConnectionEdgesNode"


class GetBalanceTransactionsConnectionBalanceTransactionsConnectionEdgesNode(
    BalanceTransactionDefault
):
    """A single balance transaction (top-up, usage deduction, refund, etc.)."""

    pass


GetBalanceTransactionsConnectionBalanceTransactionsConnectionPageInfo = PageInfoDefault
GetBalanceTransactionsConnection.model_rebuild()
GetBalanceTransactionsConnectionBalanceTransactionsConnection.model_rebuild()
GetBalanceTransactionsConnectionBalanceTransactionsConnectionEdges.model_rebuild()

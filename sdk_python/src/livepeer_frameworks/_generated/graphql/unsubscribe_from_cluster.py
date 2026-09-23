from pydantic import Field

from .base_model import BaseModel


class UnsubscribeFromCluster(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    unsubscribe_from_cluster: bool = Field(
        alias="unsubscribeFromCluster", description="Unsubscribe from a cluster."
    )
    "Unsubscribe from a cluster."

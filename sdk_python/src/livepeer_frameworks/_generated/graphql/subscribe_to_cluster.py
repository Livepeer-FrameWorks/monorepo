from pydantic import Field

from .base_model import BaseModel


class SubscribeToCluster(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    subscribe_to_cluster: bool = Field(
        alias="subscribeToCluster",
        description="Subscribe to a cluster for streaming access.",
    )
    "Subscribe to a cluster for streaming access."

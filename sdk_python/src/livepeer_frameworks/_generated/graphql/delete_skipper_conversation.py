from pydantic import Field

from .base_model import BaseModel


class DeleteSkipperConversation(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    delete_skipper_conversation: bool = Field(
        alias="deleteSkipperConversation", description="Delete a Skipper conversation."
    )
    "Delete a Skipper conversation."

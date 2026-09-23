from pydantic import Field

from .base_model import BaseModel
from .fragments import SkipperConversationSummaryDefault


class UpdateSkipperConversation(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    update_skipper_conversation: "UpdateSkipperConversationUpdateSkipperConversation" = Field(
        alias="updateSkipperConversation",
        description="Update the title of a Skipper conversation.",
    )
    "Update the title of a Skipper conversation."


UpdateSkipperConversationUpdateSkipperConversation = SkipperConversationSummaryDefault
UpdateSkipperConversation.model_rebuild()

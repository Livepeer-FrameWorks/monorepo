from pydantic import Field

from .base_model import BaseModel
from .fragments import PushTarget


class CreatePushTarget(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_push_target: "CreatePushTargetCreatePushTarget" = Field(
        alias="createPushTarget",
        description="Add a multistream push target to a stream.\nWhen the stream goes live, it will automatically push to all enabled targets.",
    )
    "Add a multistream push target to a stream.\nWhen the stream goes live, it will automatically push to all enabled targets."


CreatePushTargetCreatePushTarget = PushTarget
CreatePushTarget.model_rebuild()

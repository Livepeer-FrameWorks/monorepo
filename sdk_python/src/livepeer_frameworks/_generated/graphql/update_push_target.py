from pydantic import Field

from .base_model import BaseModel
from .fragments import PushTarget


class UpdatePushTarget(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    update_push_target: "UpdatePushTargetUpdatePushTarget" = Field(
        alias="updatePushTarget", description="Update a multistream push target."
    )
    "Update a multistream push target."


UpdatePushTargetUpdatePushTarget = PushTarget
UpdatePushTarget.model_rebuild()

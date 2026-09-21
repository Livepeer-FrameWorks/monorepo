from pydantic import Field

from .base_model import BaseModel
from .fragments import DeleteSuccess


class DeletePushTarget(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    delete_push_target: "DeletePushTargetDeletePushTarget" = Field(
        alias="deletePushTarget", description="Delete a multistream push target."
    )
    "Delete a multistream push target."


DeletePushTargetDeletePushTarget = DeleteSuccess
DeletePushTarget.model_rebuild()

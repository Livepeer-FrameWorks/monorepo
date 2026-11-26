from pydantic import Field

from .base_model import BaseModel
from .fragments import FreeCancellationReviewDefault


class PreviewFreeCancellation(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    preview_free_cancellation: "PreviewFreeCancellationPreviewFreeCancellation" = Field(
        alias="previewFreeCancellation"
    )


class PreviewFreeCancellationPreviewFreeCancellation(FreeCancellationReviewDefault):
    """Version-bound review of a scheduled move to Free. Future usage remains payable and is not estimated as zero."""

    pass


PreviewFreeCancellation.model_rebuild()

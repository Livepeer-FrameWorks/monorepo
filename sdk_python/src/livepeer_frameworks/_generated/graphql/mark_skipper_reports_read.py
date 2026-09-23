from pydantic import Field

from .base_model import BaseModel


class MarkSkipperReportsRead(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    mark_skipper_reports_read: int = Field(
        alias="markSkipperReportsRead",
        description="Mark Skipper investigation reports as read.\nPass specific IDs, or omit to mark all as read.\nReturns the number of reports marked.",
    )
    "Mark Skipper investigation reports as read.\nPass specific IDs, or omit to mark all as read.\nReturns the number of reports marked."

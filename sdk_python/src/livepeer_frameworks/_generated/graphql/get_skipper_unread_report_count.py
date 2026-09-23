from pydantic import Field

from .base_model import BaseModel


class GetSkipperUnreadReportCount(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    skipper_unread_report_count: int = Field(
        alias="skipperUnreadReportCount",
        description="Get count of unread Skipper investigation reports.",
    )
    "Get count of unread Skipper investigation reports."

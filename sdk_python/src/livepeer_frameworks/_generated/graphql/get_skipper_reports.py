from pydantic import Field

from .base_model import BaseModel
from .fragments import SkipperReportsConnectionDefault


class GetSkipperReports(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    skipper_reports: "GetSkipperReportsSkipperReports" = Field(
        alias="skipperReports",
        description="List Skipper investigation reports for the current tenant.\nReturns newest first with read/unread status and counts.",
    )
    "List Skipper investigation reports for the current tenant.\nReturns newest first with read/unread status and counts."


GetSkipperReportsSkipperReports = SkipperReportsConnectionDefault
GetSkipperReports.model_rebuild()

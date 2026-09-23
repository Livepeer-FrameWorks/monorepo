from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import SkipperReportDefault


class GetSkipperReport(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    skipper_report: Optional["GetSkipperReportSkipperReport"] = Field(
        alias="skipperReport",
        description="Fetch a single Skipper investigation report by id.\nReturns null when the report does not exist or is not owned by the current tenant.",
    )
    "Fetch a single Skipper investigation report by id.\nReturns null when the report does not exist or is not owned by the current tenant."


GetSkipperReportSkipperReport = SkipperReportDefault
GetSkipperReport.model_rebuild()

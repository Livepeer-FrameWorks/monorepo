from pydantic import Field

from .base_model import BaseModel
from .fragments import IncidentDefault, PageInfoDefault


class GetIncidentsConnection(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    incidents_connection: "GetIncidentsConnectionIncidentsConnection" = Field(
        alias="incidentsConnection",
        description="Incidents raised by platform alerting on clusters the current tenant owns,\nnewest first.",
    )
    "Incidents raised by platform alerting on clusters the current tenant owns,\nnewest first."


class GetIncidentsConnectionIncidentsConnection(BaseModel):
    edges: list["GetIncidentsConnectionIncidentsConnectionEdges"]
    page_info: "GetIncidentsConnectionIncidentsConnectionPageInfo" = Field(
        alias="pageInfo"
    )
    total_count: int = Field(alias="totalCount")


class GetIncidentsConnectionIncidentsConnectionEdges(BaseModel):
    cursor: str
    node: "GetIncidentsConnectionIncidentsConnectionEdgesNode"


GetIncidentsConnectionIncidentsConnectionEdgesNode = IncidentDefault
GetIncidentsConnectionIncidentsConnectionPageInfo = PageInfoDefault
GetIncidentsConnection.model_rebuild()
GetIncidentsConnectionIncidentsConnection.model_rebuild()
GetIncidentsConnectionIncidentsConnectionEdges.model_rebuild()

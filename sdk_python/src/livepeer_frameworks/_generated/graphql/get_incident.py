from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import IncidentDetailDefault


class GetIncident(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    incident: Optional["GetIncidentIncident"] = Field(
        description="One incident with its alerts and timeline. Null when the incident does not\nexist or is not visible to the caller."
    )
    "One incident with its alerts and timeline. Null when the incident does not\nexist or is not visible to the caller."


GetIncidentIncident = IncidentDetailDefault
GetIncident.model_rebuild()

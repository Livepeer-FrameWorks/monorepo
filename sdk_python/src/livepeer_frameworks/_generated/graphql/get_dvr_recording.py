from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import DVRRequest


class GetDvrRecording(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    dvr_recording: Optional["GetDvrRecordingDvrRecording"] = Field(
        alias="dvrRecording",
        description="A recording from the tenant catalog, readable with streams:read.",
    )
    "A recording from the tenant catalog, readable with streams:read."


GetDvrRecordingDvrRecording = DVRRequest
GetDvrRecording.model_rebuild()

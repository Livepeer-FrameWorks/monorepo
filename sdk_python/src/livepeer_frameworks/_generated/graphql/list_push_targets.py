from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import PushTarget


class ListPushTargets(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    stream: Optional["ListPushTargetsStream"] = Field(
        description="Fetch a single stream by its global ID."
    )
    "Fetch a single stream by its global ID."


class ListPushTargetsStream(BaseModel):
    """A live stream configuration with real-time operational metrics.
    Streams are the core entity for broadcasting and viewing live content."""

    id: str = Field(description="Global unique identifier for Relay compatibility.")
    "Global unique identifier for Relay compatibility."
    push_targets: list["ListPushTargetsStreamPushTargets"] = Field(
        alias="pushTargets",
        description="Configured multistream push targets for this stream.",
    )
    "Configured multistream push targets for this stream."


ListPushTargetsStreamPushTargets = PushTarget
ListPushTargets.model_rebuild()
ListPushTargetsStream.model_rebuild()

from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import StreamValidationDefault


class GetValidateStreamKey(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    validate_stream_key: Optional["GetValidateStreamKeyValidateStreamKey"] = Field(
        alias="validateStreamKey",
        description="Validate a stream key and return the associated stream info.",
    )
    "Validate a stream key and return the associated stream info."


GetValidateStreamKeyValidateStreamKey = StreamValidationDefault
GetValidateStreamKey.model_rebuild()

from pydantic import Field

from .base_model import BaseModel
from .fragments import TranscodeLadderPolicyDefault


class GetTranscodeLadderPolicy(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    transcode_ladder_policy: "GetTranscodeLadderPolicyTranscodeLadderPolicy" = Field(
        alias="transcodeLadderPolicy",
        description="Allowed video ladders, account choice, and effective default.",
    )
    "Allowed video ladders, account choice, and effective default."


GetTranscodeLadderPolicyTranscodeLadderPolicy = TranscodeLadderPolicyDefault
GetTranscodeLadderPolicy.model_rebuild()

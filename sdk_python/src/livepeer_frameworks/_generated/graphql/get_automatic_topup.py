from pydantic import Field

from .base_model import BaseModel
from .fragments import AutomaticTopupSettingsDefault


class GetAutomaticTopup(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    automatic_topup: "GetAutomaticTopupAutomaticTopup" = Field(alias="automaticTopup")


GetAutomaticTopupAutomaticTopup = AutomaticTopupSettingsDefault
GetAutomaticTopup.model_rebuild()

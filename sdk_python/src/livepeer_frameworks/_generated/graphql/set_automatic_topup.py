from pydantic import Field

from .base_model import BaseModel
from .fragments import AutomaticTopupSettingsDefault


class SetAutomaticTopup(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    set_automatic_topup: "SetAutomaticTopupSetAutomaticTopup" = Field(
        alias="setAutomaticTopup"
    )


SetAutomaticTopupSetAutomaticTopup = AutomaticTopupSettingsDefault
SetAutomaticTopup.model_rebuild()

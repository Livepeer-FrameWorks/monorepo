from pydantic import Field

from .base_model import BaseModel
from .fragments import MollieMandateDefault


class GetMollieMandates(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    mollie_mandates: list["GetMollieMandatesMollieMandates"] = Field(
        alias="mollieMandates",
        description="List Mollie mandates for the current tenant.",
    )
    "List Mollie mandates for the current tenant."


class GetMollieMandatesMollieMandates(MollieMandateDefault):
    """Mollie Mandate - recurring payment authorization for a customer."""

    pass


GetMollieMandates.model_rebuild()

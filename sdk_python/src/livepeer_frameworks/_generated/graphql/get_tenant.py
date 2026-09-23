from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import TenantDefault


class GetTenant(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    tenant: Optional["GetTenantTenant"] = Field(
        description="Get the current tenant's profile and settings."
    )
    "Get the current tenant's profile and settings."


GetTenantTenant = TenantDefault
GetTenant.model_rebuild()

from pydantic import Field

from .base_model import BaseModel
from .fragments import (  # noqa: F401
    CapabilitiesDefault,
    CapabilitiesDefaultClusters,
    CapabilitiesDefaultTenant,
)


class GetCapabilities(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    capabilities: "GetCapabilitiesCapabilities" = Field(
        description="What the current tenant can use, built only from gates the platform enforces.\nDescribes enforcement; it never grants access itself."
    )
    "What the current tenant can use, built only from gates the platform enforces.\nDescribes enforcement; it never grants access itself."


class GetCapabilitiesCapabilities(CapabilitiesDefault):
    """Enforced gates for the current tenant. Each section is read from the service
    that enforces it and cached per tenant for 30 seconds. When that service is
    unavailable, the last cached section is returned; without one the section is
    null and the response carries an error."""

    pass


GetCapabilities.model_rebuild()

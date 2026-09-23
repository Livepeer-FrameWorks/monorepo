from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import ServiceInstanceHealthDefault


class GetServiceInstancesHealth(BaseModel):
    """Root Query type - the entry point for all read operations.

    List and object fields are nullable per GraphQL best practices,
    enabling graceful degradation when individual services are unavailable.
    Most connection fields are non-null, but some may be nullable when upstream data is optional."""

    service_instances_health: Optional[
        list["GetServiceInstancesHealthServiceInstancesHealth"]
    ] = Field(
        alias="serviceInstancesHealth",
        description="Check health status of service instances for owned infrastructure.",
    )
    "Check health status of service instances for owned infrastructure."


GetServiceInstancesHealthServiceInstancesHealth = ServiceInstanceHealthDefault
GetServiceInstancesHealth.model_rebuild()

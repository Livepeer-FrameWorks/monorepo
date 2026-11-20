from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthErrorDefault,
    InfrastructureNodeDefault,
    InfrastructureNodeDefaultCapacity,
    InfrastructureNodeDefaultLiveState,
    InfrastructureNodeDefaultRoutingImpactPreview,
    NotFoundErrorDefault,
    ValidationErrorDefault,
)


class SetNodeCapacity(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    set_node_capacity: Annotated[
        Union[
            "SetNodeCapacitySetNodeCapacityInfrastructureNode",
            "SetNodeCapacitySetNodeCapacityValidationError",
            "SetNodeCapacitySetNodeCapacityNotFoundError",
            "SetNodeCapacitySetNodeCapacityAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="setNodeCapacity",
        description="Replace both directional node budgets. Null restores the default 1 Gbit/s budget. Requires infrastructure:write and ownership or platform operator access.",
    )
    "Replace both directional node budgets. Null restores the default 1 Gbit/s budget. Requires infrastructure:write and ownership or platform operator access."


class SetNodeCapacitySetNodeCapacityInfrastructureNode(InfrastructureNodeDefault):
    typename__: Literal["InfrastructureNode"] = Field(alias="__typename")


class SetNodeCapacitySetNodeCapacityValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class SetNodeCapacitySetNodeCapacityNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class SetNodeCapacitySetNodeCapacityAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


SetNodeCapacity.model_rebuild()

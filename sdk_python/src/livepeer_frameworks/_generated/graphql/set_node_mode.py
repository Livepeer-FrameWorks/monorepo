from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthErrorDefault,
    InfrastructureNodeDefault,
    InfrastructureNodeDefaultLiveState,
    InfrastructureNodeDefaultRoutingImpactPreview,
    NotFoundErrorDefault,
    ValidationErrorDefault,
)


class SetNodeMode(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    set_node_mode: Annotated[
        Union[
            "SetNodeModeSetNodeModeInfrastructureNode",
            "SetNodeModeSetNodeModeValidationError",
            "SetNodeModeSetNodeModeNotFoundError",
            "SetNodeModeSetNodeModeAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="setNodeMode",
        description="Set a node's operational mode. Drains/maintenance bleed traffic away from\nthe node; restoring with NORMAL re-admits it to routing. Reason is\nrecorded in Foghorn's audit trail.",
    )
    "Set a node's operational mode. Drains/maintenance bleed traffic away from\nthe node; restoring with NORMAL re-admits it to routing. Reason is\nrecorded in Foghorn's audit trail."


class SetNodeModeSetNodeModeInfrastructureNode(InfrastructureNodeDefault):
    typename__: Literal["InfrastructureNode"] = Field(alias="__typename")


class SetNodeModeSetNodeModeValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class SetNodeModeSetNodeModeNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class SetNodeModeSetNodeModeAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


SetNodeMode.model_rebuild()

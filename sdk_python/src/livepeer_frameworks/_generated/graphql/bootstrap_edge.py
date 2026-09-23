from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    BootstrapEdgeResponseDefault,
    BootstrapEdgeResponseDefaultTelemetry,  # noqa: F401
    ValidationErrorDefault,
)


class BootstrapEdge(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    bootstrap_edge: Annotated[
        Union[
            "BootstrapEdgeBootstrapEdgeBootstrapEdgeResponse",
            "BootstrapEdgeBootstrapEdgeValidationError",
            "BootstrapEdgeBootstrapEdgeAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="bootstrapEdge",
        description="Bootstrap a new edge using only an opaque bootstrap token.\n\nPublic — the bootstrap token is itself the credential. Bridge resolves\nthe token's cluster via Quartermaster, finds the cluster's assigned\nFoghorn, and proxies a PreRegisterEdge call so the operator never has\nto know cluster topology.",
    )
    "Bootstrap a new edge using only an opaque bootstrap token.\n\nPublic — the bootstrap token is itself the credential. Bridge resolves\nthe token's cluster via Quartermaster, finds the cluster's assigned\nFoghorn, and proxies a PreRegisterEdge call so the operator never has\nto know cluster topology."


class BootstrapEdgeBootstrapEdgeBootstrapEdgeResponse(BootstrapEdgeResponseDefault):
    typename__: Literal["BootstrapEdgeResponse"] = Field(alias="__typename")


class BootstrapEdgeBootstrapEdgeValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class BootstrapEdgeBootstrapEdgeAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


BootstrapEdge.model_rebuild()

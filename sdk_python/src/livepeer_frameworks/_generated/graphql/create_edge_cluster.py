from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    AuthErrorDefault,
    CreateEdgeClusterResponseDefault,
    CreateEdgeClusterResponseDefaultBootstrapToken,
    CreateEdgeClusterResponseDefaultCluster,
    ValidationErrorDefault,
)


class CreateEdgeCluster(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_edge_cluster: Annotated[
        Union[
            "CreateEdgeClusterCreateEdgeClusterCreateEdgeClusterResponse",
            "CreateEdgeClusterCreateEdgeClusterValidationError",
            "CreateEdgeClusterCreateEdgeClusterAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="createEdgeCluster",
        description="Create an edge cluster with automatic Foghorn assignment and enrollment token.",
    )
    "Create an edge cluster with automatic Foghorn assignment and enrollment token."


class CreateEdgeClusterCreateEdgeClusterCreateEdgeClusterResponse(
    CreateEdgeClusterResponseDefault
):
    typename__: Literal["CreateEdgeClusterResponse"] = Field(alias="__typename")


class CreateEdgeClusterCreateEdgeClusterValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreateEdgeClusterCreateEdgeClusterAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreateEdgeCluster.model_rebuild()

from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    IncidentDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
)


class ResolveIncident(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    resolve_incident: Annotated[
        Union[
            "ResolveIncidentResolveIncidentIncident",
            "ResolveIncidentResolveIncidentValidationError",
            "ResolveIncidentResolveIncidentNotFoundError",
            "ResolveIncidentResolveIncidentAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="resolveIncident",
        description="Resolve an incident manually. Repeats of the same firing alerts do not\nreopen it; a new alert opens a new incident.",
    )
    "Resolve an incident manually. Repeats of the same firing alerts do not\nreopen it; a new alert opens a new incident."


class ResolveIncidentResolveIncidentIncident(IncidentDefault):
    typename__: Literal["Incident"] = Field(alias="__typename")


class ResolveIncidentResolveIncidentValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class ResolveIncidentResolveIncidentNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class ResolveIncidentResolveIncidentAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


ResolveIncident.model_rebuild()

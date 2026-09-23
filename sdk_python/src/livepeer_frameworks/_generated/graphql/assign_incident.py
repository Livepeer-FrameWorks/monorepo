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


class AssignIncident(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    assign_incident: Annotated[
        Union[
            "AssignIncidentAssignIncidentIncident",
            "AssignIncidentAssignIncidentValidationError",
            "AssignIncidentAssignIncidentNotFoundError",
            "AssignIncidentAssignIncidentAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="assignIncident",
        description="Assign an incident to a user of the incident's tenant. A null assignee\nclears the assignment.",
    )
    "Assign an incident to a user of the incident's tenant. A null assignee\nclears the assignment."


class AssignIncidentAssignIncidentIncident(IncidentDefault):
    typename__: Literal["Incident"] = Field(alias="__typename")


class AssignIncidentAssignIncidentValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class AssignIncidentAssignIncidentNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class AssignIncidentAssignIncidentAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


AssignIncident.model_rebuild()

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


class AddIncidentNote(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    add_incident_note: Annotated[
        Union[
            "AddIncidentNoteAddIncidentNoteIncident",
            "AddIncidentNoteAddIncidentNoteValidationError",
            "AddIncidentNoteAddIncidentNoteNotFoundError",
            "AddIncidentNoteAddIncidentNoteAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="addIncidentNote", description="Add a note to an incident's timeline."
    )
    "Add a note to an incident's timeline."


class AddIncidentNoteAddIncidentNoteIncident(IncidentDefault):
    typename__: Literal["Incident"] = Field(alias="__typename")


class AddIncidentNoteAddIncidentNoteValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class AddIncidentNoteAddIncidentNoteNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class AddIncidentNoteAddIncidentNoteAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


AddIncidentNote.model_rebuild()

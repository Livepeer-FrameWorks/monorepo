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


class AcknowledgeIncident(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    acknowledge_incident: Annotated[
        Union[
            "AcknowledgeIncidentAcknowledgeIncidentIncident",
            "AcknowledgeIncidentAcknowledgeIncidentValidationError",
            "AcknowledgeIncidentAcknowledgeIncidentNotFoundError",
            "AcknowledgeIncidentAcknowledgeIncidentAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="acknowledgeIncident",
        description="Acknowledge an incident. The incident stays open until its alerts resolve\nor someone resolves it.",
    )
    "Acknowledge an incident. The incident stays open until its alerts resolve\nor someone resolves it."


class AcknowledgeIncidentAcknowledgeIncidentIncident(IncidentDefault):
    typename__: Literal["Incident"] = Field(alias="__typename")


class AcknowledgeIncidentAcknowledgeIncidentValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class AcknowledgeIncidentAcknowledgeIncidentNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class AcknowledgeIncidentAcknowledgeIncidentAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


AcknowledgeIncident.model_rebuild()

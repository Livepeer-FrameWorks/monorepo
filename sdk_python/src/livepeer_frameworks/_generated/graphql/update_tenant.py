from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    TenantDefault,
    TenantDefaultCustomDomainStatus,  # noqa: F401
    ValidationErrorDefault,
)


class UpdateTenant(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    update_tenant: Annotated[
        Union[
            "UpdateTenantUpdateTenantTenant",
            "UpdateTenantUpdateTenantValidationError",
            "UpdateTenantUpdateTenantAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(alias="updateTenant", description="Update the current tenant's profile.")
    "Update the current tenant's profile."


class UpdateTenantUpdateTenantTenant(TenantDefault):
    typename__: Literal["Tenant"] = Field(alias="__typename")


class UpdateTenantUpdateTenantValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class UpdateTenantUpdateTenantAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


UpdateTenant.model_rebuild()

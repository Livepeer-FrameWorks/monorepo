from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    PaymentDefault,
    PaymentDefaultConversion,  # noqa: F401
    ValidationErrorDefault,
)


class CreatePayment(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    create_payment: Annotated[
        Union[
            "CreatePaymentCreatePaymentPayment",
            "CreatePaymentCreatePaymentValidationError",
            "CreatePaymentCreatePaymentAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="createPayment", description="Create a payment for subscription or usage."
    )
    "Create a payment for subscription or usage."


class CreatePaymentCreatePaymentPayment(PaymentDefault):
    typename__: Literal["Payment"] = Field(alias="__typename")


class CreatePaymentCreatePaymentValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class CreatePaymentCreatePaymentAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


CreatePayment.model_rebuild()

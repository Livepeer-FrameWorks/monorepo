from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (
    AuthErrorDefault,
    NotFoundErrorDefault,
    ValidationErrorDefault,
    X402PaymentResultDefault,
)


class SubmitX402Payment(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    submit_x_402_payment: Annotated[
        Union[
            "SubmitX402PaymentSubmitX402PaymentX402PaymentResult",
            "SubmitX402PaymentSubmitX402PaymentValidationError",
            "SubmitX402PaymentSubmitX402PaymentNotFoundError",
            "SubmitX402PaymentSubmitX402PaymentAuthError",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="submitX402Payment",
        description="Submit an x402 payment payload to settle a 402 response or top up balance.\nAuthenticated members may settle viewer resources; direct top-ups and\nnon-viewer resources require billing management authority on the target tenant.",
    )
    "Submit an x402 payment payload to settle a 402 response or top up balance.\nAuthenticated members may settle viewer resources; direct top-ups and\nnon-viewer resources require billing management authority on the target tenant."


class SubmitX402PaymentSubmitX402PaymentX402PaymentResult(X402PaymentResultDefault):
    typename__: Literal["X402PaymentResult"] = Field(alias="__typename")


class SubmitX402PaymentSubmitX402PaymentValidationError(ValidationErrorDefault):
    typename__: Literal["ValidationError"] = Field(alias="__typename")


class SubmitX402PaymentSubmitX402PaymentNotFoundError(NotFoundErrorDefault):
    typename__: Literal["NotFoundError"] = Field(alias="__typename")


class SubmitX402PaymentSubmitX402PaymentAuthError(AuthErrorDefault):
    typename__: Literal["AuthError"] = Field(alias="__typename")


SubmitX402Payment.model_rebuild()

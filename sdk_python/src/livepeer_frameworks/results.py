"""Result unions: success members are returned, error members raised."""

from __future__ import annotations

from typing import Any, TypeVar, overload

from pydantic import BaseModel

from .errors import ProtocolError, ResultError

T = TypeVar("T", bound=BaseModel)


@overload
def expect_result(value: object, success: type[T], /) -> T: ...


@overload
def expect_result(value: Any, *success: str) -> Any: ...


def expect_result(value: Any, *success: Any) -> Any:
    """Returns a result union member when it is the success member, and
    raises ResultError for any other member (ValidationError, NotFoundError,
    AuthError, RateLimitError, or an UnknownMember, whose ResultError carries
    its __typename, message, and code). value is a generated model or raw response
    data; success is the generated success class, which narrows the type, or
    one or more __typename strings:

        stream = expect_result(created.create_stream, CreateStreamCreateStreamStream)
        stream = expect_result(created.create_stream, "Stream")
    """
    if value is None:
        raise ProtocolError("result is missing")
    typename = value.get("__typename") if isinstance(value, dict) else getattr(value, "typename__", None)
    for want in success:
        if (isinstance(want, type) and isinstance(value, want)) or want == typename:
            return value
    dumped: dict[str, Any] = value.model_dump(by_alias=True, mode="json") if isinstance(value, BaseModel) else dict(value)
    raise ResultError(dumped)

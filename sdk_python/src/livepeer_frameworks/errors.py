"""Typed errors. Every error the SDK raises for a failed call is a
FrameWorksError; the subclass says what failed. The class names are shared
with the TypeScript and Go SDKs."""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from typing import Any


class FrameWorksError(Exception):
    """Base of every SDK error.

    status is the HTTP status of the response (None when the failure was not
    an HTTP status); code is extensions.code of a GraphQL error or the code
    field of an HTTP error body; retry_after_seconds is the wait the server
    asked for; body is the parsed response body, when there was one.
    """

    def __init__(
        self,
        message: str,
        *,
        status: int | None = None,
        code: str | None = None,
        retry_after_seconds: int | None = None,
        body: Any = None,
    ) -> None:
        super().__init__(message)
        self.message = message
        self.status = status
        self.code = code
        self.retry_after_seconds = retry_after_seconds
        self.body = body


class NetworkError(FrameWorksError):
    """The request never produced a response: DNS, TCP, TLS, or a reset connection."""


class HTTPError(FrameWorksError):
    """A non-2xx response without a GraphQL body that no more specific class covers."""


class AuthenticationError(FrameWorksError):
    """The credentials were missing, invalid, or expired: HTTP 401, a GraphQL
    UNAUTHORIZED error, or a subscription socket closed before it was
    acknowledged."""


class PaymentRequiredError(FrameWorksError):
    """HTTP 402: the call needs a balance, a payment method, or an x402 payment (see body)."""


class RateLimitError(FrameWorksError):
    """HTTP 429 or a GraphQL RATE_LIMITED error; retry_after_seconds says when to try again."""


class ServerError(FrameWorksError):
    """An HTTP 5xx response."""


class ProtocolError(FrameWorksError):
    """The response was not a GraphQL response: not JSON, or neither data nor errors."""


@dataclass(frozen=True)
class PartialErrors:
    """The GraphQL errors of a call that still returned its data: each failed
    a field below a root field that came back, which the server set to None
    (for example Stream.metrics for an API token without analytics:read). The
    call returns its data; these reach the on_partial_errors handler of the
    call or the client. Errors without a path, errors that null a root field,
    and UNAUTHORIZED, RATE_LIMITED, and document errors raise instead."""

    #: The operation name, or None for an anonymous document.
    operation_name: str | None
    errors: list[Mapping[str, Any]]


class GraphQLError(FrameWorksError):
    """The server answered with GraphQL errors. errors holds every entry; data holds any partial result."""

    def __init__(
        self,
        message: str,
        errors: Sequence[Mapping[str, Any]],
        data: Any = None,
        **details: Any,
    ) -> None:
        super().__init__(message, **details)
        self.errors = list(errors)
        path = self.errors[0].get("path") if self.errors else None
        self.path: list[str | int] | None = list(path) if isinstance(path, list) else None
        self.data = data


class SchemaMismatchError(GraphQLError):
    """The server rejected the document itself (GRAPHQL_VALIDATION_FAILED or
    GRAPHQL_PARSE_FAILED): it does not know a field or argument this SDK sends."""


class ResultError(FrameWorksError):
    """An error member of a result union, raised by expect_result."""

    def __init__(self, result: Mapping[str, Any]) -> None:
        def text(key: str) -> str | None:
            value = result.get(key)
            return value if isinstance(value, str) else None

        typename = text("__typename") or "unknown"
        retry_after = result.get("retryAfter")
        super().__init__(
            text("message") or f"unexpected result {typename}",
            code=text("code"),
            retry_after_seconds=retry_after if isinstance(retry_after, int) else None,
        )
        self.typename = typename
        self.field = text("field")
        self.constraint = text("constraint")
        self.resource_type = text("resourceType")
        self.resource_id = text("resourceId")
        self.result = dict(result)


class ServerTooOldError(FrameWorksError):
    """The server is a stable release older than the oldest release this SDK line supports.

    server_version is None when the server predates serverInfo."""

    def __init__(self, server_version: str | None, minimum_version: str) -> None:
        if server_version:
            message = (
                f"FrameWorks server {server_version} is older than {minimum_version}, "
                "the oldest release this client can use"
            )
        else:
            message = f"FrameWorks server predates serverInfo; this client needs {minimum_version} or later"
        super().__init__(message)
        self.server_version = server_version
        self.minimum_version = minimum_version


class UnsupportedOperationError(FrameWorksError):
    """The operation was added in a release newer than the server."""

    def __init__(self, operation: str, since: str, server_version: str) -> None:
        super().__init__(f"{operation} needs FrameWorks {since} or later; the server runs {server_version}")
        self.operation = operation
        self.since = since
        self.server_version = server_version


class UploadError(FrameWorksError):
    """A VOD upload failed while sending its parts. The upload was aborted."""

    def __init__(self, message: str, upload_id: str, part_number: int | None, **details: Any) -> None:
        super().__init__(message, **details)
        self.upload_id = upload_id
        self.part_number = part_number


class WebhookVerificationError(FrameWorksError):
    """The signature, timestamp, or headers of a webhook request did not verify."""

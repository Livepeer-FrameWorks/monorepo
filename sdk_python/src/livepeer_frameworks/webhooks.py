"""Webhook verification and typed parsing. verify checks the Standard
Webhooks signature with the official standardwebhooks library; parse decodes
the body into the generated payload message of its event type. An event type
this SDK version does not know is returned as a raw event, so a new event type
never breaks an older SDK."""

from __future__ import annotations

import dataclasses
import json
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any

import betterproto2
from standardwebhooks import Webhook, WebhookVerificationError as _StandardVerificationError

from ._generated.events import PUBLIC_EVENT_MESSAGES
from .errors import FrameWorksError, WebhookVerificationError

__all__ = ["WebhookEvent", "WebhookReceiver", "WebhookVerificationError", "parse_webhook_event", "PUBLIC_EVENT_MESSAGES"]

_SIGNATURE_HEADERS = ("webhook-id", "webhook-timestamp", "webhook-signature")


@dataclass(frozen=True)
class WebhookEvent:
    """One webhook event. id equals the webhook-id header; deduplicate on it.
    known is True when this SDK knows type: data is then the decoded payload
    message. For any other type data is None and raw_data holds the payload as
    received."""

    id: str
    type: str
    api_version: str
    created_at: str
    known: bool
    data: betterproto2.Message | None
    raw_data: Any


def _camel(name: str) -> str:
    head, *rest = name.split("_")
    return head + "".join(part.title() for part in rest)


def _sanitize(cls: type[betterproto2.Message], value: Any) -> Any:
    """Drops the fields and enum names a newer server may send that the
    generated message does not know, so decoding never fails on them."""
    if not isinstance(value, dict):
        return value
    message_cls: Any = cls
    fields = {f.name: f for f in dataclasses.fields(message_cls)}
    by_json = {_camel(name): name for name in fields}
    types: Mapping[str, Any] = message_cls._betterproto.cls_by_field
    out: dict[str, Any] = {}
    for key, item in value.items():
        name = by_json.get(key) or (key if key in fields else None)
        if name is None:
            continue
        meta = betterproto2.FieldMetadata.get(fields[name])
        field_type = types.get(name)
        if meta.proto_type == "enum" and isinstance(field_type, type):
            items = item if isinstance(item, list) else [item]
            kept = [v for v in items if _known_enum(field_type, v)]
            if meta.repeated:
                out[key] = kept
            elif kept:
                out[key] = item
            continue
        if meta.proto_type == "message" and meta.unwrap is None and isinstance(field_type, type):
            if isinstance(item, list):
                out[key] = [_sanitize(field_type, v) for v in item]
            else:
                out[key] = _sanitize(field_type, item)
            continue
        out[key] = item
    return out


def _known_enum(enum_cls: Any, value: Any) -> bool:
    if isinstance(value, int):
        return True
    # Webhooks carry the full proto names (ARTIFACT_KIND_CLIP); the generated
    # members drop the prefix (CLIP).
    if isinstance(value, str) and value in enum_cls.betterproto_renamed_proto_names_to_value():
        return True
    try:
        enum_cls.from_string(value)
    except (ValueError, KeyError):
        return False
    return True


def parse_webhook_event(payload: bytes | str) -> WebhookEvent:
    """Parses a webhook body into a typed event, without verifying it."""
    try:
        body = json.loads(payload)
    except ValueError as err:
        raise FrameWorksError("webhook body is not JSON") from err
    if not isinstance(body, dict):
        raise FrameWorksError("webhook body is not an event object")
    event_type = str(body.get("type") or "")
    raw = body.get("data")
    message_cls = PUBLIC_EVENT_MESSAGES.get(event_type)
    data = message_cls.from_dict(_sanitize(message_cls, raw or {})) if message_cls else None
    return WebhookEvent(
        id=str(body.get("id") or ""),
        type=event_type,
        api_version=str(body.get("api_version") or ""),
        created_at=str(body.get("created_at") or ""),
        known=message_cls is not None,
        data=data,
        raw_data=raw,
    )


class WebhookReceiver:
    """A receiver for one endpoint secret (whsec_...). Pass the raw request
    body: parsing and re-serializing JSON changes the bytes that were
    signed."""

    def __init__(self, secret: str) -> None:
        self._webhook = Webhook(secret)

    def verify(self, payload: bytes | str, headers: Mapping[str, str]) -> None:
        """Raises WebhookVerificationError unless the headers sign payload with
        this secret within five minutes."""
        signature_headers = {k.lower(): v for k, v in headers.items() if k.lower() in _SIGNATURE_HEADERS}
        try:
            self._webhook.verify(payload, signature_headers, json_parse=False)
        except _StandardVerificationError as err:
            raise WebhookVerificationError(str(err)) from err
        except ValueError as err:
            # A malformed signature header fails inside the library's parsing.
            raise WebhookVerificationError(f"invalid signature header: {err}") from err

    def receive(self, payload: bytes | str, headers: Mapping[str, str]) -> WebhookEvent:
        """verify, then parse_webhook_event."""
        self.verify(payload, headers)
        return parse_webhook_event(payload)

from typing import Annotated, Literal, Union

from pydantic import Field

from ..._forward import OpenUnion, UnknownMember
from .base_model import BaseModel
from .fragments import (  # noqa: F401
    SkipperDoneDefault,
    SkipperMetaDefault,
    SkipperMetaDefaultBlocks,
    SkipperMetaDefaultCitations,
    SkipperMetaDefaultDetails,
    SkipperMetaDefaultExternalLinks,
    SkipperTokenDefault,
    SkipperToolEndEventDefault,
    SkipperToolStartEventDefault,
)


class SkipperChat(BaseModel):
    """Real-time subscriptions for live event streaming via WebSocket.
    All subscriptions are tenant-scoped and require authentication.
    Events are delivered as they occur with minimal latency."""

    skipper_chat: Annotated[
        Union[
            "SkipperChatSkipperChatSkipperToken",
            "SkipperChatSkipperChatSkipperToolStartEvent",
            "SkipperChatSkipperChatSkipperToolEndEvent",
            "SkipperChatSkipperChatSkipperMeta",
            "SkipperChatSkipperChatSkipperDone",
            UnknownMember,
        ],
        OpenUnion(),
    ] = Field(
        alias="skipperChat",
        description="Stream chat events from the Skipper AI consultant.\nReceives token chunks, tool lifecycle events, metadata, and completion signal.\nCreates a new conversation if conversationId is omitted.",
    )
    "Stream chat events from the Skipper AI consultant.\nReceives token chunks, tool lifecycle events, metadata, and completion signal.\nCreates a new conversation if conversationId is omitted."


class SkipperChatSkipperChatSkipperToken(SkipperTokenDefault):
    typename__: Literal["SkipperToken"] = Field(alias="__typename")


class SkipperChatSkipperChatSkipperToolStartEvent(SkipperToolStartEventDefault):
    typename__: Literal["SkipperToolStartEvent"] = Field(alias="__typename")


class SkipperChatSkipperChatSkipperToolEndEvent(SkipperToolEndEventDefault):
    typename__: Literal["SkipperToolEndEvent"] = Field(alias="__typename")


class SkipperChatSkipperChatSkipperMeta(SkipperMetaDefault):
    typename__: Literal["SkipperMeta"] = Field(alias="__typename")


class SkipperChatSkipperChatSkipperDone(SkipperDoneDefault):
    typename__: Literal["SkipperDone"] = Field(alias="__typename")


SkipperChat.model_rebuild()

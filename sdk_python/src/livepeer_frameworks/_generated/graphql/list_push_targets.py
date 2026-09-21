from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import PushTarget


class ListPushTargets(BaseModel):
    stream: Optional["ListPushTargetsStream"]


class ListPushTargetsStream(BaseModel):
    id: str
    push_targets: list["ListPushTargetsStreamPushTargets"] = Field(alias="pushTargets")


ListPushTargetsStreamPushTargets = PushTarget
ListPushTargets.model_rebuild()
ListPushTargetsStream.model_rebuild()

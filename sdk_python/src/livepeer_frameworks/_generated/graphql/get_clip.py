from typing import Optional

from .base_model import BaseModel
from .fragments import Clip


class GetClip(BaseModel):
    clip: Optional["GetClipClip"]


GetClipClip = Clip
GetClip.model_rebuild()

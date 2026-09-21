from typing import Optional

from .base_model import BaseModel
from .fragments import ClipFields


class GetClip(BaseModel):
    clip: Optional["GetClipClip"]


GetClipClip = ClipFields
GetClip.model_rebuild()

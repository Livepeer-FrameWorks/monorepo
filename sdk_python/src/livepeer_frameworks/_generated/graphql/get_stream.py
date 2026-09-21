from typing import Optional

from .base_model import BaseModel
from .fragments import Stream


class GetStream(BaseModel):
    stream: Optional["GetStreamStream"]


GetStreamStream = Stream
GetStream.model_rebuild()

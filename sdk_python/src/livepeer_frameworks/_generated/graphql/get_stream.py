from typing import Optional

from .base_model import BaseModel
from .fragments import StreamFields


class GetStream(BaseModel):
    stream: Optional["GetStreamStream"]


GetStreamStream = StreamFields
GetStream.model_rebuild()

from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import SigningKey


class GetSigningKey(BaseModel):
    signing_key: Optional["GetSigningKeySigningKey"] = Field(alias="signingKey")


GetSigningKeySigningKey = SigningKey
GetSigningKey.model_rebuild()

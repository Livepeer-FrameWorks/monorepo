from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import VodAssetFields


class GetVodAsset(BaseModel):
    vod_asset: Optional["GetVodAssetVodAsset"] = Field(alias="vodAsset")


GetVodAssetVodAsset = VodAssetFields
GetVodAsset.model_rebuild()

from typing import Optional

from pydantic import Field

from .base_model import BaseModel
from .fragments import VodAsset


class GetVodAsset(BaseModel):
    vod_asset: Optional["GetVodAssetVodAsset"] = Field(alias="vodAsset")


GetVodAssetVodAsset = VodAsset
GetVodAsset.model_rebuild()

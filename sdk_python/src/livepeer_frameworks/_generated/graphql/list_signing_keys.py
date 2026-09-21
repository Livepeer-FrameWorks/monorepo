from pydantic import Field

from .base_model import BaseModel
from .fragments import PageInfo, SigningKey


class ListSigningKeys(BaseModel):
    signing_keys_connection: "ListSigningKeysSigningKeysConnection" = Field(
        alias="signingKeysConnection"
    )


class ListSigningKeysSigningKeysConnection(BaseModel):
    nodes: list["ListSigningKeysSigningKeysConnectionNodes"]
    page_info: "ListSigningKeysSigningKeysConnectionPageInfo" = Field(alias="pageInfo")
    total_count: int = Field(alias="totalCount")


ListSigningKeysSigningKeysConnectionNodes = SigningKey
ListSigningKeysSigningKeysConnectionPageInfo = PageInfo
ListSigningKeys.model_rebuild()
ListSigningKeysSigningKeysConnection.model_rebuild()

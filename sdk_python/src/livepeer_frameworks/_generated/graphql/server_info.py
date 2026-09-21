from pydantic import Field

from .base_model import BaseModel


class ServerInfo(BaseModel):
    server_info: "ServerInfoServerInfo" = Field(alias="serverInfo")


class ServerInfoServerInfo(BaseModel):
    version: str
    features: list[str]


ServerInfo.model_rebuild()

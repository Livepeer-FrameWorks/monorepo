from pydantic import Field

from .base_model import BaseModel
from .fragments import PushTargetFields


class CreatePushTarget(BaseModel):
    create_push_target: "CreatePushTargetCreatePushTarget" = Field(
        alias="createPushTarget"
    )


CreatePushTargetCreatePushTarget = PushTargetFields
CreatePushTarget.model_rebuild()

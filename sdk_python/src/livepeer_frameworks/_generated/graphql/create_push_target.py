from pydantic import Field

from .base_model import BaseModel
from .fragments import PushTarget


class CreatePushTarget(BaseModel):
    create_push_target: "CreatePushTargetCreatePushTarget" = Field(
        alias="createPushTarget"
    )


CreatePushTargetCreatePushTarget = PushTarget
CreatePushTarget.model_rebuild()

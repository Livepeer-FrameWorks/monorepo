from pydantic import Field

from .base_model import BaseModel
from .fragments import PushTarget


class UpdatePushTarget(BaseModel):
    update_push_target: "UpdatePushTargetUpdatePushTarget" = Field(
        alias="updatePushTarget"
    )


UpdatePushTargetUpdatePushTarget = PushTarget
UpdatePushTarget.model_rebuild()

from pydantic import Field

from .base_model import BaseModel
from .fragments import PushTargetFields


class UpdatePushTarget(BaseModel):
    update_push_target: "UpdatePushTargetUpdatePushTarget" = Field(
        alias="updatePushTarget"
    )


UpdatePushTargetUpdatePushTarget = PushTargetFields
UpdatePushTarget.model_rebuild()

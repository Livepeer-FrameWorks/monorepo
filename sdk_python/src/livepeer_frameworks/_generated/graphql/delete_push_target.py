from pydantic import Field

from .base_model import BaseModel
from .fragments import DeleteSuccess


class DeletePushTarget(BaseModel):
    delete_push_target: "DeletePushTargetDeletePushTarget" = Field(
        alias="deletePushTarget"
    )


DeletePushTargetDeletePushTarget = DeleteSuccess
DeletePushTarget.model_rebuild()

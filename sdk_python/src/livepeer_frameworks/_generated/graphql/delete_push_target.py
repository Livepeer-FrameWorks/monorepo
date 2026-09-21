from pydantic import Field

from .base_model import BaseModel
from .fragments import DeleteSuccessFields


class DeletePushTarget(BaseModel):
    delete_push_target: "DeletePushTargetDeletePushTarget" = Field(
        alias="deletePushTarget"
    )


DeletePushTargetDeletePushTarget = DeleteSuccessFields
DeletePushTarget.model_rebuild()

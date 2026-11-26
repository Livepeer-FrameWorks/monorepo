from pydantic import Field

from .base_model import BaseModel
from .fragments import EnablePrepaidUsagePayloadDefault


class EnablePrepaidUsage(BaseModel):
    """Root Mutation type - the entry point for all write operations.

    All mutations return union types with explicit error states per GraphQL best practices.
    Check the result type to handle success/error cases appropriately."""

    enable_prepaid_usage: "EnablePrepaidUsageEnablePrepaidUsage" = Field(
        alias="enablePrepaidUsage",
        description="Enable PAYG pricing from the prepaid balance, including after an explicit choice of Free. Existing paid monthly subscriptions are preserved.",
    )
    "Enable PAYG pricing from the prepaid balance, including after an explicit choice of Free. Existing paid monthly subscriptions are preserved."


class EnablePrepaidUsageEnablePrepaidUsage(EnablePrepaidUsagePayloadDefault):
    """The funded-usage arrangement after opting into PAYG."""

    pass


EnablePrepaidUsage.model_rebuild()

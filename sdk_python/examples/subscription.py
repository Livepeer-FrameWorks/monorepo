"""Follow the live and idle events of every stream."""

import asyncio
import os

from livepeer_frameworks import AsyncFrameWorksClient, AuthenticationError


async def main() -> None:
    async with AsyncFrameWorksClient(
        "https://bridge.frameworks.network/graphql", token=os.environ["FRAMEWORKS_API_TOKEN"]
    ) as fw:
        try:
            async for message in fw.tenant_events(types=["stream.live", "stream.idle"]):
                event = message.tenant_events
                print(event.time, event.type_, event.subject)
        except AuthenticationError:
            print("the token was rejected; the subscription does not retry with it")


if __name__ == "__main__":
    asyncio.run(main())

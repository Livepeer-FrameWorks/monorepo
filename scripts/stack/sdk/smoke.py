"""SDK smoke for livepeer_frameworks: list, tenantEvents over WebSocket with a
bearer token and no Origin, createStream observed on the subscription,
deleteStream. Prints PASS/FAIL lines; exits 1 on any failure.

    PYTHONPATH=<repo>/sdk_python/src python3 smoke.py <graphql url> <ws url> <token>
"""

import asyncio
import sys
import time

from livepeer_frameworks import AsyncFrameWorksClient
from livepeer_frameworks._generated.graphql.input_types import CreateStreamInput

URL, WS_URL, TOKEN = sys.argv[1:4]
failed = 0


def report(ok, msg):
    global failed
    if not ok:
        failed += 1
    print(f"{'PASS' if ok else 'FAIL'}  sdk-py: {msg}")


async def main():
    fw = AsyncFrameWorksClient(url=URL, ws_url=WS_URL, token=TOKEN)
    try:
        listed = await fw.list_streams()
        report(listed is not None, "list_streams")
    except Exception as exc:  # the smoke reports, it does not crash
        report(False, f"list_streams: {exc}")

    name = f"stack-sdk-py-{int(time.time())}"
    seen = asyncio.Event()

    async def watch():
        try:
            async for ev in fw.tenant_events(types=["stream.created"]):
                if name in repr(ev):
                    seen.set()
                    return
        except Exception as exc:
            print(f"  subscription ended: {exc}")

    task = asyncio.create_task(watch())
    await asyncio.sleep(1.5)
    created_id = ""
    try:
        created = await fw.create_stream(input=CreateStreamInput(name=name))
        created_id = getattr(created.create_stream, "id", "") or ""
        report(bool(created_id), "create_stream")
    except Exception as exc:
        report(False, f"create_stream: {exc}")
    try:
        await asyncio.wait_for(seen.wait(), 20)
        report(True, "tenant_events delivered stream.created (bearer, no Origin)")
    except asyncio.TimeoutError:
        report(False, "tenant_events did not deliver stream.created within 20 s")
    task.cancel()
    if created_id:
        try:
            deleted = await fw.delete_stream(id=created_id)
            report(getattr(deleted.delete_stream, "typename__", "") == "DeleteSuccess", "delete_stream")
        except Exception as exc:
            report(False, f"delete_stream: {exc}")


asyncio.run(main())
sys.exit(1 if failed else 0)

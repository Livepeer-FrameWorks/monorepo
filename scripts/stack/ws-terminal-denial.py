"""Verify terminal denials on both supported protocols and each Bridge replica."""

import asyncio
import json
import sys

import websockets

import replica_pin


async def operation_frame(ws, operation, timeout=1):
    async with asyncio.timeout(timeout):
        while True:
            frame = json.loads(await ws.recv())
            if frame.get("type") == "ping":
                await ws.send(json.dumps({"type": "pong"}))
            elif frame.get("type") != "ka":
                if frame.get("id") != operation:
                    raise AssertionError(f"unexpected operation frame: {frame}")
                return frame


async def check(url, protocol):
    uri, pin = replica_pin.ws_args(url)
    async with websockets.connect(uri, subprotocols=[protocol], open_timeout=10, **pin) as ws:
        await ws.send(json.dumps({"type": "connection_init"}))
        async with asyncio.timeout(10):
            while True:
                frame = json.loads(await ws.recv())
                if frame.get("type") == "connection_ack":
                    break
                if frame.get("type") != "ka":
                    raise AssertionError(f"connection did not acknowledge: {frame}")
        start = "start" if protocol == "graphql-ws" else "subscribe"
        data = "data" if protocol == "graphql-ws" else "next"
        await ws.send(json.dumps({"id": "denied", "type": start, "payload": {
            "query": "subscription { tenantEvents { id type } }",
        }}))
        denial = await operation_frame(ws, "denied")
        assert denial.get("type") == data, denial
        errors = denial.get("payload", {}).get("errors", [])
        assert len(errors) == 1 and errors[0].get("extensions", {}).get("code") == "UNAUTHORIZED", denial
        complete = await operation_frame(ws, "denied")
        assert complete.get("type") == "complete", complete

        await ws.send(json.dumps({"id": "public", "type": start, "payload": {
            "query": "{ serverInfo { version } }",
        }}))
        public = await operation_frame(ws, "public")
        assert public.get("type") == data and public.get("payload", {}).get("data", {}).get("serverInfo"), public
        assert (await operation_frame(ws, "public")).get("type") == "complete"
        try:
            extra = await operation_frame(ws, "denied", timeout=5)
        except TimeoutError:
            return f"PASS {url} {protocol}: one denial, complete, public query, 5s quiet"
        raise AssertionError(f"response continued after completion: {extra}")


async def main():
    results = await asyncio.gather(*(check(url, protocol) for url in sys.argv[1:]
                                     for protocol in ("graphql-ws", "graphql-transport-ws")))
    print("\n".join(results))


asyncio.run(main())

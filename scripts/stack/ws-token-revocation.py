"""Hold a tenantEvents WebSocket on each Bridge replica with one API token,
revoke the token through the first replica, create a stream three seconds
later, and report how each socket ended.

    python3 ws-token-revocation.py <graphql url> <session jwt> <token id> <token value> <ws url>...

Prints one JSON object: {"stream_id", "sockets": [{"url", "code",
"closed_after_s", "events_after_revoke": [<event type>...]}]}. code is the
close code, or null when the socket was still open WAIT_S seconds after the
revocation; closed_after_s counts from the revocation's answer.
"""

import asyncio
import json
import sys
import time
import urllib.request

import websockets

WAIT_S = 10
CREATE_AFTER_S = 3
SUBSCRIBE = "subscription { tenantEvents { id type } }"


def gql(url, jwt, query, variables):
    body = json.dumps({"query": query, "variables": variables}).encode()
    req = urllib.request.Request(url, data=body, headers={
        "Authorization": "Bearer " + jwt, "Content-Type": "application/json",
    })
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.load(resp)


async def open_socket(url, token):
    ws = await websockets.connect(url, subprotocols=["graphql-transport-ws"], open_timeout=10)
    await ws.send(json.dumps({"type": "connection_init", "payload": {"Authorization": "Bearer " + token}}))
    msg = json.loads(await asyncio.wait_for(ws.recv(), 10))
    if msg.get("type") != "connection_ack":
        raise RuntimeError(f"{url}: connection_init answered {msg}")
    await ws.send(json.dumps({"id": "1", "type": "subscribe", "payload": {"query": SUBSCRIBE}}))
    return ws


async def watch(ws, result, revoked):
    try:
        while True:
            msg = json.loads(await ws.recv())
            kind = msg.get("type")
            if kind == "ping":
                await ws.send(json.dumps({"type": "pong"}))
                continue
            # "complete" ends the subscription without delivering anything.
            if kind == "complete":
                continue
            if kind == "next":
                event = ((msg.get("payload") or {}).get("data") or {}).get("tenantEvents") or {}
                kind = event.get("type")
            if revoked.is_set():
                result["events_after_revoke"].append(kind)
    except websockets.ConnectionClosed as exc:
        rcvd = getattr(exc, "rcvd", None)
        result["code"] = rcvd.code if rcvd is not None else None
        result["closed_at"] = time.time()


async def main():
    graphql_url, session_jwt, token_id, token_value, *ws_urls = sys.argv[1:]
    sockets = [await open_socket(url, token_value) for url in ws_urls]
    results = [{"url": url, "code": None, "closed_at": None, "events_after_revoke": []} for url in ws_urls]
    revoked = asyncio.Event()
    watchers = [asyncio.create_task(watch(ws, r, revoked)) for ws, r in zip(sockets, results)]
    # Each replica opens its upstream Signalman streams once a subscription starts.
    await asyncio.sleep(2)
    for r in results:
        if r["closed_at"] is not None:
            raise RuntimeError(f"{r['url']} closed with {r['code']} before the revocation")

    revoked.set()
    answer = await asyncio.to_thread(gql, graphql_url, session_jwt,
                                     "mutation($id:ID!){revokeDeveloperToken(id:$id){__typename}}", {"id": token_id})
    revoked_at = time.time()
    if ((answer.get("data") or {}).get("revokeDeveloperToken") or {}).get("__typename") != "DeleteSuccess":
        raise RuntimeError(f"revokeDeveloperToken answered {answer}")

    # A socket still open after the bound would deliver this stream's events.
    await asyncio.sleep(CREATE_AFTER_S)
    created = await asyncio.to_thread(
        gql, graphql_url, session_jwt,
        "mutation($i:CreateStreamInput!){createStream(input:$i){__typename ... on Stream{id}}}",
        {"i": {"name": f"stack-ws-revoke-{int(time.time())}", "record": False}})
    stream_id = ((created.get("data") or {}).get("createStream") or {}).get("id")

    await asyncio.wait(watchers, timeout=max(0, revoked_at + WAIT_S - time.time()))
    for task, ws in zip(watchers, sockets):
        if not task.done():
            task.cancel()
            await ws.close()
    print(json.dumps({"stream_id": stream_id, "sockets": [{
        "url": r["url"], "code": r["code"], "events_after_revoke": r["events_after_revoke"],
        "closed_after_s": None if r["closed_at"] is None else round(r["closed_at"] - revoked_at, 3),
    } for r in results]}))


asyncio.run(main())

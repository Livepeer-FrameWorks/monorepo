"""Sign the demo user in, hold a tenantEvents WebSocket on each Bridge
replica with that session's access token, sign out through the first
replica, and report how each socket ended and whether the signed-out token
and a session started after the sign-out can still connect.

    python3 ws-session-signout.py <bridge url> <login body json> <ws url>...

Prints one JSON object: {"stream_id", "sockets": [{"url", "code",
"closed_after_s", "events_after_signout": [<event type>...]}],
"reconnect_signed_out": {"first", "code"}, "reconnect_new_session":
{"first", "code"}}. code is the close code, or null when the socket was still
open WAIT_S seconds after the sign-out; closed_after_s counts from the
sign-out's answer. A stream is created CREATE_AFTER_S seconds after the
sign-out with a new session, so a socket still open would deliver its events.
"""

import asyncio
import http.cookiejar
import json
import sys
import time
import urllib.request

import websockets

WAIT_S = 10
CREATE_AFTER_S = 3
SUBSCRIBE = "subscription { tenantEvents { id type } }"


def login(bridge_url, body):
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    req = urllib.request.Request(bridge_url + "/auth/login", data=body.encode(), headers={"Content-Type": "application/json"})
    with opener.open(req, timeout=30) as resp:
        resp.read()
    for cookie in jar:
        if cookie.name == "access_token":
            return cookie.value
    raise RuntimeError("login set no access_token cookie")


def post(url, jwt, body=None):
    data = json.dumps(body).encode() if body is not None else b""
    req = urllib.request.Request(url, data=data, method="POST", headers={
        "Authorization": "Bearer " + jwt, "Content-Type": "application/json",
    })
    with urllib.request.urlopen(req, timeout=30) as resp:
        return resp.status, json.load(resp)


async def open_socket(url, token):
    ws = await websockets.connect(url, subprotocols=["graphql-transport-ws"], open_timeout=10)
    await ws.send(json.dumps({"type": "connection_init", "payload": {"Authorization": "Bearer " + token}}))
    msg = json.loads(await asyncio.wait_for(ws.recv(), 10))
    if msg.get("type") != "connection_ack":
        raise RuntimeError(f"{url}: connection_init answered {msg}")
    await ws.send(json.dumps({"id": "1", "type": "subscribe", "payload": {"query": SUBSCRIBE}}))
    return ws


async def init_outcome(url, token):
    """How connection_init with token is answered: the first message type,
    or "close" with its code."""
    ws = await websockets.connect(url, subprotocols=["graphql-transport-ws"], open_timeout=10)
    try:
        await ws.send(json.dumps({"type": "connection_init", "payload": {"Authorization": "Bearer " + token}}))
        msg = json.loads(await asyncio.wait_for(ws.recv(), 10))
        return {"first": msg.get("type"), "code": None}
    except websockets.ConnectionClosed as exc:
        rcvd = getattr(exc, "rcvd", None)
        return {"first": "close", "code": rcvd.code if rcvd is not None else None}
    finally:
        await ws.close()


async def watch(ws, result, signed_out):
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
            if signed_out.is_set():
                result["events_after_signout"].append(kind)
    except websockets.ConnectionClosed as exc:
        rcvd = getattr(exc, "rcvd", None)
        result["code"] = rcvd.code if rcvd is not None else None
        result["closed_at"] = time.time()


async def main():
    bridge_url, login_body, *ws_urls = sys.argv[1:]
    graphql_url = bridge_url + "/graphql"
    session = await asyncio.to_thread(login, bridge_url, login_body)
    sockets = [await open_socket(url, session) for url in ws_urls]
    results = [{"url": url, "code": None, "closed_at": None, "events_after_signout": []} for url in ws_urls]
    signed_out = asyncio.Event()
    watchers = [asyncio.create_task(watch(ws, r, signed_out)) for ws, r in zip(sockets, results)]
    # Each replica opens its upstream Signalman streams once the sockets register.
    await asyncio.sleep(2)
    for r in results:
        if r["closed_at"] is not None:
            raise RuntimeError(f"{r['url']} closed with {r['code']} before the sign-out")

    signed_out.set()
    status, answer = await asyncio.to_thread(post, bridge_url + "/auth/logout", session)
    signed_out_at = time.time()
    if status != 200 or not answer.get("success"):
        raise RuntimeError(f"logout answered {status} {answer}")

    # Session tokens carry their issue time in whole seconds, so the new
    # session starts in a later second than the sign-out.
    await asyncio.sleep(CREATE_AFTER_S)
    fresh = await asyncio.to_thread(login, bridge_url, login_body)
    _, created = await asyncio.to_thread(
        post, graphql_url, fresh,
        {"query": "mutation($i:CreateStreamInput!){createStream(input:$i){__typename ... on Stream{id}}}",
         "variables": {"i": {"name": f"stack-ws-signout-{int(time.time())}", "record": False}}})
    stream_id = ((created.get("data") or {}).get("createStream") or {}).get("id")

    await asyncio.wait(watchers, timeout=max(0, signed_out_at + WAIT_S - time.time()))
    for task, ws in zip(watchers, sockets):
        if not task.done():
            task.cancel()
            await ws.close()

    reconnect_signed_out = await init_outcome(ws_urls[-1], session)
    reconnect_new_session = await init_outcome(ws_urls[-1], fresh)
    print(json.dumps({
        "stream_id": stream_id,
        "sockets": [{
            "url": r["url"], "code": r["code"], "events_after_signout": r["events_after_signout"],
            "closed_after_s": None if r["closed_at"] is None else round(r["closed_at"] - signed_out_at, 3),
        } for r in results],
        "reconnect_signed_out": reconnect_signed_out,
        "reconnect_new_session": reconnect_new_session,
    }))


asyncio.run(main())

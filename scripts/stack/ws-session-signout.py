"""Sign the demo user in, hold a tenantEvents WebSocket on each Bridge
replica with that session's access token, sign out through the first
replica, and report how each socket ended and whether the signed-out token
and a session started after the sign-out can still connect.

    python3 ws-session-signout.py <bridge url> <login body json | @command> <ws url>...

Prints one JSON object: {"stream_id", "sockets": [{"url", "code",
"closed_after_s", "events_after_signout": [<event type>...]}],
"reconnect_signed_out": {"first", "code"}, "reconnect_new_session":
{"first", "code"}, "http_signed_out", "http_new_session"}, the last two the
HTTP status of a GraphQL POST to the last replica with each session's
token. code is the close code, or null when the socket was still
open WAIT_S seconds after the sign-out; closed_after_s counts from the
sign-out's answer. A stream is created CREATE_AFTER_S seconds after the
sign-out with a new session, so a socket still open would deliver its events.
"""

import asyncio
import http.cookiejar
import json
import subprocess
import sys
import time
import urllib.error
import urllib.request

import websockets

import replica_pin

WAIT_S = 10
CREATE_AFTER_S = 3
SUBSCRIBE = "subscription { tenantEvents { id type } }"


def login(bridge_url, body):
    """Sign in and return the session's access token. body is the password
    login's JSON, or "@<command>" for a tenant without a password: the command
    prints {"path", "body"} for one sign-in (wallet-login.sh body), run anew per
    sign-in because each needs a fresh challenge."""
    path = "/auth/login"
    if body.startswith("@"):
        signed = json.loads(subprocess.run(body[1:], shell=True, check=True, capture_output=True, text=True).stdout)
        path, body = signed["path"], json.dumps(signed["body"])
    jar = http.cookiejar.CookieJar()
    url = replica_pin.join(bridge_url, path)
    opener = replica_pin.build_opener(url, urllib.request.HTTPCookieProcessor(jar))
    req = urllib.request.Request(replica_pin.split(url)[0], data=body.encode(), headers={"Content-Type": "application/json"})
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
    with replica_pin.urlopen(req, timeout=30) as resp:
        return resp.status, json.load(resp)


def http_status(ws_url, jwt):
    """Status of a GraphQL POST with jwt to the Bridge replica serving ws_url."""
    uri, pin = replica_pin.split(ws_url)
    base = uri.replace("ws://", "http://", 1).replace("wss://", "https://", 1).rsplit("/graphql", 1)[0]
    url = base + "/graphql" + (f"#{pin}" if pin else "")
    req = urllib.request.Request(url, data=json.dumps({"query": "{streamsConnection(page:{first:1}){totalCount}}"}).encode(),
                                 method="POST", headers={"Authorization": "Bearer " + jwt, "Content-Type": "application/json"})
    try:
        with replica_pin.urlopen(req, timeout=30) as resp:
            return resp.status
    except urllib.error.HTTPError as exc:
        return exc.code


async def open_socket(url, token):
    uri, pin = replica_pin.ws_args(url)
    ws = await websockets.connect(uri, subprotocols=["graphql-transport-ws"], open_timeout=10, **pin)
    await ws.send(json.dumps({"type": "connection_init", "payload": {"Authorization": "Bearer " + token}}))
    msg = json.loads(await asyncio.wait_for(ws.recv(), 10))
    if msg.get("type") != "connection_ack":
        raise RuntimeError(f"{url}: connection_init answered {msg}")
    await ws.send(json.dumps({"id": "1", "type": "subscribe", "payload": {"query": SUBSCRIBE}}))
    return ws


async def init_outcome(url, token):
    """How connection_init with token is answered: the first message type,
    or "close" with its code."""
    uri, pin = replica_pin.ws_args(url)
    ws = await websockets.connect(uri, subprotocols=["graphql-transport-ws"], open_timeout=10, **pin)
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
    graphql_url = replica_pin.join(bridge_url, "/graphql")
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
    status, answer = await asyncio.to_thread(post, replica_pin.join(bridge_url, "/auth/logout"), session)
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
    # The last replica did not handle the sign-out; it learned of it from
    # the session revocation stream.
    http_signed_out = await asyncio.to_thread(http_status, ws_urls[-1], session)
    http_new_session = await asyncio.to_thread(http_status, ws_urls[-1], fresh)
    print(json.dumps({
        "stream_id": stream_id,
        "sockets": [{
            "url": r["url"], "code": r["code"], "events_after_signout": r["events_after_signout"],
            "closed_after_s": None if r["closed_at"] is None else round(r["closed_at"] - signed_out_at, 3),
        } for r in results],
        "reconnect_signed_out": reconnect_signed_out,
        "reconnect_new_session": reconnect_new_session,
        "http_signed_out": http_signed_out,
        "http_new_session": http_new_session,
    }))


asyncio.run(main())

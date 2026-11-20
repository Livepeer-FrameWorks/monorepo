#!/usr/bin/env python3
"""Read Mist's audience census inside one disposable stack edge."""

import argparse
import hashlib
import json
import re
import subprocess
import sys


def census(response):
    clients = response.get("clients")
    if not isinstance(clients, dict) or not isinstance(clients.get("fields"), list):
        raise ValueError("Mist clients field list is unavailable")
    if not all(isinstance(name, str) for name in clients["fields"]):
        raise ValueError("Mist clients field names are invalid")
    columns = {name: index for index, name in enumerate(clients["fields"])}
    if not {"stream", "sessid", "tags"}.issubset(columns):
        raise ValueError("Mist clients census lacks stream, sessid or tags")
    rows = clients.get("data")
    if rows is None:
        rows = []
    if not isinstance(rows, list):
        raise ValueError("Mist clients census rows are invalid")
    streams = {}
    sessions = set()
    for row in rows:
        if not isinstance(row, list) or any(columns[key] >= len(row) for key in ("stream", "sessid", "tags")):
            raise ValueError("Mist clients census row is truncated")
        stream, session, tags = (row[columns[key]] for key in ("stream", "sessid", "tags"))
        if not isinstance(stream, str) or not isinstance(session, str) or not session:
            raise ValueError("Mist clients census identity is invalid")
        if tags is not None and not isinstance(tags, list):
            raise ValueError("Mist clients census tags are invalid")
        if session[0] in "IOU" or "fwproc" in (tags or []) or stream.startswith("processing+"):
            continue
        if session in sessions:
            continue
        sessions.add(session)
        bound = re.fullmatch(r"live\+([A-Za-z0-9_-]+)_fw1[pr]_[0-9a-f]{32}", stream)
        if bound:
            stream = bound.group(1)
        else:
            for prefix in ("live+", "pull+", "vod+", "dvr+", "clip+"):
                if stream.startswith(prefix):
                    stream = stream[len(prefix):]
                    break
        streams[stream] = streams.get(stream, 0) + 1
    return {"audience": sum(streams.values()), "streams": streams}


def read_edge(container):
    inspected = subprocess.run(["docker", "inspect", container], check=True, capture_output=True, text=True, timeout=10)
    environment = dict(value.split("=", 1) for value in json.loads(inspected.stdout)[0]["Config"]["Env"] if "=" in value)
    username, password = environment.get("MIST_API_USERNAME"), environment.get("MIST_API_PASSWORD")
    if not username or not password:
        raise ValueError("Mist API credentials are unavailable in the owned edge")

    def request(command):
        try:
            result = subprocess.run(["docker", "exec", container, "curl", "-fsS", "-m", "5", "--get", "--data-urlencode", "command=" + json.dumps(command), "http://localhost:4242/api2"], check=True, capture_output=True, text=True, timeout=10)
        except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
            raise ValueError("Mist API request failed") from None
        return json.loads(result.stdout)

    authorization = request({"authorize": {"username": username, "password": ""}}).get("authorize", {})
    if authorization.get("status") == "CHALL":
        digest = hashlib.md5(password.encode()).hexdigest()
        answer = hashlib.md5((digest + authorization["challenge"]).encode()).hexdigest()
        authorization = request({"authorize": {"username": username, "password": answer}}).get("authorize", {})
    if authorization.get("status") != "OK":
        raise ValueError("Mist API authentication failed")
    return census(request({"clients": {"time": -5, "fields": ["stream", "sessid", "tags"]}}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("container")
    args = parser.parse_args()
    try:
        print(json.dumps(read_edge(args.container)))
    except (ValueError, KeyError, IndexError, subprocess.SubprocessError):
        print("Mist audience census is unavailable", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())

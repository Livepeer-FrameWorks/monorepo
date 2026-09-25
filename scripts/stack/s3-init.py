#!/usr/bin/env python3
"""Create the stack's per-cell buckets on the in-network S3 service.

Runs in stack-runner: python3 /repo/scripts/stack/s3-init.py <bucket>...
Waits for s3:8080 (Ceph's demo image takes a minute to boot), then creates
each bucket with a SigV4-signed PUT. An existing bucket owned by the demo
user counts as created, so re-upping a slot is idempotent. Standard library
only: the runner has no AWS tooling.
"""
import datetime
import hashlib
import hmac
import os
import sys
import time
import urllib.error
import urllib.request

ENDPOINT = os.environ.get("STACK_S3_ENDPOINT", "http://s3:8080")
HOST = ENDPOINT.split("://", 1)[1]
REGION = "us-east-1"
ACCESS_KEY = "frameworks"
SECRET_KEY = "frameworks-secret"
EMPTY_SHA256 = hashlib.sha256(b"").hexdigest()


def _sign(key, msg):
    return hmac.new(key, msg.encode(), hashlib.sha256).digest()


def put_bucket(bucket):
    now = datetime.datetime.now(datetime.timezone.utc)
    amz_date = now.strftime("%Y%m%dT%H%M%SZ")
    date = now.strftime("%Y%m%d")
    path = "/" + bucket
    headers = {"host": HOST, "x-amz-content-sha256": EMPTY_SHA256, "x-amz-date": amz_date}
    signed = ";".join(sorted(headers))
    canonical = "\n".join(
        ["PUT", path, "", "".join(f"{k}:{headers[k]}\n" for k in sorted(headers)), signed, EMPTY_SHA256]
    )
    scope = f"{date}/{REGION}/s3/aws4_request"
    to_sign = "\n".join(["AWS4-HMAC-SHA256", amz_date, scope, hashlib.sha256(canonical.encode()).hexdigest()])
    key = _sign(_sign(_sign(_sign(("AWS4" + SECRET_KEY).encode(), date), REGION), "s3"), "aws4_request")
    signature = hmac.new(key, to_sign.encode(), hashlib.sha256).hexdigest()
    request = urllib.request.Request(ENDPOINT + path, method="PUT", data=b"")
    request.add_header("x-amz-content-sha256", EMPTY_SHA256)
    request.add_header("x-amz-date", amz_date)
    request.add_header(
        "Authorization",
        f"AWS4-HMAC-SHA256 Credential={ACCESS_KEY}/{scope}, SignedHeaders={signed}, Signature={signature}",
    )
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            return response.status, ""
    except urllib.error.HTTPError as err:
        return err.code, err.read().decode(errors="replace")
    except OSError as err:  # connection refused/reset while the service restarts
        return 0, str(err)


def wait_reachable(deadline):
    while True:
        try:
            urllib.request.urlopen(ENDPOINT + "/", timeout=5)
            return
        except urllib.error.HTTPError:
            return  # answering at all (even 403 for anonymous) means the S3 frontend is up
        except OSError as err:
            if time.monotonic() >= deadline:
                sys.exit(f"s3-init: {ENDPOINT} not reachable: {err}")
            time.sleep(3)


def main(buckets):
    if not buckets:
        sys.exit("usage: s3-init.py <bucket>...")
    deadline = time.monotonic() + 300
    wait_reachable(deadline)
    for bucket in buckets:
        while True:
            status, body = put_bucket(bucket)
            if status == 200 or "BucketAlreadyOwnedByYou" in body:
                print(f"s3-init: bucket {bucket} ready")
                break
            # RGW answers before its user and pools finish initialising.
            if time.monotonic() >= deadline:
                sys.exit(f"s3-init: creating {bucket} failed: HTTP {status} {body[:300]}")
            time.sleep(3)


if __name__ == "__main__":
    main(sys.argv[1:])

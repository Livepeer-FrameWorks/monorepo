#!/usr/bin/env python3
"""Exit 0 only when a stack S3 bucket has no object under the given prefix."""

import datetime
import hashlib
import hmac
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET


def sign(key, message):
    return hmac.new(key, message.encode(), hashlib.sha256).digest()


def list_prefix(bucket, prefix):
    endpoint = os.environ.get("STACK_S3_ENDPOINT", "http://s3:8080")
    host = urllib.parse.urlsplit(endpoint).netloc
    path = "/" + urllib.parse.quote(bucket, safe="")
    query = "list-type=2&prefix=" + urllib.parse.quote(prefix, safe="")
    now = datetime.datetime.now(datetime.timezone.utc)
    date = now.strftime("%Y%m%d")
    stamp = now.strftime("%Y%m%dT%H%M%SZ")
    empty_hash = hashlib.sha256(b"").hexdigest()
    headers = {"host": host, "x-amz-content-sha256": empty_hash, "x-amz-date": stamp}
    signed_headers = ";".join(sorted(headers))
    canonical = "\n".join(
        ["GET", path, query, "".join(f"{k}:{headers[k]}\n" for k in sorted(headers)), signed_headers, empty_hash]
    )
    scope = f"{date}/us-east-1/s3/aws4_request"
    to_sign = "\n".join(["AWS4-HMAC-SHA256", stamp, scope, hashlib.sha256(canonical.encode()).hexdigest()])
    secret = os.environ.get("STACK_S3_SECRET_KEY", "frameworks-secret")
    access = os.environ.get("STACK_S3_ACCESS_KEY", "frameworks")
    key = sign(sign(sign(sign(("AWS4" + secret).encode(), date), "us-east-1"), "s3"), "aws4_request")
    signature = hmac.new(key, to_sign.encode(), hashlib.sha256).hexdigest()
    request = urllib.request.Request(endpoint + path + "?" + query)
    request.add_header("x-amz-content-sha256", empty_hash)
    request.add_header("x-amz-date", stamp)
    request.add_header(
        "Authorization",
        f"AWS4-HMAC-SHA256 Credential={access}/{scope}, SignedHeaders={signed_headers}, Signature={signature}",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        root = ET.fromstring(response.read())
    keys = [node.text for node in root.findall(".//{*}Contents/{*}Key") if node.text]
    truncated = root.findtext(".//{*}IsTruncated")
    return keys, truncated == "true"


def main():
    if len(sys.argv) != 3:
        sys.exit("usage: s3-prefix-empty.py <bucket> <prefix>")
    try:
        keys, truncated = list_prefix(sys.argv[1], sys.argv[2])
    except (OSError, ET.ParseError, urllib.error.HTTPError) as error:
        print(f"S3 prefix check failed: {error}", file=sys.stderr)
        return 2
    if keys or truncated:
        print(f"S3 prefix still has {len(keys)} object(s): {keys[:3]}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())

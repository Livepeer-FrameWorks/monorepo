#!/usr/bin/env python3
"""Decode every HLS rendition and check its actual frames and duration."""
import math
import re
import subprocess
import sys
import urllib.parse
import urllib.request


def decode(url, seconds, fps):
    variants = []
    if urllib.parse.urlsplit(url).scheme in ("http", "https"):
        with urllib.request.urlopen(url, timeout=15) as response:
            head = response.read(512)
            if head.startswith(b"#EXTM3U"):
                body = (head + response.read()).decode()
                pending = False
                for line in body.splitlines():
                    if line.startswith("#EXT-X-STREAM-INF:"):
                        pending = True
                    elif pending and line.strip() and not line.startswith("#"):
                        variants.append(urllib.parse.urljoin(url, line.strip()))
                        pending = False
    for index, variant in enumerate(variants or [url]):
        result = subprocess.run([
            "ffmpeg", "-hide_banner", "-v", "error", "-xerror", "-nostdin",
            "-rw_timeout", "15000000", "-i", variant,
            "-map", "0:v:0", "-map", "0:a?", "-fps_mode", "passthrough",
            "-progress", "pipe:1", "-nostats", "-f", "null", "-",
        ], capture_output=True, text=True, timeout=min(180, max(30, seconds * 2)))
        if result.returncode:
            detail = re.sub(r"https?://\S+", "[media URL]", result.stderr)
            raise AssertionError(f"variant {index} decode failed: {detail[-1000:]}")
        progress = dict(line.split("=", 1) for line in result.stdout.splitlines() if "=" in line)
        frames = int(progress.get("frame", 0))
        elapsed = int(progress.get("out_time_us", 0)) / 1_000_000
        assert abs(frames - seconds * fps) <= max(2, math.ceil(fps * 0.1)), (index, frames, seconds, fps)
        assert abs(elapsed - seconds) <= 0.3, (index, elapsed, seconds)
        print(f"decoded variant {index}: {frames} frames, {elapsed:.3f}s")


if __name__ == "__main__":
    decode(sys.argv[1], float(sys.argv[2]), float(sys.argv[3]))

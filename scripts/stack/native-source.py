#!/usr/bin/env python3
"""Serve continuous MPEG-TS for stack-only Mist-native executable inputs."""

import http.server
import signal
import subprocess
import sys

processes = set()


def stop_process(process):
    process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait()


class Source(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/loop.ts":
            self.send_error(404)
            return
        process = subprocess.Popen(
            [
                "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error",
                "-re", "-stream_loop", "-1", "-i", sys.argv[1],
                "-map", "0:v:0", "-an", "-c:v", "copy", "-f", "mpegts", "-",
            ],
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
        )
        processes.add(process)
        try:
            self.send_response(200)
            self.send_header("Content-Type", "video/mp2t")
            self.end_headers()
            while chunk := process.stdout.read(4096):
                self.wfile.write(chunk)
        except (BrokenPipeError, ConnectionResetError):
            pass
        finally:
            try:
                stop_process(process)
            finally:
                processes.discard(process)


signal.signal(signal.SIGTERM, lambda *_args: sys.exit(0))
try:
    http.server.ThreadingHTTPServer(("0.0.0.0", 18948), Source).serve_forever()
finally:
    for child in tuple(processes):
        stop_process(child)

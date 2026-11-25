#!/usr/bin/env python3
"""Check media scenario verdicts against source timestamps and session state."""

import http.server
import json
import os
from pathlib import Path
import re
import shlex
import sqlite3
import subprocess
import tempfile
import threading
import unittest

ROOT = Path(__file__).resolve().parent


def scenario_function(scenario, name):
    source = (ROOT / "scenarios" / scenario).read_text()
    match = re.search(r"^" + re.escape(name) + r"\(\) \{\n.*?^\}", source, re.M | re.S)
    assert match, name
    return match.group(0)


class QualificationGuards(unittest.TestCase):
    def test_pull_end_distinguishes_playable_sessions_from_pending_dials(self):
        predicate = scenario_function("58-pull-stream-lifecycle.sh", "session_ended")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            database = root / "sessions.db"
            query = root / "query.py"
            query.write_text(
                "import sqlite3,sys\n"
                "c=sqlite3.connect(':memory:')\n"
                "c.execute('ATTACH DATABASE ? AS foghorn',(sys.argv[2],))\n"
                "print(c.execute(sys.argv[1]).fetchone()[0])\n"
            )
            connection = sqlite3.connect(database)
            self.addCleanup(connection.close)
            connection.execute(
                "CREATE TABLE pull_ingest_sessions(tenant_id TEXT,stream_id TEXT,"
                "admission_state TEXT,ended_at TEXT)"
            )
            for state, ended, expected in (("pending", None, True), ("active", None, False), ("active", "ended", True)):
                with self.subTest(state=state, ended=ended):
                    connection.execute("DELETE FROM pull_ingest_sessions")
                    connection.execute("INSERT INTO pull_ingest_sessions VALUES ('tenant','stream',?,?)", (state, ended))
                    connection.commit()
                    script = "STACK_TENANT_ID=tenant UUID=stream FOGHORN_B_DB=foghorn_b\n"
                    script += "pg() { python3 " + shlex.quote(str(query)) + ' "$2" ' + shlex.quote(str(database)) + "; }\n"
                    result = subprocess.run(["bash", "-c", script + predicate + "\nsession_ended"], capture_output=True, text=True, timeout=10)
                    self.assertEqual(result.returncode == 0, expected, result.stdout + result.stderr)

    def test_webvtt_uses_uploaded_cue_clock_and_duration(self):
        predicate = scenario_function("78-vod-upload-formats.sh", "subtitles_play")
        body = {"text": ""}

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.send_header("Content-Type", "text/vtt")
                self.end_headers()
                self.wfile.write(body["text"].encode())

            def log_message(self, *_args):
                pass

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                probe = root / "ffprobe"
                probe.write_text("#!/usr/bin/env python3\nimport os\nprint(os.environ['SOURCE_PACKETS'])\n")
                probe.chmod(0o755)
                url = f"http://127.0.0.1:{server.server_port}/hls/fixture/index.m3u8"
                script = "location_at() { printf '%s\\n' " + shlex.quote(url) + "; }\n" + predicate
                script += "\nsubtitles_play source.mkv http://foghorn fixture\n"
                cases = (
                    ("mp4", "0.500000", "00:00:00.500 --> 00:00:02.000", True),
                    ("remuxed-mkv", "0.521000", "00:00:00.521 --> 00:00:02.021", True),
                    ("wrong-origin", "0.521000", "00:00:00.500 --> 00:00:02.000", False),
                    ("wrong-duration", "0.521000", "00:00:00.521 --> 00:00:02.121", False),
                )
                for name, start, timing, expected in cases:
                    with self.subTest(case=name):
                        body["text"] = "WEBVTT\n\n" + timing + "\nstack subtitle cue\n"
                        packets = [
                            {"pts_time": "0.000000", "duration_time": "0.500000", "size": "2"},
                            {"pts_time": start, "duration_time": "1.500000", "size": "22"},
                        ]
                        env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"], SOURCE_PACKETS=json.dumps({"packets": packets}))
                        result = subprocess.run(["bash", "-c", script], env=env, capture_output=True, text=True, timeout=10)
                        self.assertEqual(result.returncode == 0, expected, result.stdout + result.stderr)
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)


if __name__ == "__main__":
    unittest.main()

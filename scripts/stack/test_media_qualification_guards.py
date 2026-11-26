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

    def test_webvtt_keeps_cue_offset_from_first_video_frame_and_duration(self):
        predicate = scenario_function("78-vod-upload-formats.sh", "subtitles_play")
        served = {"vtt": "", "info": ""}

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                info = self.path.startswith("/json_")
                self.send_response(200)
                self.send_header("Content-Type", "application/javascript" if info else "text/vtt")
                self.end_headers()
                self.wfile.write((served["info"] if info else served["vtt"]).encode())

            def log_message(self, *_args):
                pass

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                probe = root / "ffprobe"
                probe.write_text("#!/usr/bin/env python3\nimport os,sys\n"
                                 "kind=sys.argv[sys.argv.index('-select_streams')+1]\n"
                                 "print(os.environ['SOURCE_' + kind.upper()])\n")
                probe.chmod(0o755)
                url = f"http://127.0.0.1:{server.server_port}/hls/fixture/index.m3u8"
                script = "location_at() { printf '%s\\n' " + shlex.quote(url) + "; }\n" + predicate
                script += "\nsubtitles_play source.mkv http://foghorn fixture\n"
                # (case, source video start, source cue start, served video firstms, served cue timing, expected)
                cases = (
                    ("aligned", "0.000000", "0.500000", 0, "00:00:00.500 --> 00:00:02.000", True),
                    ("every-track-shifted", "0.000000", "0.500000", 6, "00:00:00.506 --> 00:00:02.006", True),
                    ("primed-source", "0.021000", "0.521000", 0, "00:00:00.500 --> 00:00:02.000", True),
                    ("subtitle-only-shifted", "0.000000", "0.500000", 0, "00:00:00.506 --> 00:00:02.006", False),
                    ("wrong-duration", "0.000000", "0.500000", 0, "00:00:00.500 --> 00:00:02.100", False),
                )
                for name, video_start, cue_start, video_first, timing, expected in cases:
                    with self.subTest(case=name):
                        served["vtt"] = "WEBVTT\n\n" + timing + "\nstack subtitle cue\n"
                        served["info"] = json.dumps({"meta": {"tracks": {
                            "video_H264_960x540_1": {"type": "video", "firstms": video_first},
                            "subtitle_subtitle_2": {"type": "meta", "firstms": video_first},
                        }}})
                        subtitles = [
                            {"pts_time": "0.000000", "duration_time": "0.500000", "size": "2"},
                            {"pts_time": cue_start, "duration_time": "1.500000", "size": "22"},
                        ]
                        video = [{"pts_time": video_start, "duration_time": "0.100000", "size": "900"}]
                        env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"],
                                   SOURCE_S=json.dumps({"packets": subtitles}), SOURCE_V=json.dumps({"packets": video}))
                        result = subprocess.run(["bash", "-c", script], env=env, capture_output=True, text=True, timeout=10)
                        self.assertEqual(result.returncode == 0, expected, result.stdout + result.stderr)
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)


class SourceExportGuards(unittest.TestCase):
    def export_commands(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source, tools, scratch = (root / name for name in ("source", "tools", "scratch"))
            for path in (source / "src/output", tools, scratch):
                path.mkdir(parents=True)
            (source / "meson.build").write_text("project('source-contract-fixture', 'cpp')\n")
            (source / "src/output/output.cpp").write_text("// fixture\n")
            for command in (("init", "-q"), ("add", "."),
                            ("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                             "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture")):
                subprocess.run(["git", "-C", str(source), *command], check=True, capture_output=True)
            revision = subprocess.check_output(["git", "-C", str(source), "rev-parse", "HEAD"], text=True).strip()
            commands = root / "docker.jsonl"
            docker = tools / "docker"
            docker.write_text(
                "#!/usr/bin/env python3\nimport json,os,sys\n"
                "with open(os.environ['EXPORT_COMMANDS'],'a') as f: f.write(json.dumps(sys.argv[1:])+'\\n')\n"
                "if sys.argv[1:3]==['image','inspect']:\n"
                " print('sha256:fixture' if sys.argv[4]=='{{.Id}}' else os.environ['EXPORT_REVISION'])\n"
            )
            make = tools / "make"
            make.write_text("#!/bin/sh\nexit 0\n")
            for executable in (docker, make):
                executable.chmod(0o755)
            env = dict(os.environ, PATH=str(tools) + os.pathsep + os.environ["PATH"],
                       TMPDIR=str(scratch), EXPORT_COMMANDS=str(commands), EXPORT_REVISION=revision,
                       MIST_SOURCE_DIR=str(source), MIST_CONTRACT_BUILD_IMAGE="fixture:local",
                       MIST_PREFETCH_SUBPROJECTS="1")
            env.pop("MIST_INSTALL_TREE_OUT", None)
            result = subprocess.run(["bash", str(ROOT.parent / "verify-mist-current-source.sh")],
                                    env=env, capture_output=True, text=True, timeout=20)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            return [json.loads(line) for line in commands.read_text().splitlines()]

    def test_source_export_prefetches_the_non_processing_profile(self):
        run = next(args for args in self.export_commands() if args[0] == "run")
        self.assertTrue({"mbedtls", "libsrtp2", "usrsctp", "libsrt", "librist"}.issubset(run),
                        "the offline profile needs every enabled protocol dependency, including librist")

    def test_source_export_prefetch_preserves_job_directory_ownership(self):
        run = next(args for args in self.export_commands() if args[0] == "run")
        self.assertIn("--user", run, "root-owned dependencies prevent job cleanup")
        self.assertEqual(run[run.index("--user") + 1], f"{os.getuid()}:{os.getgid()}")
        self.assertIn("HOME=/tmp", run)


    def test_source_export_separates_offline_build_from_native_socket_tests(self):
        commands = self.export_commands()
        build = next(args for args in commands if args[0] == "build")
        self.assertIn("--network=none", build)
        native = next(args for args in commands if args[0] == "run" and "test" in args)
        self.assertEqual(native[native.index("--network") + 1], "bridge")
        self.assertIn("--print-errorlogs", native)


if __name__ == "__main__":
    unittest.main()

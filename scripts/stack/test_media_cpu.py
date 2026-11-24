#!/usr/bin/env python3
"""Check the native media entrypoint's quota, affinity and argument contract."""
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent
FUNCTIONS = (ROOT / "media-cpu.sh").read_text().rsplit('\nmedia_cpu_main "$@"', 1)[0]


class MediaCpuBudget(unittest.TestCase):
    def test_native_workers_use_only_the_available_cpu_budget(self):
        for allowed, quota, period, wanted in (
            ("0-23", "200000", "100000", "0,1"),
            ("4-7,12,16-19", "200000", "100000", "4,5"),
            ("7", "200000", "100000", "7"),
            ("4-7", "150000", "100000", "4"),
            ("4-7", "25000", "100000", "4"),
            ("4,7-8", "max", "100000", "4,7,8"),
            ("4,7-8", "-1", "100000", "4,7,8"),
        ):
            with self.subTest(allowed=allowed, quota=quota):
                result = subprocess.run(
                    ["sh", "-c", FUNCTIONS + '\nmedia_cpu_set "$@"', "fixture", allowed, quota, period],
                    capture_output=True, text=True,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout.strip(), wanted)

    def test_unreadable_or_invalid_limits_never_launch_the_encoder(self):
        for allowed, quota, period in (("", "200000", "100000"), ("3-1", "200000", "100000"),
                                       ("0-3", "", "100000"), ("0-3", "200000", "0")):
            with self.subTest(allowed=allowed, quota=quota, period=period):
                result = subprocess.run(
                    ["sh", "-c", FUNCTIONS + '\nmedia_cpu_set "$@"', "fixture", allowed, quota, period],
                    capture_output=True, text=True,
                )
                self.assertNotEqual(result.returncode, 0)

    def test_entrypoint_replaces_itself_and_preserves_every_argument(self):
        # A real exec into the fixture command also checks that no shell remains
        # between Docker's PID 1 and the native process.
        command = 'printf "%s\\n" "$$" "$@"'
        overrides = '\nmedia_cpu_limits() { printf "200000 100000\\n"; }\n'
        overrides += 'media_cpu_allowed() { printf "4-7\\n"; }\n'
        with tempfile.TemporaryDirectory() as temporary:
            taskset = Path(temporary) / "taskset"
            taskset.write_text('#!/bin/sh\nprintf "%s\\n" "$1" "$2"\nshift 2\nexec "$@"\n')
            taskset.chmod(0o755)
            script = FUNCTIONS + overrides + '\nPATH=' + shlex.quote(temporary) + ':"$PATH"\nmedia_cpu_main "$@"'
            with subprocess.Popen(["sh", "-c", script, "fixture", "sh", "-c", command,
                                   "native", "-orchestrator", "an argument with spaces"],
                                  stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True) as process:
                stdout, stderr = process.communicate()
                self.assertEqual(process.returncode, 0, stderr)
                self.assertEqual(stdout.splitlines(), ["-c", "4,5", str(process.pid), "-orchestrator", "an argument with spaces"])


if __name__ == "__main__":
    unittest.main()

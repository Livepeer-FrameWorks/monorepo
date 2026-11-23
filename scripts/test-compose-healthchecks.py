#!/usr/bin/env python3
"""Check cancellation of the rendered Kafka health command without a broker."""
import json
import os
from pathlib import Path
import shlex
import signal
import subprocess
import sys
import tempfile
import time
import unittest


CONFIG = json.loads(Path(sys.argv.pop(1)).read_text())


class KafkaHealthcheck(unittest.TestCase):
    def test_cancelling_probe_terminates_its_client(self):
        health = CONFIG["services"]["kafka"]["healthcheck"]["test"]
        executable = "/opt/kafka/bin/kafka-topics.sh"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            client = root / "kafka-topics.sh"
            started = root / "started"
            # Kafka's launch scripts exec their Java client. Emulate its PID
            # ownership while keeping this regression independent of JVM speed.
            client.write_text("#!/bin/sh\nprintf '%s\\n' \"$$\" > "
                              + shlex.quote(str(started)) + "\nexec sleep 60\n")
            client.chmod(0o755)
            if health[0] == "CMD-SHELL":
                command = ["/bin/sh", "-c", health[1].replace(executable, shlex.quote(str(client)))]
            else:
                self.assertEqual(health[0], "CMD")
                self.assertEqual(health[1], executable)
                command = [str(client), *health[2:]]
            process = subprocess.Popen(command, start_new_session=True)
            client_pid = None
            try:
                deadline = time.monotonic() + 5
                while not started.exists() and time.monotonic() < deadline:
                    time.sleep(0.01)
                self.assertTrue(started.exists(), "health client did not start")
                client_pid = int(started.read_text())
                # Docker cancels the health exec's initial PID, not every process
                # that an extra command shell might have started beneath it.
                process.kill()
                process.wait(timeout=5)
                if client_pid != process.pid:
                    try:
                        os.kill(client_pid, 0)
                    except ProcessLookupError:
                        pass
                    else:
                        self.fail("cancelled Kafka health probe left its client running")
            finally:
                # Clean up even when exercising the broken shell wrapper.
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait(timeout=5)


if __name__ == "__main__":
    unittest.main()

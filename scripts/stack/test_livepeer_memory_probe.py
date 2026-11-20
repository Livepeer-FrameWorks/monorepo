#!/usr/bin/env python3
"""An empty encode workload must never qualify orchestrator memory safety."""
import importlib.util
import json
import os
from pathlib import Path
from tempfile import TemporaryDirectory
from types import SimpleNamespace
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("probe", Path(__file__).with_name("probe_livepeer_memory.py"))
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)

class MemoryProbeTests(unittest.TestCase):
    def test_missing_segments_fails_instead_of_qualifying_empty_workload(self):
        def fake_run(*args, **_kwargs):
            output = ""
            if args[0:2] == ("docker", "inspect"):
                output = json.dumps([{"Name": name, "State": {"Running": True, "OOMKilled": False}, "HostConfig": {"Memory": 2147483648}, "RestartCount": 0} for name in ("orch", "gateway")])
            elif "ps" in args:
                output = "orch gateway"
            elif "port" in args:
                output = "127.0.0.1:12345"
            return SimpleNamespace(stdout=output, returncode=0)
        with TemporaryDirectory() as work:
            with patch.dict(os.environ, {"GO_LIVEPEER_PROBE_IMAGE": "test-image", "LIVEPEER_PROBE_DIR": work}), patch.object(probe, "run", fake_run), patch.object(probe.urllib.request, "urlopen", return_value=SimpleNamespace(close=lambda: None)):
                with self.assertRaisesRegex(AssertionError, "segments"):
                    probe.main()
            receipt = json.loads((Path(work) / "receipt.json").read_text())
            self.assertFalse(receipt["passed"])

if __name__ == "__main__":
    unittest.main()

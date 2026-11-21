#!/usr/bin/env python3
"""Exercise scenario predicates without starting the stack."""
from pathlib import Path
import re
import json
import subprocess
import unittest

ROOT = Path(__file__).resolve().parent

def function(scenario, name):
    source = (ROOT / "scenarios" / scenario).read_text()
    match = re.search(r"^" + re.escape(name) + r"\(\) \{\n.*?^\}", source, re.M | re.S)
    assert match, name
    return match.group(0)

class ProcessingScenarioGuards(unittest.TestCase):
    def test_rotation_absence_requires_positive_job_logs(self):
        predicate = function("79-rotated-upload.sh", "no_upload_degradation")
        result = subprocess.run(["bash", "-c", predicate + '\nlog_json() { return 0; }; no_upload_degradation fixture now'])
        self.assertNotEqual(result.returncode, 0, "empty logs falsely prove no degradation")

    def test_rotation_absence_rejects_unrelated_job_logs(self):
        predicate = function("79-rotated-upload.sh", "no_upload_degradation")
        script = predicate + r'''
log_json() { printf '%s\n' '{"artifact_hash":"other","stream":"processing+other","msg":"ordinary processing event"}' | jq -c "$2"; }
no_upload_degradation fixture now
'''
        result = subprocess.run(["bash", "-c", script])
        self.assertNotEqual(result.returncode, 0, "another job's logs falsely proved no degradation")

    def test_rotation_rejects_deferred_and_refused_fallbacks_with_positive_logs(self):
        predicate = function("79-rotated-upload.sh", "no_upload_degradation")
        script = predicate + r'''
FIXTURE_LOGS=$1
log_json() { printf '%s\n' "$FIXTURE_LOGS" | jq -c "$2"; }
no_upload_degradation fixture now
'''
        for message in ("Stream transcode degraded to local processing", "Stream transcode failed; local replacement awaits job admission", "Stream transcode failed; no local replacement was admitted"):
            row = {"artifact_hash": "fixture", "stream": "processing+fixture", "msg": message}
            result = subprocess.run(["bash", "-c", script, "guard", json.dumps(row)])
            self.assertNotEqual(result.returncode, 0, "positive job evidence hid failure: " + message)
        ordinary = {"artifact_hash": "fixture", "stream": "processing+fixture", "msg": "ordinary processing event"}
        result = subprocess.run(["bash", "-c", script, "guard", json.dumps(ordinary)])
        self.assertEqual(result.returncode, 0, "ordinary job activity was refused")

    def test_livepeer_policy_does_not_prove_transcoded_output(self):
        predicate = function("66-transcode-ladders.sh", "livepeer_produced")
        script = predicate + r'''
FIXTURE_LOGS=$1
pg() { printf '%s\n' '[{"process":"Livepeer"}]'; }
json_has() { printf '%s\n' "$1" | jq -e "$2" >/dev/null; }
log_json() { printf '%s\n' "$FIXTURE_LOGS" | jq -c "$2"; }
livepeer_produced fixture now
'''
        cases = [
            ("", False),
            (json.dumps({"trigger_type": "LIVEPEER_SEGMENT_COMPLETE", "stream_name": "processing+fixture", "rendition_count": "0"}), False),
            (json.dumps({"trigger_type": "LIVEPEER_SEGMENT_COMPLETE", "stream_name": "processing+other", "rendition_count": "2"}), False),
            (json.dumps({"trigger_type": "LIVEPEER_SEGMENT_COMPLETE", "stream_name": "processing+fixture", "rendition_count": "2"}), True),
        ]
        for logs, expected in cases:
            with self.subTest(logs=logs):
                result = subprocess.run(["bash", "-c", script, "guard", logs], capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, expected, "process policy falsely proves Livepeer produced a rendition: " + logs)

    def test_gateway_wait_starts_after_health_is_observed(self):
        predicates = "\n".join(function("69-vod-ladder-geometry.sh", name) for name in ("gateway_state", "gateway_present"))
        script = predicates + '''
GATEWAY_HEALTHY_AT=0
DISCOVERY_HEALTHY_SINCE=''
NOW=110
pg() { printf 'healthy'; }
date() { printf '%s' "$NOW"; }
if gateway_present; then echo 'stale pool accepted immediately'; exit 1; fi
NOW=176
gateway_present || { echo 'settled pool still refused'; exit 1; }
'''
        result = subprocess.run(["bash", "-c", script], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

if __name__ == "__main__":
    unittest.main()

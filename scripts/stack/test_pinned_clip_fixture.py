#!/usr/bin/env python3
"""Keep the reconnect fixture on a compatible tier with retained historical media."""
from pathlib import Path
import re
import shlex
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class PinnedClipFixtureTest(unittest.TestCase):
    def test_recorded_source_bytes_are_proven_before_the_fault(self):
        source = (ROOT / "scripts/stack/scenarios/85-pinned-clip-reconnect.sh").read_text()
        probe = re.search(r"^probe_clip_source\(\) \{\n.*?^\}", source, re.M | re.S)
        with tempfile.TemporaryDirectory() as directory:
            script = "STACK_STATE_DIR=" + shlex.quote(directory) + "\nSTACK_LIB_DIR=" + shlex.quote(str(ROOT / "scripts/stack")) + "\n"
            script += '''FOGHORN_A_DB=foghorn STACK_TENANT_ID=tenant EDGE_A_HOST=edge
pg() { printf '%s\\n' '{"source_stream_name":"dvr+fixture","source_start_unix":"100","source_stop_unix":"104","source_credential":"credential"}'; }
curl() { while [ "$1" != --output ]; do shift; done; : >"$2"; }
'''
            script += (probe[0] if probe else 'probe_clip_source() { return 0; }') + "\nprobe_clip_source hash\n"
            result = subprocess.run(["bash", "-c", script], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0, "database segment rows cannot prove that the pinned source serves MKV media")
        self.assertIsNotNone(probe)
        self.assertIn('eventually 45 "pinned source $hash decodes before the fault" probe_clip_source "$hash" || finish', source)
        self.assertLess(source.index('decodes before the fault'), source.index('CUT_TS='))

    def test_failed_clip_cannot_enter_replica_decode_retries(self):
        source = (ROOT / "scripts/stack/scenarios/85-pinned-clip-reconnect.sh").read_text()
        loop = source[source.index('for index in "${!ids[@]}"; do'):]
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / "decode-attempted"
            script = "marker=" + shlex.quote(str(marker)) + "\n"
            script += '''ids=(clip) hashes=(hash) playbacks=(playback)
FOGHORN_A_URLS=http://replica FOGHORN_B_URLS=http://peer
FOGHORN_A_DB=foghorn STACK_TENANT_ID=tenant
eventually() { case "$2" in *"reaches ready") return 1 ;; *) touch "$marker"; return 0 ;; esac; }
check() { return 0; }
pg() { echo 0; }
finish() { exit 1; }
'''
            result = subprocess.run(["bash", "-c", script + loop], capture_output=True, text=True)
            self.assertEqual(result.returncode, 1, result.stderr)
            self.assertFalse(marker.exists(), "a terminal failed asset cannot become playable by polling replicas")

    def test_exact_clip_measurement_uses_frame_addressable_source(self):
        source = (ROOT / "scripts/stack/scenarios/85-pinned-clip-reconnect.sh").read_text()
        library = (ROOT / "scripts/stack/lib.sh").read_text()
        pattern = r"^publish\(\) \{.*?^\}"
        publisher = re.search(pattern, library, re.M | re.S)[0]
        override = re.search(pattern, source, re.M | re.S)
        with tempfile.TemporaryDirectory() as directory:
            capture = Path(directory) / "arguments"
            script = "STACK_STATE_DIR=" + shlex.quote(directory) + "\nARGS_CAPTURE=" + shlex.quote(str(capture)) + "\n"
            script += "need() { return 0; }\nffmpeg() { printf '%s\\n' \"$@\" >\"$ARGS_CAPTURE\"; }\n"
            script += publisher + "\n" + (override[0] + "\n" if override else "")
            script += "publish rtmp://fixture key 640x360 15 600 >/dev/null\nwait\n"
            result = subprocess.run(["bash", "-c", script], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            args = capture.read_text().splitlines()
            self.assertEqual(args[args.index("-g") + 1], "1",
                             "copying from a two-second GOP includes its preceding keyframe, so a four-second frame assertion is invalid")
            self.assertEqual(args[args.index("-bf") + 1], "0")

    def test_fault_closes_server_streams_before_releasing_jobs(self):
        source = (ROOT / "scripts/stack/scenarios/85-pinned-clip-reconnect.sh").read_text()
        start = source.index('netfault "$EDGE_SERVICE" sh -c "iptables')
        end = source.index('\nlost_control()', start)
        with tempfile.TemporaryDirectory() as directory:
            marker = directory + '/server-disconnects'
            script = 'marker=' + shlex.quote(marker) + '\n: >"$marker"\n'
            script += '''EDGE_SERVICE=edge CONTROL_PORT=18029 EDGE_IP=127.0.0.2
CONTROL_SERVICES=(foghorn foghorn-2)
netfault() { [ "$1" = edge ] || printf '%s\\n' "$1" >>"$marker"; return 0; }
fail() { return 1; }
finish() { exit 1; }
'''
            script += source[start:end] + '\n[ "$(grep -c . "$marker")" = 2 ]\n'
            result = subprocess.run(['bash', '-c', script], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, 'blocked client resets cannot notify the server: ' + result.stderr)
        observed = source.index("'Foghorn observes the control disconnect'")
        self.assertLess(observed, source.index('for hash in "${hashes[@]}"', source.index('CUT_TS=')))

    def test_queue_hold_cannot_lose_to_dispatch_before_api_returns(self):
        source = (ROOT / "scripts/stack/scenarios/85-pinned-clip-reconnect.sh").read_text()
        hold = re.search(r"^hold_clip\(\) \{\n.*?^\}", source, re.M | re.S)[0]
        gate = re.search(r"^install_clip_queue_gate\(\) \{\n.*?^\}", source, re.M | re.S)
        with tempfile.TemporaryDirectory() as directory:
            setup = "gate=" + shlex.quote(directory + "/gate") + "\nstate=" + shlex.quote(directory + "/state") + "\n"
            setup += '''STACK_TENANT_ID=tenant FOGHORN_A_DB=foghorn EDGE_NODE=edge DVR_ATTEMPT=0
ids=() hashes=() playbacks=()
gql() {
  if [ -f "$gate" ]; then echo queued >"$state"; else echo dispatched >"$state"; fi
  printf '%s\\n' '{"data":{"createClip":{"id":"clip","clipHash":"hash","playbackId":"playback"}}}'
}
pg_fixture() {
  case "$2" in
    *"CREATE TRIGGER"*) touch "$gate" ;;
    *"UPDATE foghorn.processing_jobs"*) [ "$(cat "$state")" != queued ] || printf 'job\\n' ;;
  esac
}
log() { printf '%s\\n' "$*" >&2; }
'''
            if gate:
                setup += gate[0] + '\ninstall_clip_queue_gate\n'
                self.assertLess(source.index('install_clip_queue_gate ||'), source.index("eventually 30 'queue a pinned live clip"))
            result = subprocess.run(['bash', '-c', setup + hold + '\nhold_clip live\n'],
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_fixture_is_funded_or_collectible_before_streaming(self):
        source = (ROOT / "scripts/stack/scenarios/85-pinned-clip-reconnect.sh").read_text()
        model = re.search(r"--billing-model (\w+)", source)[1]
        setup = source[:source.index('netfault_start "$EDGE_SERVICE"')]
        if model == "prepaid":
            self.assertIn("admin billing credit", setup)
            self.assertIn("availableBalanceCents", setup)
        else:
            self.assertIn("admin billing grant set", setup,
                          "a postpaid fixture needs confirmed collection or an invoice grant before rated work")
            self.assertIn("--collection invoice", setup)

    def test_fixture_billing_model_and_retention_match_catalog(self):
        source = (ROOT / "scripts/stack/scenarios/85-pinned-clip-reconnect.sh").read_text()
        assignment = re.search(r"--tier (\w+) --billing-model (\w+)", source)
        self.assertIsNotNone(assignment)
        tier, model = assignment.groups()
        catalog = (ROOT / "api_billing/internal/bootstrap/catalog/billing_tiers.yaml").read_text()
        block = re.search(r"^  - tier_name: " + re.escape(tier) + r"\n(.*?)(?=^  - tier_name:|\Z)",
                          catalog, re.M | re.S)
        self.assertIsNotNone(block)
        level = int(re.search(r"tier_level: (\d+)", block[1])[1])
        prepaid = "is_default_prepaid: true" in block[1] or level == 0
        self.assertEqual(model, "prepaid" if prepaid else "postpaid")
        overrides = (ROOT / "scripts/stack/seed-purser.sql").read_text()
        self.assertNotRegex(overrides, r"\('" + re.escape(tier) + r"', 'dvr_(?:default|max)_window_seconds'",
                            "fixture inherits the stack's short DVR window and cannot cut historical media")
        window = int(re.search(r"dvr_default_window_seconds: (\d+)", block[1])[1])
        self.assertGreater(window, 150)
        self.assertIn('tierName=="' + tier + '"', source)


if __name__ == "__main__":
    unittest.main()

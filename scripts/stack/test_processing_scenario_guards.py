#!/usr/bin/env python3
"""Exercise scenario predicates without starting the stack."""
from pathlib import Path
import re
import json
import subprocess
import shlex
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent

def function(scenario, name):
    source = (ROOT / "scenarios" / scenario).read_text()
    match = re.search(r"^" + re.escape(name) + r"\(\) \{\n.*?^\}", source, re.M | re.S)
    assert match, name
    return match.group(0)

class ProcessingScenarioGuards(unittest.TestCase):
    def test_hls_restart_accepts_only_scoped_503_and_fresh_media_recovery(self):
        source = (ROOT / "lib.sh").read_text()
        predicate = re.search(r"^hls_restart_continuity_state\(\) \{\n.*?^\}", source, re.M | re.S).group(0)
        cases = [
            ("scoped-503", "100 ok\n102 fail HTTPError:HTTP_Error_503:_Service_Unavailable\n106 ok\n108 ok\n", "ok"),
            ("authoritative-denial", "100 ok\n102 fail HTTPError:HTTP_Error_404:_Error_opening_stream\n108 ok\n", "fail"),
            ("unrelated-error", "100 ok\n102 fail HTTPError:HTTP_Error_500:_Server_Error\n108 ok\n", "fail"),
            ("before-outage", "100 fail HTTPError:HTTP_Error_503:_Service_Unavailable\n108 ok\n", "fail"),
            ("after-outage", "106 fail HTTPError:HTTP_Error_503:_Service_Unavailable\n108 ok\n", "fail"),
            ("no-recovery", "100 ok\n102 fail HTTPError:HTTP_Error_503:_Service_Unavailable\n", "pending"),
            ("stale-recovery", "106 ok\n", "pending"),
            ("empty", "", "pending"),
        ]
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "viewer.log"
            for name, rows, expected in cases:
                with self.subTest(case=name):
                    path.write_text(rows)
                    now = 200 if name == "stale-recovery" else 110
                    script = 'date() { printf "' + str(now) + '\\n"; }\n' + predicate
                    script += '\nhls_restart_continuity_state ' + shlex.quote(str(path)) + ' 100 101 105\n'
                    result = subprocess.run(["bash", "-c", script], capture_output=True, text=True)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout.strip(), expected)

    def test_local_recording_measures_increment_above_rtmp_ack_traffic(self):
        # The real CBR trace has 0.83-0.88 Mb/s of outgoing TCP ACKs before S3.
        baseline = [dict(lastHeartbeat=str(i), observedSource="OBSERVED_SOURCE_NIC",
                         bandwidthDownMbps=4.73, bandwidthUpMbps=up)
                    for i, up in enumerate((0.83, 0.87))]
        recording = [dict(baseline[0], lastHeartbeat=str(i + 2), bandwidthUpMbps=up)
                     for i, up in enumerate((0.863592, 0.858264, 0.886896, 0.864752))]
        predicates = "\n".join(function("70-node-capacity.sh", name) for name in
                               ("dvr_network_baseline", "local_recording_network_matches", "upload_network_matches"))
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp) / "baseline.jsonl"
            samples = Path(tmp) / "recording.jsonl"
            def write(path, rows):
                path.write_text("".join(json.dumps(row) + "\n" for row in rows))
            write(base, baseline)
            cases = [("ack-only", recording, True),
                     ("real-egress", [dict(row, bandwidthUpMbps=1.5) for row in recording], False),
                     ("missing-rate", [dict(row, bandwidthUpMbps=None) for row in recording], False),
                     ("missing-sample", recording[:3], False),
                     ("repeated-heartbeat", [recording[0]] * 4, False),
                     ("non-NIC", [dict(row, observedSource="UNKNOWN") for row in recording], False)]
            for name, rows, expected in cases:
                with self.subTest(case=name):
                    write(samples, rows)
                    script = 'set -euo pipefail\n' + predicates
                    script += '\nbaseline_up=$(dvr_network_baseline ' + shlex.quote(str(base)) + ')\n'
                    script += 'local_recording_network_matches ' + shlex.quote(str(samples))
                    result = subprocess.run(["bash", "-c", script], capture_output=True, text=True)
                    self.assertEqual(result.returncode == 0, expected, result.stdout + result.stderr)
            for up, expected in ((0.87, False), (1.5, True)):
                script = 'set -euo pipefail\n' + predicates + '\nbaseline_up=0.85\nupload_network_matches'
                result = subprocess.run(["bash", "-c", script], input=json.dumps(dict(baseline[0], bandwidthUpMbps=up)), capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, expected, result.stdout + result.stderr)
            for bad in (baseline[:1], [dict(row, bandwidthUpMbps=None) for row in baseline], [baseline[0]] * 2):
                write(base, bad)
                result = subprocess.run(["bash", "-c", predicates + '\ndvr_network_baseline ' + shlex.quote(str(base))], capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0, "invalid baseline cannot prove disk-only recording")

    def test_local_ladders_may_run_concurrently_only_within_reserved_memory(self):
        predicate = function("66-transcode-ladders.sh", "local_jobs_have_reserved_capacity")
        row = {"artifact_hash": "first", "status": "processing", "retry_count": 0,
               "processing_node_id": "edge", "memory_reserved_bytes": 1187428044}
        second = dict(row, artifact_hash="second")
        for mode, budget, expected in (("running", 2684354560, True), ("queued", 2684354560, True),
                                       ("running", 2000000000, False), ("retry", 2684354560, False),
                                       ("unreserved", 2684354560, False), ("missing", 2684354560, False)):
            with self.subTest(mode=mode):
                current = dict(second)
                if mode == "queued":
                    current.update(status="queued", wait_reason="memory", memory_reserved_bytes=0)
                if mode == "retry": current["retry_count"] = 1
                if mode == "unreserved": current["memory_reserved_bytes"] = 0
                rows = [row] if mode == "missing" else [row, current]
                script = 'set -uo pipefail\nFOGHORN_A_DB=cell STACK_TENANT_ID=tenant CELL_A_CLUSTER=cell\nlocal_hashes=(first second)\n'
                script += 'pg() { case "$2" in *jsonb_agg*) printf "%s\\n" ' + shlex.quote(json.dumps(rows)) + ' ;; *) printf "f\\n" ;; esac; }\n'
                script += 'stack_exec() { printf "%s\\n" ' + shlex.quote(json.dumps({"processing_memory_budget_bytes": budget})) + '; }\n'
                result = subprocess.run(["bash", "-c", script + predicate + '\nlocal_jobs_have_reserved_capacity'],
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, expected, result.stdout + result.stderr)

    def test_pull_public_session_uses_the_cell_identity(self):
        session = function("58-pull-stream-lifecycle.sh", "session_live")
        script = 'set -uo pipefail\nFOGHORN_B_DB=cell STACK_TENANT_ID=tenant UUID=stream\n'
        script += '''pg() {
          case "$2" in
            *"SELECT cell_session_id::text"*) printf '3585365d-90ef-4bbb-ba17-5bc3709c9685|4222\\n' ;;
            *) printf 'c0c57492-3bbf-495f-8546-7bded75d6603|4222\\n' ;;
          esac
        }
        '''
        script += session + '\nsession_live && printf "%s\\n" "$ROW"\n'
        result = subprocess.run(["bash", "-c", script], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "3585365d-90ef-4bbb-ba17-5bc3709c9685|4222",
                         "the public pull session identifies the cell lifecycle, not an individual buffer row")

    def test_network_segments_follow_the_admitted_runtime(self):
        source = (ROOT / "scenarios" / "35-network-observations.sh").read_text()
        start = source.index('INTERNAL=$(internal_name_of "$UUID")')
        end = source.index('\nwait_publisher', start)
        setup = source[start:end]
        for runtime in ("live+logical_fw1p_generation", "live+logical", ""):
            with self.subTest(runtime=runtime):
                script = 'set -uo pipefail\nUUID=stream FOGHORN_A_DB=cell STACK_TENANT_ID=tenant\n'
                script += 'internal_name_of() { printf "logical\\n"; }\n'
                script += 'video_heights() { printf "360\\n"; }; eventually() { shift 2; "$@"; }\n'
                script += 'pg() { printf "%s\\n" ' + shlex.quote(runtime) + '; }\n'
                script += 'fail() { printf "%s\\n" "$*" >&2; }; finish() { exit 1; }\n'
                script += setup + '\nprintf "%s\\n" "$DELIVERY_PREFIX"\n'
                result = subprocess.run(["bash", "-c", script], capture_output=True, text=True)
                if not runtime:
                    self.assertNotEqual(result.returncode, 0, "missing admission cannot prove gateway settlement")
                    continue
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout.strip(), runtime + "-",
                                 "gateway observations must follow the admitted fenced runtime, not the logical stream")

    def test_producer_swap_preserves_pipes_in_job_json(self):
        source = (ROOT / "scenarios" / "12-producer-swap.sh").read_text()
        row_function = re.search(r"^job_row\(\) \{[^\n]*\n.*?^\}", source, re.M | re.S).group(0)
        first = next(line for line in source.splitlines() if line.startswith('P=$(job_row'))
        final = source[source.index('ROW=$(job_row'):source.index('check "the job finished')]
        final = "\n".join(line for line in final.splitlines() if line.startswith(("ROW=", "P=", "ERR=")))
        error = "PROCESS_TRACKS_CHANGED: old|new producer"
        for phase, processes in (("initial", [{"process": "Livepeer", "track_select": "video=1|audio=none"}]),
                                 ("final", [{"process": "AV", "track_select": "video=1|audio=none"}])):
            with self.subTest(phase=phase):
                record = {"status": "completed", "retry_count": 1, "processes": processes, "error": error}
                legacy = "completed|1|" + json.dumps(processes) + "|" + error
                # psql's default unaligned format uses pipes. The JSON query
                # keeps the entire row in one field, including selector pipes.
                pg = 'pg() { case "$2" in *jsonb_build_object*) printf "%s\\n" ' + shlex.quote(json.dumps(record))
                pg += ' ;; *) printf "%s\\n" ' + shlex.quote(legacy) + ' ;; esac; }\n'
                script = 'set -uo pipefail\nFOGHORN_A_DB=foghorn STACK_TENANT_ID=fixture VHASH=artifact\n'
                script += row_function + '\n' + pg + (first if phase == "initial" else final)
                script += '\nprintf "%s\\n" "$P"\n'
                if phase == "final":
                    script += 'printf "%s\\n" "$ERR"\n'
                result = subprocess.run(["bash", "-c", script], capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                rows = result.stdout.splitlines()
                try:
                    actual = json.loads(rows[0])
                except (ValueError, IndexError) as exc:
                    self.fail("producer-swap assertion truncated selector JSON at a pipe: " + result.stdout + str(exc))
                self.assertEqual(actual, processes, "producer types and selectors must survive the DB row parser")
                if phase == "final":
                    self.assertEqual(rows[1], error, "failure details must preserve pipes too")

    def test_vod_multipart_upload_sends_each_source_byte_once(self):
        lib = (ROOT / "lib.sh").read_text()
        upload = re.search(r"^upload_vod\(\) \{\n.*?^\}", lib, re.M | re.S).group(0)
        for size in (4, 8, 16, 20):
            with self.subTest(size=size), tempfile.TemporaryDirectory() as tmp:
                directory = Path(tmp)
                source = bytes(range(size))
                (directory / "source.mp4").write_bytes(source)
                count = (size + 7) // 8
                response = {"data": {"createVodUpload": {"id": "upload", "partSize": 8,
                            "parts": [{"partNumber": i, "presignedUrl": "mock://parts/" + str(i)}
                                      for i in range(1, count + 1)]}}}
                script = "set -uo pipefail\n" + upload + '\nFIXTURE_DIR=' + shlex.quote(tmp) + '\n'
                script += '''
gql() {
  case "$1" in
    *createVodUpload*)
      printf '%s' "$1" > "$FIXTURE_DIR/create-query"
      printf '%s\\n' ''' + shlex.quote(json.dumps(response)) + ''' ;;
    *) printf '%s\\n' '{"data":{"completeVodUpload":{"id":"asset"}}}' ;;
  esac
}
curl() {
  local body='' url="${!#}"
  while [ "$#" -gt 0 ]; do
    if [ "$1" = --data-binary ]; then body=$2; shift; fi
    shift
  done
  if [ "$body" = @- ]; then cat > "$FIXTURE_DIR/part-${url##*/}"
  else cat "${body#@}" > "$FIXTURE_DIR/part-${url##*/}"; fi
  printf 'HTTP/1.1 200 OK\\r\\nETag: "%s"\\r\\n\\r\\n' "${url##*/}"
}
upload_vod "$FIXTURE_DIR/source.mp4" fixture
'''
                result = subprocess.run(["bash", "-c", script], capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                bodies = [(directory / ("part-" + str(i))).read_bytes() for i in range(1, count + 1)]
                self.assertEqual(b"".join(bodies), source,
                                 "multipart upload repeats the whole source for every presigned part")
                self.assertEqual([len(body) for body in bodies], [min(8, size - i * 8) for i in range(count)])
                self.assertRegex((directory / "create-query").read_text(), r"\bpartSize\b")

    def vod_webhook_result(self, event, artifact):
        source = (ROOT / "scenarios" / "03-livepeer-vod.sh").read_text()
        invocation = next(line.strip() for line in source.replace("\\\n", "").splitlines()
                          if ' saw_event "$SINCE" ' + event in line)
        lib = (ROOT / "lib.sh").read_text()
        predicate = re.search(r"^saw_event\(\) \{\n.*?^\}", lib, re.M | re.S).group(0)
        payload = json.dumps({"artifact": {"artifactId": artifact},
                              "reason": "MEDIA_FAILURE_REASON_SOURCE_UNAVAILABLE"})
        delivery = shlex.quote(event + " fixture valid " + payload)
        script = predicate + '\n' + '''
eventually() { shift 2; "$@"; }
SINCE=0
VHASH=expected
MHASH=expected
''' + 'webhook_events() { printf "%s\\n" ' + delivery + '; }\n' + invocation
        return subprocess.run(["bash", "-c", script], capture_output=True, text=True)

    def test_vod_ready_webhook_belongs_to_the_livepeer_upload(self):
        unrelated = self.vod_webhook_result("upload.ready", "fallback-upload")
        self.assertNotEqual(unrelated.returncode, 0,
                            "the fallback upload's ready event falsely proves the Livepeer upload succeeded")
        matching = self.vod_webhook_result("upload.ready", "expected")
        self.assertEqual(matching.returncode, 0, matching.stderr)

    def test_missing_source_webhook_belongs_to_the_import(self):
        unrelated = self.vod_webhook_result("upload.failed", "other-upload")
        self.assertNotEqual(unrelated.returncode, 0,
                            "another upload's failure falsely proves the missing-source import emitted its event")
        matching = self.vod_webhook_result("upload.failed", "expected")
        self.assertEqual(matching.returncode, 0, matching.stderr)

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

    def test_gateway_wait_requires_positive_selection_pool(self):
        predicates = "\n".join(function("69-vod-ladder-geometry.sh", name) for name in ("gateway_state", "gateway_present"))
        script = predicates + r'''
GATEWAY=fixture-gateway
STATE=$1 HEALTH=$2 HTTP_EXIT=$3
pg() { printf '%s' "$STATE"; }
curl() {
  [ "${!#}" = http://fixture-gateway:8935/healthz ] || return 22
  printf '%s' "$HEALTH"
  return "$HTTP_EXIT"
}
gateway_present
'''
        cases = [("healthy", '{"orchestrators":1}', 0, True),
                 ("unhealthy", '{"orchestrators":1}', 0, False),
                 ("healthy", '{"orchestrators":0}', 0, False),
                 ("healthy", '{"orchestrators":true}', 0, False),
                 ("healthy", '{}', 0, False),
                 ("healthy", 'unreadable', 0, False),
                 ("healthy", '', 0, False),
                 ("healthy", '   ', 0, False),
                 ("healthy", '{"orchestrators":1}', 22, False),
                 ("healthy", '{"orchestrators":1}\n{"orchestrators":1}', 0, False),
                 ("healthy", '', 22, False)]
        for state, health, code, expected in cases:
            with self.subTest(state=state, health=health, code=code):
                result = subprocess.run(["bash", "-c", script, "fixture", state, health, str(code)], capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, expected, result.stdout + result.stderr)

if __name__ == "__main__":
    unittest.main()

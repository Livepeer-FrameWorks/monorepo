import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

const fakeCurl = [
  "#!/usr/bin/env bash",
  "set -eu",
  "output= headers= cookies=",
  "while [[ $# -gt 0 ]]; do",
  '  case "$1" in',
  "    --output) output=$2; shift ;;",
  "    --dump-header) headers=$2; shift ;;",
  "    --cookie-jar) cookies=$2; shift ;;",
  "    https://*) url=$1 ;;",
  "  esac",
  "  shift",
  "done",
  'printf "%s\\n" "$url" >>"$SMOKE_TEST_CALLS"',
  'case "$url" in',
  '  */auth/login) printf session-cookie >"$cookies" ;;',
  "  */graphql)",
  "    payload=$(cat)",
  "    if [[ $payload == *deleteStream* ]]; then",
  '      printf \'{"data":{"deleteStream":{"__typename":"DeleteSuccess"}}}\'',
  "    elif [[ $SMOKE_TEST_CASE == missing-id ]]; then",
  '      printf \'{"data":{"createStream":{"__typename":"Stream","streamId":null}}}\'',
  "    else",
  '      printf \'{"data":{"createStream":{"__typename":"Stream","streamId":"test-stream","playbackId":"test-playback","streamKey":"test-key"}}}\'',
  "    fi ;;",
  '  */ingest/*) printf \'{"primary":{"rtmpUrl":"rtmp://edge.staging-media-eu.staging.frameworks.network/live/test-key"}}\' ;;',
  '  */play/*) printf "HTTP/1.1 302 Found\\r\\nLocation: https://edge.staging-media-eu.staging.frameworks.network/index.m3u8\\r\\n" >"$headers" ;;',
  '  */index.m3u8) printf "#EXTM3U\\n#EXTINF:2,\\nsegment.ts\\n" ;;',
  "  */segment.ts)",
  "    if [[ $SMOKE_TEST_CASE == http-error ]]; then",
  '      printf "upstream unavailable" >"$output"',
  "      exit 22",
  "    elif [[ $SMOKE_TEST_CASE == empty-segment ]]; then",
  '      : >"$output"',
  "    else",
  '      printf synthetic-media-bytes >"$output"',
  "    fi ;;",
  "  *) exit 99 ;;",
  "esac",
].join("\n");

for (const scenario of ["success", "http-error", "empty-segment", "missing-id"]) {
  test("disposable staging smoke: " + scenario, () => {
    const dir = mkdtempSync(join(tmpdir(), "staging-smoke-test-"));
    try {
      const bin = join(dir, "bin");
      mkdirSync(bin);
      for (const [name, body] of Object.entries({
        curl: fakeCurl,
        sops: "#!/usr/bin/env bash\nprintf synthetic-password\n",
        ffmpeg: "#!/usr/bin/env bash\nexec /bin/sleep 30\n",
        sleep: "#!/usr/bin/env bash\nexit 0\n",
      })) {
        writeFileSync(join(bin, name), body, { mode: 0o755 });
      }
      const secrets = join(dir, "secrets.env");
      const calls = join(dir, "calls");
      writeFileSync(secrets, "synthetic fixture; sops is mocked\n");
      const result = spawnSync("bash", ["scripts/staging-media-smoke.sh"], {
        encoding: "utf8",
        timeout: 10000,
        env: {
          ...process.env,
          PATH: bin + ":" + process.env.PATH,
          STAGING_SECRETS_FILE: secrets,
          SMOKE_TEST_CASE: scenario,
          SMOKE_TEST_CALLS: calls,
          TMPDIR: dir,
        },
      });
      assert.ifError(result.error);
      if (scenario === "success") {
        assert.equal(result.status, 0, result.stderr);
        assert.match(result.stdout, /PASS stream=test-stream/);
      } else {
        assert.notEqual(result.status, 0, result.stdout);
        assert.doesNotMatch(result.stdout, /PASS/);
        assert.match(
          result.stderr,
          scenario === "missing-id" ? /incomplete identifiers/ : /nonempty successful HLS segment/
        );
      }
      const requests = readFileSync(calls, "utf8").trim().split("\n");
      assert.equal(
        requests.filter((url) => url.endsWith("/graphql")).length,
        scenario === "missing-id" ? 1 : 2
      );
      assert.doesNotMatch(result.stdout + result.stderr, /synthetic-password/);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
}

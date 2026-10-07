#!/usr/bin/env bash
# Unit checks for scripts/stack/logcheck.sh against a stubbed docker CLI, so they
# need no stack. Every `docker compose logs` call advances the stub's clock by
# one step; a service's log shows the lines logged up to the current step.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/fw-stack-logcheck-test.XXXXXX")
slot=200
slot_dir="$repo/.stack/slot-$slot"
slot_existed=0
[ -d "$slot_dir" ] && slot_existed=1
cleanup() {
  rm -rf "$work"
  [ "$slot_existed" = 1 ] || rm -rf "$slot_dir"
}
trap cleanup EXIT

mkdir -p "$work/bin"
cat >"$work/bin/docker" <<'EOF'
#!/usr/bin/env bash
# Stub: `version` and `compose config/ps/logs` from the fixture in $FIXTURE_DIR.
# A fixture line "<step> <service> <text>" is logged at <step>.
case "$1" in
version) echo amd64; exit 0 ;;
esac
args=" $* "
case "$args" in
*" config --services "*) echo edge; echo foghorn; exit 0 ;;
*" ps --services "*) echo edge; echo foghorn; exit 0 ;;
*" logs "*)
  step=$(($(cat "$FIXTURE_DIR/clock" 2>/dev/null || echo 0) + 1))
  echo "$step" >"$FIXTURE_DIR/clock"
  svc=${!#}
  awk -v now="$step" -v svc="$svc" '$1 <= now && $2 == svc { $1 = ""; $2 = ""; sub(/^  /, ""); print }' "$FIXTURE_DIR/lines"
  exit 0
  ;;
esac
exit 0
EOF
chmod +x "$work/bin/docker"

failures=0
expect() { # expect <description> <command...>
  local what=$1
  shift
  if "$@"; then printf 'ok    %s\n' "$what"; else printf 'FAIL  %s\n' "$what"; failures=$((failures + 1)); fi
}
run_logcheck() { # run_logcheck <fixture lines>: logcheck's exit status on a fresh clock
  printf '%s\n' "$1" >"$work/lines"
  rm -f "$work/clock"
  PATH="$work/bin:$PATH" FIXTURE_DIR="$work" STACK_SLOT=$slot STACK_TARGET=stack \
    bash "$here/logcheck.sh" >"$work/out" 2>&1
}
passes() { run_logcheck "$1"; }
fails() { ! run_logcheck "$1"; }

stream='live+abcDEF123'
refusal="{\"msg\":\"livepeer auth: refused a transcode of an ended ingest session\",\"stream\":\"$stream\"}"
mist_403="[2026-10-07 17:07:14] MistInBuffer:$stream (1366) WARN: Process \`Livepeer\` (PID 1416) exited with unrecoverable error (code 2: Livepeer upload fatal HTTP status 403 Forbidden), disabling restart"

# Foghorn logs its refusal, then Mist logs the 403, both while logcheck already
# runs: after its first read and before it reads the edge.
expect "a session_ended 403 whose refusal is logged while the check runs passes" \
  passes "2 foghorn $refusal
2 edge $mist_403"
expect "a session_ended 403 logged before the check passes" \
  passes "0 foghorn $refusal
0 edge $mist_403"
expect "a 403 for a stream Foghorn never refused still fails" \
  fails "0 edge $mist_403"
expect "a 403 for another stream than the refused one still fails" \
  fails "0 foghorn ${refusal//abcDEF123/otherStream9}
0 edge $mist_403"

if [ "$failures" -gt 0 ]; then
  echo "logcheck checks: $failures failed"
  exit 1
fi
echo "logcheck checks: ok"

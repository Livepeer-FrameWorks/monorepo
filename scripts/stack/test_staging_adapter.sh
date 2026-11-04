#!/usr/bin/env bash
# Unit checks for scripts/stack/staging-adapter.sh against a stubbed ssh, so
# they need no deployment: the journal reads a scenario asks for reach
# journalctl in a form it accepts.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/fw-stack-adapter-test.XXXXXX")
trap 'rm -rf "$work"' EXIT
failures=0
expect() { # expect <description> <command...>
  local what=$1
  shift
  if "$@"; then printf 'ok    %s\n' "$what"; else printf 'FAIL  %s\n' "$what"; failures=$((failures + 1)); fi
}

mkdir -p "$work/bin"
cat >"$work/bin/ssh" <<'EOF'
#!/usr/bin/env bash
# Stub: records the remote command line and, like ssh without -n, reads its stdin.
printf '%s\n' "$*" >>"$FIXTURE_DIR/ssh-calls"
case " $* " in *" -n "*) ;; *) cat >/dev/null ;; esac
if [ -n "${FIXTURE_FAIL_UNIT:-}" ]; then
  case "$*" in *"$FIXTURE_FAIL_UNIT"*) exit 1 ;; esac
fi
if [ -n "${FIXTURE_JOURNAL:-}" ]; then cat "$FIXTURE_JOURNAL"; fi
EOF
chmod +x "$work/bin/ssh"
printf '%s\n' 'edge|ops@10.0.0.23|frameworks-helmsman.service' >"$work/service-map.txt"

journal_since() { # journal_since <since>: the --since value journalctl was given
  : >"$work/ssh-calls"
  (
    export PATH="$work/bin:$PATH" FIXTURE_DIR="$work" STAGING_SERVICE_MAP_FILE="$work/service-map.txt"
    # shellcheck source=scripts/stack/staging-adapter.sh
    . "$here/staging-adapter.sh"
    stack_logs --since "$1" edge >/dev/null
  )
  sed -nE 's/.*--since ([^ ]+).*/\1/p' "$work/ssh-calls"
}
# docker compose logs takes a bare duration ("10m") as "that long ago";
# journalctl refuses it ("Failed to parse timestamp: 10m") and needs "-10m".
relative_is_signed() { [ "$(journal_since 10m)" = -10m ]; }
expect "a relative since (10m) reaches journalctl as -10m" relative_is_signed
absolute_unchanged() { [ "$(journal_since 2026-10-22T09:00:00.123Z)" = 2026-10-22T09:00:00.123Z ]; }
expect "an absolute since reaches journalctl unchanged" absolute_unchanged

# A service with several rows (replicas, or an edge's Helmsman, Mist and
# Caddy units) is read on every row, not only the first.
printf '%s\n' 'edge|ops@10.0.0.23|frameworks-helmsman.service' 'edge|ops@10.0.0.23|frameworks-mistserver.service' \
  'edge|ops@10.0.0.23|frameworks-caddy.service' >"$work/service-map.txt"
units_read() { # units_read <adapter function call...>: the units the reads reached
  : >"$work/ssh-calls"
  (
    export PATH="$work/bin:$PATH" FIXTURE_DIR="$work" STAGING_SERVICE_MAP_FILE="$work/service-map.txt"
    # shellcheck source=scripts/stack/staging-adapter.sh
    . "$here/staging-adapter.sh"
    "$@" >/dev/null
  )
  grep -oE 'frameworks-[a-z]+\.service' "$work/ssh-calls" | tr '\n' ' '
}
every_unit() { [ "$(units_read "$@")" = "frameworks-helmsman.service frameworks-mistserver.service frameworks-caddy.service " ]; }
expect "stack_logs reads every unit of a service" every_unit stack_logs --since 10m edge
expect "log_json_at reads every unit of a service" every_unit log_json_at 10m '.' edge

timestamp_is_raw() (
  export PATH="$work/bin:$PATH" FIXTURE_DIR="$work" STAGING_SERVICE_MAP_FILE="$work/service-map.txt"
  export FIXTURE_JOURNAL="$work/journal.jsonl"
  printf '%s\n' '{"__REALTIME_TIMESTAMP":"1793827948083859","MESSAGE":"{\"msg\":\"refused\"}"}' >"$FIXTURE_JOURNAL"
  . "$here/staging-adapter.sh"
  local stamp
  stamp=$(log_json_at 10m '.at' edge | sed -n 1p)
  [[ "$stamp" = 2026-11-04T*Z ]] && [[ "$stamp" != *'"'* ]]
)
expect "journal receive timestamps are raw strings usable in a later jq filter" timestamp_is_raw

partial_read_fails() (
  export PATH="$work/bin:$PATH" FIXTURE_DIR="$work" FIXTURE_FAIL_UNIT=frameworks-mistserver.service
  export FIXTURE_JOURNAL="$work/partial-journal" STAGING_SERVICE_MAP_FILE="$work/partial-service-map"
  printf 'ordinary event\n' >"$FIXTURE_JOURNAL"
  cp "$work/service-map.txt" "$STAGING_SERVICE_MAP_FILE"
  printf 'edge-b|ops@10.0.0.24|frameworks-helmsman.service\n' >>"$STAGING_SERVICE_MAP_FILE"
  . "$here/staging-adapter.sh"
  if logs_since 10m edge edge-b >"$work/partial-read"; then return 1; fi
  [ -s "$work/partial-read" ]
)
expect "a partial failed journal read stays failed when another service is readable" partial_read_fails

first_plays_use_declared_services() (
  FAIL=0
  export STACK_FOGHORN_A_SERVICES='cell-a-one cell-a-two' STACK_FOGHORN_B_SERVICES=cell-b
  FOGHORN_A_URLS=http://cell-a FOGHORN_B_URLS=http://cell-b
  EDGE_A_RTMP=rtmp://edge-a EDGE_B_RTMP=rtmp://edge-b
  need() { :; }
  utc_now() { printf '2026-11-04T21:00:00Z\n'; }
  seq() { printf '1\n'; }
  sleep() { :; }
  log() { :; }
  pass() { :; }
  fail() { FAIL=$((FAIL + 1)); }
  check() { shift; "$@" || fail; }
  eventually() { shift 2; "$@"; }
  finish() { [ "$FAIL" = 0 ]; }
  create_stream() { printf '%s\n' '{"id":"stream","streamId":"uuid","playbackId":"playback","streamKey":"fixture"}'; }
  delete_stream() { :; }
  internal_name_of() { printf 'internal\n'; }
  publish() { printf '9999999\n'; }
  media_at() { :; }
  location_at() { printf 'http://edge/media\n'; }
  segment_fetch() { printf '200 188\n'; }
  valid_services() {
    local service
    for service in "$@"; do
      case "$service" in cell-a-one | cell-a-two | cell-b) ;; *) return 1 ;; esac
    done
  }
  log_json() { shift 2; valid_services "$@"; }
  log_has() { shift 2; [ "$#" = 3 ] && valid_services "$@"; }
  sed '\|^\. .*../lib\.sh"$|d' "${STACK_FIRST_PLAY_FIXTURE_SOURCE:-$here/scenarios/23-cross-cell-first-plays.sh}" >"$work/first-play-scenario.sh"
  . "$work/first-play-scenario.sh"
)
expect "first cross-cell plays read the declared cells' logs" first_plays_use_declared_services

if [ "$failures" -gt 0 ]; then
  echo "staging adapter checks: $failures failed"
  exit 1
fi
echo "staging adapter checks: ok"

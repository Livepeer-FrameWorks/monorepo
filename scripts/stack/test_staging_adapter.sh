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
# Stub: records the remote command line.
printf '%s\n' "$*" >>"$FIXTURE_DIR/ssh-calls"
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

if [ "$failures" -gt 0 ]; then
  echo "staging adapter checks: $failures failed"
  exit 1
fi
echo "staging adapter checks: ok"

#!/usr/bin/env bash
# Run the stack scenarios against an already deployed target. Endpoint and
# transport details come from the operator's private files; this runner never
# provisions or tears down the deployment.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
cd "$repo" || exit 1
export STACK_TARGET=staging STACK_REPO="$repo"
export STACK_ADAPTER_FILE=${STACK_ADAPTER_FILE:-$here/staging-adapter.sh}

for file in "${STACK_ENDPOINTS_FILE:-}" "${STACK_ADAPTER_FILE:-}"; do
  if [ -z "$file" ] || [ ! -f "$file" ]; then
    echo 'BLOCKED: staging needs a private STACK_ENDPOINTS_FILE and a readable STACK_ADAPTER_FILE' >&2
    exit 2
  fi
done
set -a
# shellcheck source=/dev/null
. "$STACK_ENDPOINTS_FILE"
# shellcheck source=/dev/null
. "$STACK_ADAPTER_FILE"
set +a
for required in BRIDGE_URL FOGHORN_A_URLS FOGHORN_B_URLS EDGE_A_RTMP EDGE_B_RTMP STACK_TENANT_ID STACK_API_TOKEN; do
  if [ -z "${!required:-}" ]; then
    echo "BLOCKED: staging endpoint file lacks $required" >&2
    exit 2
  fi
done
if ! declare -F stack_services >/dev/null || ! declare -F stack_logs >/dev/null; then
  echo 'BLOCKED: staging adapter must provide stack_services and stack_logs' >&2
  exit 2
fi
if ! stack_services >/dev/null; then
  echo 'BLOCKED: staging service log map is unavailable' >&2
  exit 2
fi
if [ "${STACK_MAIL_MODE:-mailpit}" = imap ]; then
  for required in STACK_TEST_EMAIL_BASE STACK_MAIL_IMAP_HOST STACK_MAIL_IMAP_USER STACK_MAIL_IMAP_PASSWORD STACK_SMTP_PORT; do
    if [ -z "${!required:-}" ]; then
      echo "BLOCKED: staging IMAP verification needs $required" >&2
      exit 2
    fi
  done
fi

if [ -z "${STACK_STATE_DIR:-}" ]; then
  STACK_STATE_DIR="$repo/.stack/staging-$(date -u +%Y%m%dT%H%M%SZ)-$$"
fi
mkdir -p "$STACK_STATE_DIR" || exit 1
chmod 700 "$STACK_STATE_DIR"
export STACK_STATE_DIR
printf 'staging evidence: %s\n' "$STACK_STATE_DIR"

repeat=${STACK_REPEAT:-1}
case "$repeat" in
  '' | *[!0-9]*) echo 'STACK_REPEAT must be an integer from 1 to 100' >&2; exit 1 ;;
esac
if [ "$repeat" -lt 1 ] || [ "$repeat" -gt 100 ]; then
  echo 'STACK_REPEAT must be an integer from 1 to 100' >&2
  exit 1
fi

scenarios=()
while IFS= read -r path; do
  [ -n "$path" ] && scenarios+=("$path")
done < <(STACK_SCENARIOS="${STACK_SCENARIOS:-default}" bash "$here/scenarios/list.sh")
[ "${#scenarios[@]}" -gt 0 ] || { echo 'no scenarios selected' >&2; exit 1; }
if printf '%s\n' "${scenarios[@]}" | grep -Eq '/(27|28)-'; then
  make build-bin-cli || exit 1
fi

run_since="$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"
failed=0 blocked=0 summary=()
for path in "${scenarios[@]}"; do
  name="$(basename "$path" .sh)"
  for iteration in $(seq 1 "$repeat"); do
    label="$name ($iteration/$repeat)"
    printf '\n==== %s ====\n' "$label"
    scenario_since="$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"
    bash "$path"
    rc=$?
    case "$rc" in
      0) summary+=("PASS     $label") ;;
      2) summary+=("BLOCKED  $label"); blocked=1 ;;
      *) summary+=("FAIL     $label (exit $rc)"); failed=1 ;;
    esac
    if bash "$here/logcheck.sh" "$scenario_since" "$name"; then
      summary+=("PASS     $label logs")
    else
      summary+=("FAIL     $label logs"); failed=1
    fi
  done
done
printf '\n==== logcheck ====\n'
if bash "$here/logcheck.sh" "$run_since"; then
  summary+=("PASS     logcheck")
else
  summary+=("FAIL     logcheck"); failed=1
fi
printf '\n==== staging summary ====\n'
printf '%s\n' "${summary[@]}"
[ "$failed" -ne 0 ] && exit 1
[ "$blocked" -ne 0 ] && exit 2
exit 0

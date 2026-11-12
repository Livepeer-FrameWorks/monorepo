#!/usr/bin/env bash
# stack-scenario: default
# staging: safe
# tenant: test
# Anonymous subscription denials terminate on both transports and both replicas.
. "$(dirname "$0")/../lib.sh"
need python3 timeout || finish
python3 -c 'import websockets' 2>/dev/null || { blocked "python websockets missing"; finish; }
REPO=${STACK_REPO:-/repo}
if [ -n "${BRIDGE_REPLICA_ADDRS:-}" ]; then
  read -r bridge_one bridge_two _ <<<"$BRIDGE_REPLICA_ADDRS"
  urls=("$BRIDGE_WS_URL#$bridge_one" "$BRIDGE_WS_URL#$bridge_two")
elif [ "${STACK_TARGET:-stack}" = staging ]; then
  urls=("$BRIDGE_WS_URL")
else
  urls=("$BRIDGE_WS_URL" "${BRIDGE_2_WS_URL:-ws://bridge-2:18000/graphql/ws}")
fi
check "Bridge terminal denials complete and leave the connection usable" \
  timeout 30 python3 "$REPO/scripts/stack/ws-terminal-denial.py" "${urls[@]}"
finish

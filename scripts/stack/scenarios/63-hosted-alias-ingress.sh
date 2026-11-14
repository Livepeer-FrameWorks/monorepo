#!/usr/bin/env bash
# stack-scenario: default
# stack-runner: host
# staging: unsafe, starts a disposable Docker TLS terminator on the host network
# Real Privateer publication and the shipped reload helper must produce a trusted
# tenant SNI handshake, preserve last-good TLS on failed reload, and withdraw it.
. "$(dirname "$0")/../lib.sh"
need docker make go timeout || finish
REPO=${STACK_REPO:-/repo}
WORK=$(mktemp -d "$REPO/.stack/hosted-alias-ingress.XXXXXX") || { fail 'create shared ingress fixture directory'; finish; }
trap 'rm -rf "$WORK"' EXIT
export TMPDIR="$WORK"
export GOMAXPROCS=2
check 'Privateer publishes, applies, rotates and withdraws real Docker ingress TLS' \
  timeout 600 make -C "$REPO" verify-hosted-alias-ingress
finish

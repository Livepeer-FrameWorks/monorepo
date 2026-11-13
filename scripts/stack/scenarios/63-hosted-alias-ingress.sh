#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, starts a disposable Docker TLS terminator on the runner network
# tenant: test
# Real Privateer publication and the shipped reload helper must produce a trusted
# tenant SNI handshake, preserve last-good TLS on failed reload, and withdraw it.
. "$(dirname "$0")/../lib.sh"
need docker make go timeout || finish
REPO=${STACK_REPO:-/repo}
source_repo=$(docker inspect "$HOSTNAME" --format '{{range .Mounts}}{{if eq .Destination "/repo"}}{{.Source}}{{end}}{{end}}')
[ -n "$source_repo" ] || { fail 'shared source mount missing for ingress fixture'; finish; }
WORK=$(mktemp -d "$REPO/.stack/hosted-alias-ingress.XXXXXX")
trap 'rm -rf "$WORK"' EXIT
export TMPDIR="$WORK"
export FRAMEWORKS_INGRESS_TEST_HOST_REPO="$source_repo"
export FRAMEWORKS_INGRESS_TEST_NETWORK_PARENT="$HOSTNAME"
export GOMAXPROCS=2
check 'Privateer publishes, applies, rotates and withdraws real Docker ingress TLS' \
  timeout 600 make -C "$REPO" verify-hosted-alias-ingress
finish

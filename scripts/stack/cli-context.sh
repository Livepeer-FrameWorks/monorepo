#!/usr/bin/env bash
# Configure the mounted CLI binary to talk to the disposable stack services.
# The platform token is resolved through the same manifest path as an operator
# invocation. Compose has already parsed the generated stack env file.
set -uo pipefail

: "${STACK_STATE_DIR:?}"
: "${STACK_SLOT:?}"
: "${STACK_REPO:=/repo}"
[ -n "${SERVICE_TOKEN:-}" ] || { echo 'stack CLI context: missing SERVICE_TOKEN' >&2; exit 2; }
[ -x "$STACK_REPO/bin/cli" ] || { echo 'stack CLI context: build with make build-bin-cli' >&2; exit 2; }
umask 077
printf 'SERVICE_TOKEN=%s\n' "$SERVICE_TOKEN" >"$STACK_STATE_DIR/cli-auth.env"

export XDG_CONFIG_HOME="$STACK_STATE_DIR/cli-xdg"
export XDG_DATA_HOME="$STACK_STATE_DIR/cli-xdg-data"
mkdir -p "$XDG_CONFIG_HOME/frameworks" "$XDG_DATA_HOME"
manifest_path=${STACK_CLI_MANIFEST:-$STACK_STATE_DIR/cli-manifest.yaml}
if [ -z "${STACK_CLI_MANIFEST:-}" ]; then
cat >"$manifest_path" <<EOF
version: v1
type: cluster
env_files:
  - cli-auth.env
EOF
fi
cat >"$XDG_CONFIG_HOME/frameworks/config.yaml" <<EOF
current: stack
contexts:
  stack:
    name: stack
    persona: platform
    access_mode: local
    endpoints:
      bridge_url: http://bridge:18000
      purser_grpc_addr: purser:19003
      allow_insecure: true
    gitops:
      source: manifest
      manifest_path: "$manifest_path"
EOF

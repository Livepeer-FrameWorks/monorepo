#!/usr/bin/env bash
# Tears down one stack slot: its containers, networks, volumes and build images. Other slots
# and the plain dev stack (a different compose project) are untouched, and a
# slot another checkout holds is refused rather than removed.
set -euo pipefail

# shellcheck source=scripts/stack/common.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
cd "$stack_repo_root"
stack_require_slot || exit 1

stack_compose down -v --remove-orphans --rmi local
# Compose excludes the explicitly tagged edge bundle from its local images.
# Let Docker preserve the image if a remaining container still references it.
if docker image inspect "$STACK_EDGE_IMAGE" >/dev/null 2>&1; then
  if ! docker image rm --no-prune "$STACK_EDGE_IMAGE" >/dev/null 2>&1; then
    echo "stack slot $STACK_SLOT edge image retained: Docker refused its removal" >&2
  fi
fi
rm -f "$STACK_STATE_DIR/ready"
echo "stack slot $STACK_SLOT ($COMPOSE_PROJECT_NAME) removed"

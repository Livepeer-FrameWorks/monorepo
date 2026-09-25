#!/usr/bin/env bash
# Tears down one stack slot: its containers, networks and volumes. Other slots
# and the plain dev stack (a different compose project) are untouched.
set -euo pipefail

# shellcheck source=scripts/stack/common.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
cd "$stack_repo_root"

stack_compose down -v --remove-orphans
rm -f "$STACK_STATE_DIR/ready"
echo "stack slot $STACK_SLOT ($COMPOSE_PROJECT_NAME) removed"

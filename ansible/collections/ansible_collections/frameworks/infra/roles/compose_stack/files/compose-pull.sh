#!/bin/sh
# compose-pull.sh <project-dir> [attempts] [policy]
#
# Pulls a compose stack's images before the stack is applied, retrying with
# backoff. Every OTHER-tier host pulls from the registry at the same moment, so
# one transient DNS or network failure must not fail the rollout; when every
# attempt fails, the last error is reported as "pull failed: <cause>" instead of
# surfacing later as an unhealthy stack. COMPOSE_PULL_BACKOFF_SECONDS sets the
# first delay (doubling per attempt).
set -u

project_dir=$1
attempts=${2:-3}
policy=${3:-policy}
delay=${COMPOSE_PULL_BACKOFF_SECONDS:-5}
attempt=1

case "$policy" in
  policy) set -- ;;
  missing|always) set -- --policy "$policy" ;;
  *) printf 'invalid compose pull policy: %s\n' "$policy" >&2; exit 2 ;;
esac

while :; do
  if output=$(docker compose --project-directory "$project_dir" pull --quiet "$@" 2>&1); then
    exit 0
  fi
  if [ "$attempt" -ge "$attempts" ]; then
    cause=$(printf '%s\n' "$output" | sed '/^[[:space:]]*$/d' | tail -n 1)
    # The verdict is the only stdout line; progress goes to stderr.
    printf 'pull failed: %s\n' "${cause:-docker compose pull exited non-zero}"
    exit 1
  fi
  printf 'pull attempt %s/%s failed; retrying in %ss: %s\n' "$attempt" "$attempts" "$delay" \
    "$(printf '%s\n' "$output" | sed '/^[[:space:]]*$/d' | tail -n 1)" >&2
  sleep "$delay"
  delay=$((delay * 2))
  attempt=$((attempt + 1))
done

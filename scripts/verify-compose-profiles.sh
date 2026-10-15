#!/usr/bin/env bash
# Validates docker-compose.yml for every COMPOSE_PROFILES preset the generated
# dev .env lists, and docker-compose.stack.yml over it for each profile set the
# production-shaped stack runs (scripts/stack/common.sh). The env file is
# generated from config/env/base.env and secrets.env.example into a temporary
# directory, so no local .env is needed. Every profile declared in either file
# must be part of at least one validated set, so a profile without a preset
# fails the gate instead of going unvalidated.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
env_file="$work/dev.env"
dev_compose="$repo_root/docker-compose.yml"
stack_compose="$repo_root/docker-compose.stack.yml"

(cd "$repo_root/scripts/env" && GOCACHE="$repo_root/scripts/env/.gocache" go run . \
  --secrets "$repo_root/config/env/secrets.env.example" --output "$env_file" >/dev/null)

presets=()
while IFS= read -r line; do
  presets+=("$line")
done < <(sed -n 's/^#[[:space:]]*COMPOSE_PROFILES=\([^[:space:]]*\).*/\1/p' "$env_file")
if [ "${#presets[@]}" -eq 0 ]; then
  echo "ERROR: no COMPOSE_PROFILES presets found in the generated env file" >&2
  exit 1
fi

# The stack's profile list, with its STACK_S3 choice expanded to both backends.
stack_profiles="$(sed -n 's/^export COMPOSE_PROFILES="\(.*\)"$/\1/p' "$repo_root/scripts/stack/common.sh")"
# shellcheck disable=SC2016 # matches the literal variable reference common.sh expands
case "$stack_profiles" in
  *'$STACK_S3'*) ;;
  *)
    echo "ERROR: scripts/stack/common.sh no longer exports COMPOSE_PROFILES with \$STACK_S3" >&2
    exit 1
    ;;
esac
stack_sets=()
for s3 in ceph seaweed; do
  stack_sets+=("${stack_profiles//\$STACK_S3/$s3}")
done

failed=0
# Comma-delimited profiles each kind of run validated successfully.
validated_dev=","
validated_stack=","
validate() { # <dev|stack> <profiles> <compose file>...
  local kind="$1" profiles="$2"
  shift 2
  local files=()
  for f in "$@"; do files+=(-f "$f"); done
  # Unset-variable warnings are expected from the example secrets; the output is
  # shown only when validation fails.
  if output="$(COMPOSE_PROFILES="$profiles" ENV_FILE="$env_file" \
    docker compose --project-directory "$repo_root" "${files[@]}" --env-file "$env_file" config -q 2>&1)"; then
    printf 'ok   %-5s COMPOSE_PROFILES=%s\n' "$kind" "${profiles:-<control plane only>}"
    if [ "$kind" = dev ]; then
      validated_dev="$validated_dev$profiles,"
    else
      validated_stack="$validated_stack$profiles,"
    fi
  else
    echo "$output" >&2
    printf 'FAIL %-5s COMPOSE_PROFILES=%s\n' "$kind" "${profiles:-<control plane only>}" >&2
    failed=1
  fi
}

for preset in "${presets[@]}"; do
  validate dev "$preset" "$dev_compose"
done
# The stack runs every service with its fixed container_name and host ports
# reset (scripts/stack/common.sh writes the same overlay per slot); without it
# the second-replica services collide with the first.
for set in "${stack_sets[@]}"; do
  reset="$work/reset.yml"
  if ! services="$(COMPOSE_PROFILES="$set" ENV_FILE="$env_file" docker compose --project-directory "$repo_root" \
    -f "$dev_compose" -f "$stack_compose" --env-file "$env_file" config --services 2>"$work/services.err")"; then
    cat "$work/services.err" >&2
    echo "FAIL stack COMPOSE_PROFILES=$set (services not listable)" >&2
    failed=1
    continue
  fi
  {
    echo "services:"
    while IFS= read -r svc; do
      [ -n "$svc" ] || continue
      printf '  %s:\n    container_name: !reset null\n    ports: !reset []\n' "$svc"
    done <<<"$services"
  } >"$reset"
  validate stack "$set" "$dev_compose" "$stack_compose" "$reset"
done

declared_profiles() { # <compose file>...
  local files=()
  for f in "$@"; do files+=(-f "$f"); done
  COMPOSE_PROFILES='' ENV_FILE="$env_file" docker compose --project-directory "$repo_root" \
    "${files[@]}" --env-file "$env_file" config --profiles 2>/dev/null
}
# A docker-compose.yml profile must be in a dev preset (the dev stack runs that
# file alone); a profile only docker-compose.stack.yml declares must be in a
# stack profile set.
dev_declared="$(declared_profiles "$dev_compose")" || {
  echo "ERROR: could not list the profiles docker-compose.yml declares" >&2
  exit 1
}
all_declared="$(declared_profiles "$dev_compose" "$stack_compose")" || {
  echo "ERROR: could not list the profiles docker-compose.stack.yml declares" >&2
  exit 1
}
while IFS= read -r profile; do
  [ -n "$profile" ] || continue
  if printf '%s\n' "$dev_declared" | grep -qxF "$profile"; then
    kind=dev covered="$validated_dev"
  else
    kind=stack covered="$validated_stack"
  fi
  case "$covered" in
    *",$profile,"*) ;;
    *)
      echo "FAIL profile '$profile' is declared but no validated $kind profile set includes it" >&2
      failed=1
      ;;
  esac
done <<<"$all_declared"
exit "$failed"

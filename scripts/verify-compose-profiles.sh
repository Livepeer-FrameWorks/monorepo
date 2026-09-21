#!/usr/bin/env bash
# Validates docker-compose.yml for every COMPOSE_PROFILES preset the generated
# dev .env lists. The env file is generated from config/env/base.env and
# secrets.env.example into a temporary directory, so no local .env is needed.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
env_file="$work/dev.env"

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

failed=0
for preset in "${presets[@]}"; do
  label="${preset:-<control plane only>}"
  # Unset-variable warnings are expected from the example secrets; the output is
  # shown only when validation fails.
  if output="$(COMPOSE_PROFILES="$preset" ENV_FILE="$env_file" \
    docker compose -f "$repo_root/docker-compose.yml" --env-file "$env_file" config -q 2>&1)"; then
    echo "ok   COMPOSE_PROFILES=$label"
  else
    echo "$output" >&2
    echo "FAIL COMPOSE_PROFILES=$label" >&2
    failed=1
  fi
done
exit "$failed"

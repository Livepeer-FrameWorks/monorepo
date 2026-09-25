#!/usr/bin/env bash
# Brings up one production-shaped stack slot (docker-compose.stack.yml on top of
# the two-cell dev stack) and leaves it ready for scenarios.
#
#   STACK_SLOT=N          slot to use (default 1); see scripts/stack/common.sh
#   MIST_SOURCE_DIR=<dir> build MistServer from this checkout (HEAD plus any
#                         uncommitted changes) instead of the pinned release
#
# Seeding follows scripts/verify-two-cell-media.sh: demo + two-cell fixtures,
# then the stack's own rows, then the Foghorn/Helmsman restarts that let fresh
# edges enroll and become routable.
set -euo pipefail

# shellcheck source=scripts/stack/common.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
cd "$stack_repo_root"

ts() { date -u +%H:%M:%S; }
log() { printf '\n[stack-%s %s] %s\n' "$STACK_SLOT" "$(ts)" "$*"; }
fail() { printf '\n[stack-%s %s] ERROR: %s\n' "$STACK_SLOT" "$(ts)" "$*" >&2; exit 1; }
wait_for() { # wait_for <seconds> <description> <command...>
  local deadline=$(($(date +%s) + $1)) what=$2
  shift 2
  until "$@"; do
    [ "$(date +%s)" -ge "$deadline" ] && fail "timed out waiting for $what"
    sleep 3
  done
}
psql_db() { stack_compose exec -T postgres psql -v ON_ERROR_STOP=1 -U "$1" -d "$1" "${@:2}"; }

# The stack runs only what the scenarios exercise. This list is explicit so a
# service added to docker-compose.yml never joins the stack unnoticed; add a
# service here when a scenario starts depending on it. Left out on purpose:
# webapps and docs (chartroom, foredeck, logbook, nginx), support and AI
# (skipper, deckhand, steward, listmonk, chatwoot, ollama), and billing or
# incident consumers no scenario asserts on (periscope-metering, lookout).
STACK_SERVICES=(
  postgres kafka kafka-init clickhouse
  quartermaster commodore purser bridge decklog signalman bosun chandler
  periscope-ingest periscope-query
  storage-init storage-init-b "s3-$STACK_S3"
  foghorn foghorn-2 foghorn-redis foghorn-b foghorn-b-2 foghorn-redis-b
  edge edge-b edge-proxy-a edge-proxy-b
  livepeer-orch-a livepeer-gateway-a livepeer-proxy-a
  livepeer-orch-b livepeer-gateway-b livepeer-proxy-b
  webhook-receiver mailpit stack-runner
)

# A slot's environment is generated once per slot state: fresh platform secrets
# from the CLI's generator (correct formats, nothing committed), a random
# Postgres password, and the ClickHouse passwords the compose ClickHouse service
# hardcodes for its dev users.
if [ ! -s "$ENV_FILE" ]; then
  log "generating the slot environment ($ENV_FILE)"
  secrets="$STACK_STATE_DIR/secrets.env"
  rm -f "$secrets"
  (cd cli && GOCACHE="$PWD/.gocache" go run . cluster secrets generate-shared --out "$secrets" >/dev/null) ||
    fail "could not generate platform secrets"
  # The dev Postgres init script creates every service role with
  # POSTGRES_PASSWORD, so the service DSN passwords must be that same value.
  pg_password="$(openssl rand -hex 16)"
  grep -vE '^DATABASE_(RUNTIME_)?PASSWORD=' "$secrets" >"$secrets.tmp" && mv "$secrets.tmp" "$secrets"
  {
    printf 'POSTGRES_PASSWORD=%s\n' "$pg_password"
    printf 'DATABASE_PASSWORD=%s\n' "$pg_password"
    printf 'DATABASE_RUNTIME_PASSWORD=%s\n' "$pg_password"
    # Edge A enrolls with the demo seed's bootstrap token (edge-b's is fixed in
    # the base compose); without it cell A never has a node.
    printf 'EDGE_ENROLLMENT_TOKEN=demo_bootstrap_token_for_local_development_testing_only\n'
    # Cross-cell placement and origin pull run over federation, which the dev
    # .env leaves off for cell A (cell B's compose service hardcodes it on).
    # Edges advertise in-network addresses: the other cell's Mist pulls DTSC
    # from the public URL's host, and the runner follows viewer redirects.
    printf 'EDGE_PUBLIC_URL=http://edge:8082\n'
    printf 'FEDERATION_ENABLED=true\n'
    printf 'FEDERATION_ALLOW_INSECURE_DEV=true\n'
    printf 'CLICKHOUSE_PASSWORD=frameworks_dev\n'
    printf 'CLICKHOUSE_READONLY_PASSWORD=readonly_dev\n'
    printf 'CLICKHOUSE_ANALYTICS_PASSWORD=analytics_dev\n'
  } >>"$secrets"
  (cd scripts/env && GOCACHE="$PWD/.gocache" go run . --secrets "$secrets" --output "$ENV_FILE") ||
    fail "could not generate the slot environment"
fi
command -v openssl >/dev/null || fail "openssl is required for the gateway certificates"
stack_ensure_gateway_certs

log "1/7 edge media stack (slot project $COMPOSE_PROJECT_NAME, subnet $STACK_SUBNET)"
if [ -n "${MIST_SOURCE_DIR:-}" ]; then
  mist_src="$(cd "$MIST_SOURCE_DIR" && pwd)"
  [ -f "$mist_src/meson.build" ] || fail "MIST_SOURCE_DIR=$MIST_SOURCE_DIR is not a MistServer checkout"
  revision="$(git -C "$mist_src" rev-parse HEAD)"
  [ -z "$(git -C "$mist_src" status --porcelain)" ] || revision="$revision-dirty"
  ctx="$STACK_STATE_DIR/mist-context"
  rm -rf "$ctx" && mkdir -p "$ctx/source"
  # Tracked files plus local modifications, with mtimes kept so the cached
  # build directory recompiles only what changed.
  (cd "$mist_src" && git ls-files -z --cached --others --exclude-standard | tar --null -T - -cf -) | tar -xf - -C "$ctx/source"
  cp scripts/stack/mist.Dockerfile "$ctx/Dockerfile"
  log "building MistServer $revision from $mist_src"
  DOCKER_BUILDKIT=1 docker build --target out --build-arg "MIST_SOURCE_REVISION=$revision" \
    --output "type=local,dest=$STACK_STATE_DIR/mist" "$ctx"
  export EDGE_DEV_MIST_TAR="$STACK_STATE_DIR/mist/mist.tar.gz"
fi
make --no-print-directory edge-dev-dist
stack_compose build edge

log "2/7 databases and control plane"
stack_compose up -d --build postgres foghorn-redis foghorn-redis-b quartermaster purser commodore bridge decklog storage-init "s3-$STACK_S3"
wait_for 180 "postgres" stack_compose exec -T postgres pg_isready -q
pg_user="$(sed -n 's/^POSTGRES_USER="\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' "$ENV_FILE" | head -1)"
stack_compose exec -T postgres psql -U "$pg_user" -d postgres -Atc "SELECT 1 FROM pg_database WHERE datname='foghorn_b'" | grep -q 1 ||
  fail "database foghorn_b is missing: the two-cell profile needs a fresh volume (scripts/stack/down.sh, then rerun)"
wait_for 180 "quartermaster schema" psql_db quartermaster -Atc "SELECT 1 FROM quartermaster.services LIMIT 1"

log "3/7 demo, two-cell and stack fixtures"
make --no-print-directory seed-demo-postgres >/dev/null
psql_db quartermaster -q < pkg/database/sql/seeds/demo/postgres/two-cell/quartermaster.sql >/dev/null
psql_db quartermaster -q < scripts/stack/seed.sql >/dev/null
live_processes="$(awk '/processes_live: &standard_live/ { getline; sub(/^[[:space:]]+/, ""); print; exit }' api_billing/internal/bootstrap/catalog/billing_tiers.yaml)"
[ -n "$live_processes" ] || fail "could not read standard_live from the billing tier catalog"
psql_db purser -q -v live="$live_processes" -v window="${STACK_DVR_WINDOW_SECONDS:-120}" < scripts/stack/seed-purser.sql >/dev/null
stack_compose up -d --no-deps storage-init storage-init-b >/dev/null
# Both cells' buckets exist before any Foghorn mints a presigned URL into them.
stack_compose build stack-runner >/dev/null
stack_compose run --rm --no-deps -T --entrypoint python3 stack-runner /repo/scripts/stack/s3-init.py \
  fw-stack-central-primary fw-stack-us-primary || fail "could not create the per-cell S3 buckets ($STACK_S3)"

log "4/7 full stack"
stack_compose up -d --build --remove-orphans "${STACK_SERVICES[@]}"
# Purser caches tier config, Commodore caches resolved processes; both must see
# the stack overrides before the first stream.
stack_compose restart purser commodore >/dev/null

log "5/7 edge enrollment"
# A freshly enrolled edge stays unroutable until Foghorn probes its HTTPS domain
# (not possible in dev) or the node reconnects with its resolved fingerprint; the
# Helmsman restart is that reconnect. Give first-boot enrollment time to land.
sleep 20
stack_compose restart foghorn foghorn-2 foghorn-b foghorn-b-2 >/dev/null
sleep 15
for edge in edge edge-b; do
  stack_compose exec -T "$edge" /command/s6-svc -r /run/service/helmsman >/dev/null
done

log "6/7 health"
unhealthy() {
  stack_compose ps --format '{{.Service}} {{.State}} {{.Health}}' | awk '$2 != "running" || ($3 != "" && $3 != "healthy") { print $1 " " $2 " " $3 }' |
    grep -vE '^(kafka-init|storage-init|storage-init-b) exited' || true
}
settled() { [ -z "$(unhealthy)" ]; }
deadline=$(($(date +%s) + 300))
until settled; do
  if [ "$(date +%s)" -ge "$deadline" ]; then
    printf '%s\n' "$(unhealthy)" >&2
    fail "services did not become healthy"
  fi
  sleep 5
done

log "6b/7 edges connected and demo session"
# Every scenario needs both edges enrolled and reporting to their cell; a
# missing enrollment otherwise surfaces as every placement refusing.
edge_reporting() {
  stack_compose logs --since 30s "$1" 2>/dev/null | grep -q "Sent node lifecycle update to Foghorn"
}
for edge in edge edge-b; do
  wait_for 120 "$edge enrolled and reporting to its Foghorn" edge_reporting "$edge"
done
# The demo user's seeded password is not published; the stack sets its own so
# scenarios can hold a real user session for developer actions (webhook
# endpoints, API tokens) the scoped demo API token must not have.
psql_db commodore -q -c "UPDATE commodore.users SET password_hash = crypt('$STACK_DEMO_PASSWORD', gen_salt('bf', 10)) WHERE email = '$STACK_DEMO_EMAIL'" >/dev/null ||
  fail "could not set the demo user's stack password"

log "7/7 Livepeer gateways discoverable"
# Foghorn fans out only to healthy, freshly probed gateway instances; the
# Quartermaster poller probes every 30 s.
gateways_healthy() {
  [ "$(psql_db quartermaster -Atc "SELECT count(*) FROM quartermaster.service_instances WHERE instance_id IN ('stack-livepeer-gateway-a','stack-livepeer-gateway-b') AND health_status = 'healthy' AND last_health_check > NOW() - INTERVAL '120 seconds'")" = 2 ]
}
wait_for 180 "both Livepeer gateways healthy in Quartermaster" gateways_healthy

date -u +%Y-%m-%dT%H:%M:%SZ >"$STACK_STATE_DIR/ready"
log "slot $STACK_SLOT ready (project $COMPOSE_PROJECT_NAME)"

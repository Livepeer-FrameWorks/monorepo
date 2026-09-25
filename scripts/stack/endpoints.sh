#!/usr/bin/env bash
# In-network endpoints of the stack, for scenarios running in stack-runner.
# Scenarios source this file and nothing else for addresses: every URL is a
# compose service name, so it is identical in every slot.

# Control plane
export BRIDGE_URL="http://bridge:18000"
export BRIDGE_GRAPHQL_URL="$BRIDGE_URL/graphql"
export BRIDGE_WS_URL="ws://bridge:18000/graphql/ws"

# Foghorn replicas per cell (public HTTP). The first entry is the one the
# gateway auth webhook uses; scenarios must resolve through every replica.
export FOGHORN_A_URLS="http://foghorn:18008 http://foghorn-2:18018"
export FOGHORN_B_URLS="http://foghorn-b:18008 http://foghorn-b-2:18008"
export FOGHORN_A_URL="http://foghorn:18008"
export FOGHORN_B_URL="http://foghorn-b:18008"
# Internal HTTP (service-token routes) per replica, same order as above.
export FOGHORN_A_INTERNAL_URLS="http://foghorn:18027 http://foghorn-2:18027"
export FOGHORN_B_INTERNAL_URLS="http://foghorn-b:18027 http://foghorn-b-2:18027"

# Edges (the bundle container: Caddy, Mist, Helmsman) and their cells
export EDGE_A_HOST="edge"
export EDGE_B_HOST="edge-b"
export EDGE_A_RTMP="rtmp://edge:1935/live"
export EDGE_B_RTMP="rtmp://edge-b:1935/live"
export CELL_A_CLUSTER="demo-media"
export CELL_B_CLUSTER="demo-selfhosted"

# Livepeer (offchain), per cell
export LIVEPEER_GATEWAY_A_FQDN="livepeer-gateway.stack-lp-a.infra.frameworks.network"
export LIVEPEER_GATEWAY_B_FQDN="livepeer-gateway.stack-lp-b.infra.frameworks.network"
export LIVEPEER_GATEWAY_A_SERVICE="livepeer-gateway-a"
export LIVEPEER_GATEWAY_B_SERVICE="livepeer-gateway-b"

# Test doubles
export WEBHOOK_RECEIVER_URL="http://webhook-receiver:18777"
export WEBHOOK_TARGET_URL="$WEBHOOK_RECEIVER_URL/hooks"
export MAILPIT_API_URL="http://mailpit:8025/api/v1"

# Datastores (passwords come from the stack-runner environment)
export PG_HOST="postgres"
pg_dsn() { printf 'postgresql://%s:%s@postgres:5432/%s?sslmode=disable' "$1" "${POSTGRES_PASSWORD:-}" "$1"; }
export CLICKHOUSE_URL="http://clickhouse:8123/?user=${CLICKHOUSE_USER:-}&password=${CLICKHOUSE_PASSWORD:-}"

# Stack fixture (scripts/stack/seed-purser.sql sets the demo tier's DVR window)
export STACK_DVR_WINDOW_SECONDS="${STACK_DVR_WINDOW_SECONDS:-120}"
export STACK_EDGE_A_SERVICE="edge"
export STACK_EDGE_B_SERVICE="edge-b"
export STACK_REPO="/repo"
# URL imports go through Helmsman's tenant-source client, which refuses private
# addresses, so the import fixtures are public (the same ones staging uses).
export STACK_IMPORT_VOD_URL="${STACK_IMPORT_VOD_URL:-https://test-videos.co.uk/vids/bigbuckbunny/mp4/h264/720/Big_Buck_Bunny_720_10s_5MB.mp4}"
export STACK_MISSING_VOD_URL="${STACK_MISSING_VOD_URL:-https://test-videos.co.uk/vids/does-not-exist-404.mp4}"

# Demo fixture (pkg/database/sql/seeds/demo)
export DEMO_TENANT_ID="5eed517e-ba5e-da7a-517e-ba5eda7a0001"
export DEMO_API_TOKEN="fw_0000000000000000000000000000000000000000000000000000000000demo01"
export DEMO_STREAM_KEY="sk_demo_live_stream_primary_key"
export DEMO_PLAYBACK_ID="pb_demo_live_001"

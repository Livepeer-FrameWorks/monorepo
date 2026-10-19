set +e
PORT=5433
USER_NAME='yugabyte'
PASSWORD=''
BINARY='ysqlsh'
DB='quartermaster'
PEER=0
fw_yb_bin() {
  if [ -e /opt/yugabyte/current ] || [ -L /opt/yugabyte/current ]; then
    for dir in /opt/yugabyte/current/postgres/bin /opt/yugabyte/current/bin; do
      if [ -x "$dir/$1" ]; then readlink -f "$dir/$1"; return 0; fi
    done
    echo "Selected YugabyteDB release lacks $1" >&2
    return 1
  fi
  for dir in /opt/yugabyte/postgres/bin /opt/yugabyte/bin /home/yugabyte/postgres/bin /home/yugabyte/bin; do
    if [ -x "$dir/$1" ]; then echo "$dir/$1"; return 0; fi
  done
  command -v "$1"
}
fw_ysql_host() {
  if command -v ss >/dev/null 2>&1; then
    fw_ysql_addr="$(ss -Hltn "sport = :$1" 2>/dev/null | awk 'NR == 1 {print $4}' | sed -e 's/:[0-9]*$//' -e 's/^\[//' -e 's/\]$//')"
  else
    fw_ysql_addr="$(sed -n 's/^--pgsql_proxy_bind_address=\(.*\):[0-9]*$/\1/p' /opt/yugabyte/conf/tserver.conf 2>/dev/null)"
  fi
  case "$fw_ysql_addr" in
    "" | "0.0.0.0" | "*" | "::") echo 127.0.0.1 ;;
    *) echo "$fw_ysql_addr" ;;
  esac
}
resolve_sql_binary() {
  if [ "$BINARY" = "ysqlsh" ]; then
    fw_yb_bin ysqlsh
    return $?
  fi
  command -v "$BINARY"
}
SQL_BIN="$(resolve_sql_binary)"
if [ -z "$SQL_BIN" ]; then
  echo "$BINARY not found on the database host"
  exit 127
fi
run_sql() {
  if [ "$PEER" = "1" ] && command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
    sudo -u "$USER_NAME" "$SQL_BIN" -X -q -t -A -v ON_ERROR_STOP=1 -p "$PORT" -d "$DB" -P pager=off -f - 2>&1
  elif [ "$PEER" = "1" ]; then
    "$SQL_BIN" -X -q -t -A -v ON_ERROR_STOP=1 -p "$PORT" -d "$DB" -P pager=off -f - 2>&1
  else
    PGPASSWORD="$PASSWORD" "$SQL_BIN" -X -q -t -A -v ON_ERROR_STOP=1 -h "$(fw_ysql_host "$PORT")" -p "$PORT" -U "$USER_NAME" -d "$DB" -P pager=off -f - 2>&1
  fi
}
READY="$(printf '%s\n' 'BEGIN READ ONLY;
SELECT
  EXISTS (SELECT 1 FROM information_schema.columns
          WHERE table_schema = '\''quartermaster'\'' AND table_name = '\''node_fingerprints'\''
            AND column_name = '\''node_identity_public_key_ed25519'\'')
  AND EXISTS (SELECT 1 FROM information_schema.columns
              WHERE table_schema = '\''quartermaster'\'' AND table_name = '\''tenants'\''
                AND column_name = '\''billing_entitlements_observed_at'\'')
  AND EXISTS (SELECT 1 FROM information_schema.tables
              WHERE table_schema = '\''quartermaster'\'' AND table_name = '\''billing_entitlement_handoffs'\'');
COMMIT;' | run_sql)"
status=$?
if [ "$status" -ne 0 ]; then printf '%s\n' "$READY"; exit "$status"; fi
READY="$(printf '%s' "$READY" | tr -d '[:space:]')"
if [ "$READY" != "t" ]; then echo '{"expand_applied":false}'; exit 0; fi
printf '%s\n' 'BEGIN READ ONLY;
SET LOCAL statement_timeout = '\''20s'\'';
WITH managed AS (
SELECT node.node_id,
       node.cluster_id,
       fingerprint.tenant_id::text AS tenant_id,
       node.enrollment_origin,
       GREATEST(node.last_heartbeat, fingerprint.last_seen) AS last_seen
FROM quartermaster.node_fingerprints fingerprint
JOIN quartermaster.infrastructure_nodes node ON node.node_id = fingerprint.node_id
JOIN quartermaster.infrastructure_clusters cluster ON cluster.cluster_id = node.cluster_id
WHERE node.status = '\''active'\''
  AND cluster.is_active = true
  AND node.enrollment_origin IN ('\''gitops_seed'\'', '\''adopted_local'\'')
  AND (
      fingerprint.node_identity_public_key_ed25519 IS NULL
      OR octet_length(fingerprint.node_identity_public_key_ed25519) <> 32
  )
ORDER BY node.node_id),
customer AS (
SELECT node.node_id,
       node.cluster_id,
       fingerprint.tenant_id::text AS tenant_id,
       node.enrollment_origin,
       GREATEST(node.last_heartbeat, fingerprint.last_seen) AS last_seen
FROM quartermaster.node_fingerprints fingerprint
JOIN quartermaster.infrastructure_nodes node ON node.node_id = fingerprint.node_id
JOIN quartermaster.infrastructure_clusters cluster ON cluster.cluster_id = node.cluster_id
WHERE node.status = '\''active'\''
  AND cluster.is_active = true
  AND node.enrollment_origin = '\''runtime_enrolled'\''
  AND (
      fingerprint.node_identity_public_key_ed25519 IS NULL
      OR octet_length(fingerprint.node_identity_public_key_ed25519) <> 32
  )
ORDER BY node.node_id),
dns AS (
SELECT tenant.id::text AS tenant_id,
       tenant.name,
       tenant.subdomain,
       tenant.custom_domain,
       tenant.custom_subdomain_enabled,
       tenant.custom_domain_enabled
FROM quartermaster.tenants tenant
WHERE tenant.billing_entitlements_observed_at = '\''epoch'\''::timestamptz
  AND (tenant.custom_subdomain_enabled OR tenant.custom_domain_enabled)
ORDER BY tenant.id)
SELECT json_build_object(
  '\''expand_applied'\'', true,
  '\''managed_keyless_total'\'', (SELECT COUNT(*) FROM managed),
  '\''managed_keyless'\'', COALESCE((SELECT json_agg(r ORDER BY r.node_id) FROM (SELECT * FROM managed ORDER BY node_id LIMIT 50) r), '\''[]'\''::json),
  '\''customer_keyless_total'\'', (SELECT COUNT(*) FROM customer),
  '\''customer_keyless'\'', COALESCE((SELECT json_agg(r ORDER BY r.node_id) FROM (SELECT * FROM customer ORDER BY node_id LIMIT 50) r), '\''[]'\''::json),
  '\''dns_grants_total'\'', (SELECT COUNT(*) FROM dns),
  '\''dns_grants'\'', COALESCE((SELECT json_agg(r ORDER BY r.tenant_id) FROM (SELECT * FROM dns ORDER BY tenant_id LIMIT 50) r), '\''[]'\''::json),
  '\''dns_handoff_present'\'', EXISTS (
    SELECT 1 FROM quartermaster.billing_entitlement_handoffs
    WHERE handoff_key = '\''purser_dns_entitlements_v0_3_0'\''
  )
)::jsonb;
COMMIT;' | run_sql

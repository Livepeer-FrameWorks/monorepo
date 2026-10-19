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
set +e
PORT=5433
USER_NAME='yugabyte'
PASSWORD=''
BINARY='ysqlsh'
PEER=0
DATABASES='yugabyte'
PLACEMENT_SQL='
SELECT n.nspname || '\''.'\'' || c.relname,
       c.relkind,
       COALESCE(tn.nspname || '\''.'\'' || tc.relname, n.nspname || '\''.'\'' || c.relname),
       p.is_colocated,
       p.num_tablets
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_index i ON i.indexrelid = c.oid
LEFT JOIN pg_class tc ON tc.oid = i.indrelid
LEFT JOIN pg_namespace tn ON tn.oid = tc.relnamespace
LEFT JOIN pg_am am ON am.oid = c.relam
CROSS JOIN LATERAL yb_table_properties(c.oid) p
WHERE c.relkind IN ('\''r'\'', '\''p'\'', '\''i'\'', '\''I'\'')
  AND n.nspname NOT IN ('\''pg_catalog'\'', '\''information_schema'\'')
  AND n.nspname NOT LIKE '\''pg_toast%'\''
  AND NOT COALESCE(i.indisprimary, false)
  AND COALESCE(am.amname, '\'''\'') NOT IN ('\''ybhnsw'\'', '\''hnsw'\'')
ORDER BY 1'
resolve_sql_binary() {
  if [ "$BINARY" = "ysqlsh" ]; then
    fw_yb_bin ysqlsh
    return $?
  fi
  if command -v "$BINARY" >/dev/null 2>&1; then
    command -v "$BINARY"
    return 0
  fi
  return 1
}
SQL_BIN="$(resolve_sql_binary)"
REQUIRED_FAILED=0
REQUIRED_OK=0
run_sql() {
  db="$1"
  sql="$2"
  if [ -z "$SQL_BIN" ]; then
    echo "$BINARY not found"
    return 127
  fi
  if [ "$PEER" = "1" ]; then
    if command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
      sudo -u "$USER_NAME" "$SQL_BIN" -X -p "$PORT" -d "$db" -A -F '	' -P pager=off -c "$sql" 2>&1
    else
      "$SQL_BIN" -X -p "$PORT" -d "$db" -A -F '	' -P pager=off -c "$sql" 2>&1
    fi
  else
    PGPASSWORD="$PASSWORD" "$SQL_BIN" -X -h "$(fw_ysql_host "$PORT")" -p "$PORT" -U "$USER_NAME" -d "$db" -A -F '	' -P pager=off -c "$sql" 2>&1
  fi
}
run_section() {
  title="$1"
  db="$2"
  sql="$3"
  required="$4"
  echo
  echo "== ${db}: ${title} =="
  if run_sql "$db" "$sql"; then
    if [ "$required" = "1" ]; then
      REQUIRED_OK=1
    fi
  else
    echo "(query failed)"
    if [ "$required" = "1" ]; then
      REQUIRED_FAILED=1
    fi
  fi
}
echo "== postgres target =="
echo "host='molecule'"
echo "port=$PORT"
echo "user=$USER_NAME"
echo "binary=${SQL_BIN:-$BINARY}"
for db in $DATABASES; do
  run_section "version" "$db" "SELECT current_database() AS database, version() AS version;" 1
  run_section "largest user tables" "$db" "SELECT schemaname, relname, n_live_tup, pg_size_pretty(pg_total_relation_size(relid)) AS total_size FROM pg_stat_user_tables ORDER BY pg_total_relation_size(relid) DESC LIMIT 80;" 1
  run_section "migration ledger" "$db" "SELECT version, phase, seq, checksum FROM _migrations ORDER BY version, phase, seq LIMIT 200;" 0
  run_section "recent data migrations" "$db" "SELECT id, status, updated_at FROM data_migrations ORDER BY updated_at DESC LIMIT 80;" 0
  if [ "$BINARY" = "ysqlsh" ]; then
    run_section "yugabyte layout: database colocation" "$db" "SELECT yb_is_database_colocated() AS colocated;" 0
    run_section "yugabyte layout: relation placement" "$db" "$PLACEMENT_SQL" 0
    run_section "yugabyte layout: tablets hosted by this node" "$db" "SELECT count(DISTINCT tablet_id) AS tablets, count(*) AS tablet_peers FROM yb_local_tablets WHERE namespace_name = current_database() AND state = 'TABLET_DATA_READY';" 0
  fi
done
if [ "$REQUIRED_OK" != "1" ] || [ "$REQUIRED_FAILED" = "1" ]; then
  exit 1
fi
exit 0

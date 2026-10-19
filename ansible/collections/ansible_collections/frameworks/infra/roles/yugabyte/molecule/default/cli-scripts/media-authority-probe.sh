set +e
PORT=5433
USER_NAME='yugabyte'
PASSWORD=''
BINARY='ysqlsh'
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
run_probe() {
  db="$1"
  echo
  echo "######## database ${db}"
  if [ "$PEER" = "1" ] && command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
    sudo -u "$USER_NAME" "$SQL_BIN" -X -q -v ON_ERROR_STOP=1 -p "$PORT" -d "$db" -P pager=off -A -F ' | ' -f - 2>&1
  elif [ "$PEER" = "1" ]; then
    "$SQL_BIN" -X -q -v ON_ERROR_STOP=1 -p "$PORT" -d "$db" -P pager=off -A -F ' | ' -f - 2>&1
  else
    PGPASSWORD="$PASSWORD" "$SQL_BIN" -X -q -v ON_ERROR_STOP=1 -h "$(fw_ysql_host "$PORT")" -p "$PORT" -U "$USER_NAME" -d "$db" -P pager=off -A -F ' | ' -f - 2>&1
  fi
}
FAILED=0
probe_db='yugabyte'
printf '%s\n' 'SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY;
SELECT 1;' | run_probe "$probe_db"
status=$?
if [ "$status" -ne 0 ]; then echo "!! probe of database ${probe_db} failed (exit $status)"; FAILED=1; fi
exit $FAILED

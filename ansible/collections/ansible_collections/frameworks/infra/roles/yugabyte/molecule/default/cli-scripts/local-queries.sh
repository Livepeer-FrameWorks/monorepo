fw_err="$(mktemp)" || exit 1
trap 'rm -f "$fw_err"' EXIT
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
yb_client="$(fw_yb_bin ysqlsh)" || exit 1
ysql_host="$(fw_ysql_host 5433)"
fw_query() {
  printf '%s%s\n' '@@fw-query ' "$1"
  "$yb_client" -X -h "$ysql_host" -p 5433 -U 'yugabyte' -d "$2" -v ON_ERROR_STOP=1 -tA -f - 2>"$fw_err"
  fw_rc=$?
  printf '\n%s%s %s ' '@@fw-done ' "$1" "$fw_rc"
  tr '\n' ' ' <"$fw_err"
  printf '\n'
}
fw_query 0 'yugabyte' <<'FW_SQL_EOF'
SELECT 1;
FW_SQL_EOF
exit 0

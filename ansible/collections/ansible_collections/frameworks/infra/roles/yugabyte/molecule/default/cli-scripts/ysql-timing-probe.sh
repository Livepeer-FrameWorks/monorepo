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
start=$(date +%s%N)
"$yb_client" -X -h "$(fw_ysql_host 5433)" -p 5433 -U 'yugabyte' -d 'yugabyte' -v ON_ERROR_STOP=1 -tA -c 'SELECT 1' >/dev/null || exit 2
end=$(date +%s%N)
echo "ysql_elapsed_us=$(( (end - start) / 1000 ))"
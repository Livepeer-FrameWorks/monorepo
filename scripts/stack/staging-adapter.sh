#!/usr/bin/env bash
# Native staging transport for the same scenario scripts. The operator's
# private map has one pipe-separated row per service replica:
#   scenario-service|ssh-host|systemd-unit
# For example: foghorn|fw-stg-eu-1|frameworks-foghorn.service

staging_targets() {
  local service=$1 row
  [ -s "${STAGING_SERVICE_MAP_FILE:-}" ] || return 1
  row=$(awk -F '|' -v name="$service" '$1 == name { print $2 "|" $3 }' "$STAGING_SERVICE_MAP_FILE")
  [ -n "$row" ] || { echo "staging service has no host mapping: $service" >&2; return 1; }
  printf '%s\n' "$row"
}

staging_target() {
  local targets
  targets=$(staging_targets "$1") || return 1
  printf '%s\n' "${targets%%$'\n'*}"
}

stack_services() {
  [ -s "${STAGING_SERVICE_MAP_FILE:-}" ] || return 1
  awk -F '|' 'NF == 3 && $1 !~ /^#/ && !seen[$1]++ { print $1 }' "$STAGING_SERVICE_MAP_FILE"
}

stack_logs() {
  local since='' service targets target host unit
  if [ "${1:-}" = --since ]; then since=$2; shift 2; fi
  service=${1:-}
  targets=$(staging_targets "$service") || return 1
  while IFS= read -r target; do
    host=${target%%|*}; unit=${target#*|}
    if [ -n "$since" ]; then
      ssh -o BatchMode=yes "$host" journalctl --no-pager --output=cat --unit "$unit" --since "$since" || return 1
    else
      ssh -o BatchMode=yes "$host" journalctl --no-pager --output=cat --unit "$unit" || return 1
    fi
  done <<<"$targets"
}

logs_since() {
  local since=$1 service
  shift
  for service in "$@"; do stack_logs --since "$since" "$service"; done
}

stack_ctl() {
  local action=$1 targets target host unit
  targets=$(staging_targets "$2") || return 1
  while IFS= read -r target; do
    host=${target%%|*}; unit=${target#*|}
    case "$action" in
      start | stop | restart)
        ssh -o BatchMode=yes "$host" sudo -n systemctl "$action" "$unit" || return 1
        ;;
      kill)
        ssh -o BatchMode=yes "$host" sudo -n systemctl kill --signal=SIGKILL "$unit" || return 1
        ssh -o BatchMode=yes "$host" sudo -n systemctl stop "$unit" || return 1
        ;;
      pause | unpause)
        local signal=STOP
        [ "$action" = unpause ] && signal=CONT
        ssh -o BatchMode=yes "$host" "pid=\$(systemctl show --property=MainPID --value '$unit'); test \"\$pid\" -gt 1 && sudo -n kill -$signal \"\$pid\"" || return 1
        ;;
      *) echo "unsupported staging service action: $action" >&2; return 2 ;;
    esac
  done <<<"$targets"
}

stack_exec() {
  local target host command
  target=$(staging_target "$1") || return 1
  host=${target%%|*}
  shift
  printf -v command '%q ' "$@"
  ssh -o BatchMode=yes "$host" "bash -lc $(printf '%q' "$command")"
}

stack_started_at() {
  local target host unit
  target=$(staging_target "$1") || return 1
  host=${target%%|*}; unit=${target#*|}
  ssh -o BatchMode=yes "$host" systemctl show --property=ExecMainStartTimestampMonotonic --value "$unit"
}

stack_service_exists() { staging_target "$1" >/dev/null; }
stack_running() {
  local target host unit
  target=$(staging_target "$1") || return 1
  host=${target%%|*}; unit=${target#*|}
  ssh -o BatchMode=yes "$host" systemctl is-active --quiet "$unit"
}
stack_healthy() { stack_running "$1"; }
stack_replica_count() {
  stack_services | grep -Ec "^$1(-[0-9]+)?$" || true
}

netfault_start() {
  local target host
  target=$(staging_target "$1") || return 1
  host=${target%%|*}
  ssh -o BatchMode=yes "$host" sudo -n sh -c 'iptables -N FW_FAULT_IN && iptables -N FW_FAULT_OUT && iptables -I INPUT -j FW_FAULT_IN && iptables -I OUTPUT -j FW_FAULT_OUT'
}
netfault() {
  local target host
  target=$(staging_target "$1") || return 1
  host=${target%%|*}
  shift
  ssh -o BatchMode=yes "$host" sudo -n "$@"
}
netfault_clear() { netfault "$1" sh -c 'iptables -F FW_FAULT_IN && iptables -F FW_FAULT_OUT'; }
netfault_stop() {
  local target host
  target=$(staging_target "$1") || return 1
  host=${target%%|*}
  ssh -o BatchMode=yes "$host" sudo -n sh -c 'iptables -D INPUT -j FW_FAULT_IN; iptables -D OUTPUT -j FW_FAULT_OUT; iptables -F FW_FAULT_IN; iptables -F FW_FAULT_OUT; iptables -X FW_FAULT_IN; iptables -X FW_FAULT_OUT' >/dev/null 2>&1
}

log_json_at() {
  local since=$1 filter=$2 service targets target host unit
  shift 2
  for service in "$@"; do
    targets=$(staging_targets "$service") || return 1
    while IFS= read -r target; do
      host=${target%%|*}; unit=${target#*|}
      ssh -o BatchMode=yes "$host" journalctl --no-pager --output=json --unit "$unit" --since "$since" || return 1
    done <<<"$targets"
  done | jq -Rrc 'fromjson? | .__REALTIME_TIMESTAMP as $stamp | .MESSAGE | fromjson? | objects |
    ($stamp | tonumber) as $micros |
    .at = (($micros / 1000000 | floor | strftime("%Y-%m-%dT%H:%M:%S")) + "." +
      (("000000" + (($micros % 1000000) | tostring))[-6:]) + "Z")' |
    jq -c "$filter"
}

mail_fault_start() {
  local port=${STACK_SMTP_PORT:-} target host tool
  case "$port" in '' | *[!0-9]*) echo 'staging SMTP fault needs STACK_SMTP_PORT' >&2; return 2 ;; esac
  target=$(staging_target commodore) || return 1
  host=${target%%|*}
  for tool in iptables ip6tables; do
    if [ "$tool" = ip6tables ] && ! ssh -o BatchMode=yes "$host" command -v ip6tables >/dev/null 2>&1; then continue; fi
    if ! ssh -o BatchMode=yes "$host" sudo -n "$tool" -N FW_SMOKE_SMTP ||
       ! ssh -o BatchMode=yes "$host" sudo -n "$tool" -A FW_SMOKE_SMTP -j REJECT ||
       ! ssh -o BatchMode=yes "$host" sudo -n "$tool" -I OUTPUT -p tcp --dport "$port" -j FW_SMOKE_SMTP; then
      mail_fault_stop
      return 1
    fi
  done
}

mail_fault_stop() {
  local port=${STACK_SMTP_PORT:-} target host tool
  target=$(staging_target commodore) || return 1
  host=${target%%|*}
  for tool in iptables ip6tables; do
    ssh -o BatchMode=yes "$host" sudo -n "$tool" -D OUTPUT -p tcp --dport "$port" -j FW_SMOKE_SMTP >/dev/null 2>&1 || true
    ssh -o BatchMode=yes "$host" sudo -n "$tool" -F FW_SMOKE_SMTP >/dev/null 2>&1 || true
    ssh -o BatchMode=yes "$host" sudo -n "$tool" -X FW_SMOKE_SMTP >/dev/null 2>&1 || true
  done
}

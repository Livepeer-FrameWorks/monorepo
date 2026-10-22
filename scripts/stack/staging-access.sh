#!/usr/bin/env bash
# Read-only access to a deployed target's databases and edge storage over SSH,
# from the rows `frameworks cluster stack-target` wrote. Credentials are read
# on the host that holds them and piped to the query; they are never printed
# or written on the runner.
#
#   staging-access.sh pg <db>                          SQL on stdin, psql -At rows
#   staging-access.sh ch                               SQL on stdin, ClickHouse body
#   staging-access.sh edge-ls <service> <dir> [depth]  paths under an edge's dir
set -uo pipefail

ssh_run() { ssh -o BatchMode=yes -o ConnectTimeout=10 "$@"; }

map_row() { # map_row <file> <awk condition>: the first matching row
  [ -s "$1" ] || { echo "staging access: map $1 is missing" >&2; return 2; }
  awk -F '|' "$2 { print; exit }" "$1"
}

case "${1:-}" in
  pg)
    db=${2:?usage: staging-access.sh pg <db>}
    row=$(map_row "${STACK_ACCESS_MAP_FILE:-}" "\$1 == \"pg\" && \$2 == \"$db\"") || exit 2
    [ -n "$row" ] || { echo "staging access: no database $db in the access map" >&2; exit 2; }
    IFS='|' read -r _ _ holder envfile client <<<"$row"
    # load_balance is a smart-driver option psql does not know.
    dsn=$(ssh_run -n "$holder" "sudo -n grep -E '^DATABASE_URL=' '$envfile' | cut -d= -f2- | tr -d \"'\\\"\" | sed -E 's/[?&]load_balance=true//'") || exit 2
    [ -n "$dsn" ] || { echo "staging access: $envfile on $holder has no DATABASE_URL" >&2; exit 2; }
    { printf '%s\n' "$dsn"; cat; } | ssh_run "$client" \
      'read -r dsn; PGOPTIONS="-c default_transaction_read_only=on" psql "$dsn" -X -A -t -q -v ON_ERROR_STOP=1 -f -'
    ;;
  ch)
    row=$(map_row "${STACK_ACCESS_MAP_FILE:-}" '$1 == "ch"') || exit 2
    [ -n "$row" ] || { echo "staging access: no ClickHouse in the access map" >&2; exit 2; }
    IFS='|' read -r _ _ holder envfile port <<<"$row"
    ssh_run "$holder" "set -a; eval \"\$(sudo -n grep -E '^CLICKHOUSE_(ADDR|USER|PASSWORD)=' '$envfile')\"; set +a
      curl -s -m 100 \"http://\${CLICKHOUSE_ADDR%:*}:$port/?readonly=1\" -H \"X-ClickHouse-User: \$CLICKHOUSE_USER\" -H \"X-ClickHouse-Key: \$CLICKHOUSE_PASSWORD\" --data-binary @-"
    ;;
  edge-ls)
    service=${2:?usage: staging-access.sh edge-ls <service> <dir> [depth]}
    dir=${3:?usage: staging-access.sh edge-ls <service> <dir> [depth]}
    row=$(map_row "${STAGING_SERVICE_MAP_FILE:-}" "\$1 == \"$service\"") || exit 2
    [ -n "$row" ] || { echo "staging access: no host for $service in the service map" >&2; exit 2; }
    host=$(cut -d '|' -f 2 <<<"$row")
    ssh_run -n "$host" "find '$dir' -maxdepth ${4:-3}"
    ;;
  *)
    echo "usage: staging-access.sh pg <db> | ch | edge-ls <service> <dir> [depth]" >&2
    exit 2
    ;;
esac

#!/usr/bin/env bash
# Credentials a deployed-target run keeps in its evidence directory while it
# runs (sourced by verify-target.sh): session JWT caches, cookie jars, the
# operator login and the test tenant's wallet key. None of them is evidence, so
# they are removed when the run ends, however it ends, and a later run removes
# what a killed run left.

# purge_run_credentials <evidence dir>: deletes every credential file under it
# and keeps the logs and reports.
purge_run_credentials() {
  [ -d "${1:-}" ] || return 0
  find "$1" -type f \( -name 'session-*.jwt' -o -name '*.jar' -o -name '*-cookies' -o -name 'login.*' \
    -o -name 'operator.env' -o -name 'cli-auth.env' -o -name 'cli-restart.env' -o -name 'wallet.key' \
    -o -name 'walletsign' \) -delete 2>/dev/null
  return 0
}

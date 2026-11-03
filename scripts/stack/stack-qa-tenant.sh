#!/usr/bin/env bash
# The compose stack's declared tenants, run inside stack-runner by verify.sh
# through the same test-tenant.sh path a deployed-target run takes. Scenarios
# declared "# tenant: qa" run as the QA customer scripts/stack/bootstrap.yaml
# declares (alias stack-qa), with the password that rendered fixture carries;
# the sign-in's login and token stay in the runner's state directory for the
# run's later calls.
#
#   stack-qa-tenant.sh login                          sign in, mint the run's QA token
#   stack-qa-tenant.sh run <scenario>                 run a scenario as its declared tenant
#   stack-qa-tenant.sh sweep <since> <windows file>   the QA tenant's sweep pass
#
# STACK_BOOTSTRAPDECL names the declaration reader verify.sh built on the host.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export STACK_REPO=${STACK_REPO:-$(cd "$here/../.." && pwd)}
: "${STACK_STATE_DIR:=/tmp/fw-stack}"
export STACK_STATE_DIR
# shellcheck source=scripts/stack/test-tenant.sh
. "$here/test-tenant.sh"
QA_TENANT_ALIAS=stack-qa TENANT_DECLARATION_RENDERED=1
fixture="$here/bootstrap.yaml"
run_env="$(qa_tenant_dir)/run.env"
# shellcheck source=/dev/null
[ "${1:-}" != login ] && [ -f "$run_env" ] && . "$run_env"

case "${1:-}" in
  login)
    # shellcheck source=scripts/stack/lib.sh
    . "$here/lib.sh"
    # Each run starts from an empty QA state directory, as a target run does:
    # the sign-in deletes a previous run's webhook endpoint, so its cache and
    # ledger describe nothing that still exists.
    rm -rf "$(qa_tenant_dir)"
    mkdir -p "$(qa_tenant_dir)" && chmod 700 "$(qa_tenant_dir)"
    password=$(qa_tenant_declaration "$fixture" | cut -f6)
    qa_tenant_sign_in "$fixture" "slot-${STACK_SLOT:-1}" "$password" "$fixture declares no password for $QA_TENANT_ALIAS"
    rc=$?
    (
      umask 077
      for var in QA_TENANT_ID QA_API_TOKEN QA_TOKEN_ID QA_EMAIL QA_PASSWORD QA_BLOCKED_REASON; do
        printf '%s=%q\n' "$var" "${!var}"
      done >"$run_env"
    )
    [ "$rc" = 0 ] || echo "QA tenant unavailable: $QA_BLOCKED_REASON" >&2
    exit "$rc"
    ;;
  run)
    [ -f "$run_env" ] || QA_BLOCKED_REASON="the run did not sign in as the QA tenant (stack-qa-tenant.sh login)"
    run_scenario_as_its_tenant "${2:?usage: stack-qa-tenant.sh run <scenario>}"
    ;;
  sweep)
    # shellcheck source=scripts/stack/lib.sh
    . "$here/lib.sh"
    qa_tenant_sweep "${2:?usage: stack-qa-tenant.sh sweep <since> <windows file>}" "${3:-}" "$fixture"
    ;;
  *)
    echo 'usage: stack-qa-tenant.sh login | run <scenario> | sweep <since> <windows file>' >&2
    exit 2
    ;;
esac

#!/usr/bin/env bash
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
. "$here/bootstrap-results.sh"
log=$(mktemp)
trap 'rm -f "$log"' EXIT
failures=0
check_result() {
  local description=$1 expected=$2 line=$3 actual=1
  printf '%s\n' "$line" >"$log"
  bootstrap_section_unchanged "quartermaster bootstrap tenants" "$log" && actual=0
  if [ "$actual" = "$expected" ]; then
    printf 'PASS %s\n' "$description"
  else
    printf 'FAIL %s: expected status %s, got %s\n' "$description" "$expected" "$actual"
    failures=$((failures+1))
  fi
}
check_result 'legacy unchanged output' 0 'quartermaster bootstrap tenants: created=0 updated=0 noop=2'
check_result 'current unchanged output with stopped=0' 0 'quartermaster bootstrap tenants: created=0 updated=0 noop=2 stopped=0'
check_result 'actual retirement is a change' 1 'quartermaster bootstrap tenants: created=0 updated=0 noop=2 stopped=1'
check_result 'creation is a change' 1 'quartermaster bootstrap tenants: created=1 updated=0 noop=2 stopped=0'
check_result 'update is a change' 1 'quartermaster bootstrap tenants: created=0 updated=1 noop=2 stopped=0'
check_result 'zero observed no-ops cannot prove replay' 1 'quartermaster bootstrap tenants: created=0 updated=0 noop=0 stopped=0'
check_result 'another section cannot satisfy this section' 1 'commodore bootstrap accounts: created=0 updated=0 noop=2 stopped=0'
check_result 'unknown trailing state is not accepted' 1 'quartermaster bootstrap tenants: created=0 updated=0 noop=2 stopped=0 failed=1'

# Exercise the actual two-pass call site with deterministic bootstrap output.
replay=$(sed -n '/^bootstrap_log="$(mktemp)"$/,/^rm -f "$bootstrap_log"$/p' "$here/up.sh")
if [ -z "$replay" ]; then
  printf 'FAIL bootstrap replay call site was not found\n'
  exit 1
fi
check_replay() {
  local description=$1 expected=$2 scenario=$3 actual=1
  if (
    calls=0
    fail() { rm -f "$bootstrap_log"; exit 1; }
    bootstrap_accounts_pass() {
      calls=$((calls+1))
      local section created=0 updated=0 noop=2 stopped=0
      if [ "$calls" = 2 ]; then
        case "$scenario" in
          creation) created=1 ;;
          update) updated=1 ;;
          retirement) stopped=1 ;;
          failure) return 1 ;;
        esac
      fi
      for section in 'quartermaster bootstrap tenants' 'purser bootstrap customer_billing' 'commodore bootstrap accounts'; do
        if [ "$calls" = 2 ] && [ "$scenario" = missing ] && [ "$section" = 'purser bootstrap customer_billing' ]; then
          continue
        fi
        if [ "$section" = 'quartermaster bootstrap tenants' ]; then
          printf '%s: created=%s updated=%s noop=%s stopped=%s\n' "$section" "$created" "$updated" "$noop" "$stopped"
        else
          printf '%s: created=%s updated=%s noop=%s\n' "$section" "$created" "$updated" "$noop"
        fi
      done >>"$1"
    }
    eval "$replay"
  ); then actual=0; fi
  if [ "$actual" = "$expected" ]; then
    printf 'PASS %s\n' "$description"
  else
    printf 'FAIL %s: expected status %s, got %s\n' "$description" "$expected" "$actual"
    failures=$((failures+1))
  fi
}
check_replay 'two unchanged passes succeed' 0 unchanged
check_replay 'earlier no-op cannot hide second-pass creation' 1 creation
check_replay 'earlier no-op cannot hide second-pass update' 1 update
check_replay 'earlier no-op cannot hide second-pass retirement' 1 retirement
check_replay 'earlier output cannot hide a missing second-pass section' 1 missing
check_replay 'second-pass command failure is refused' 1 failure
exit "$failures"

#!/usr/bin/env bash
# Unit checks for stack slot ownership (scripts/stack/common.sh) against a
# stubbed docker CLI, so they need no stack. Two checkouts A and B ask for the
# same STACK_SLOT: while A's stack is coming up or up, B's up.sh and down.sh
# refuse naming A and never reach compose; A keeps full control of its slot.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
work=$(cd "$(mktemp -d "${TMPDIR:-/tmp}/fw-stack-slot-test.XXXXXX")" && pwd)
slot=200
engine="slot-test-$$-$RANDOM"
holder=""
cleanup() {
  [ -n "$holder" ] && kill "$holder" 2>/dev/null
  rm -rf "$work" "/tmp/frameworks-stack-slots/$engine"
}
trap cleanup EXIT

mkdir -p "$work/bin"
cat >"$work/bin/docker" <<'EOF'
#!/usr/bin/env bash
# Stub. `ps` lists the working_dir label of each container of the test slot's
# project from $FIXTURE_DIR/containers; every other compose call is recorded in
# $FIXTURE_DIR/calls as "<project> <cwd> <args>".
case "$1" in
version) echo amd64; exit 0 ;;
info) echo "$FIXTURE_ENGINE"; exit 0 ;;
ps)
  case " $* " in
  *"label=com.docker.compose.project=fw-stack-$FIXTURE_SLOT "*) cat "$FIXTURE_DIR/containers" 2>/dev/null ;;
  esac
  exit 0
  ;;
compose)
  case " $* " in
  *" config --services "*) echo edge; exit 0 ;;
  esac
  echo "$COMPOSE_PROJECT_NAME $PWD ${*:2}" >>"$FIXTURE_DIR/calls"
  exit 0
  ;;
esac
exit 0
EOF
chmod +x "$work/bin/docker"
export PATH="$work/bin:$PATH" FIXTURE_DIR="$work" FIXTURE_ENGINE="$engine" FIXTURE_SLOT="$slot" STACK_SLOT="$slot"

checkout() { # checkout <name>: a checkout carrying this tree's stack scripts
  mkdir -p "$work/$1/monorepo/scripts/stack"
  cp "$here/common.sh" "$here/up.sh" "$here/down.sh" "$work/$1/monorepo/scripts/stack/"
  printf '%s\n' "$work/$1/monorepo"
}
a=$(checkout ws-a)
b=$(checkout ws-b)

failures=0
expect() { # expect <description> <command...>
  local what=$1
  shift
  if "$@"; then printf 'ok    %s\n' "$what"; else printf 'FAIL  %s\n' "$what"; failures=$((failures + 1)); fi
}
run() { # run <checkout> <up|down>: the script's exit status, output in $work/out
  : >"$work/calls"
  bash "$1/scripts/stack/$2.sh" >"$work/out" 2>&1
}
refused_naming_a() { # refused_naming_a <checkout> <up|down>
  ! run "$1" "$2" && grep -qF "belongs to the checkout at $a" "$work/out" && [ ! -s "$work/calls" ]
}
proceeds() { # proceeds <checkout> <down>
  run "$1" "$2" && grep -qF "fw-stack-$slot $1 --project-directory $1 down -v --remove-orphans" "$work/calls"
}
claims() { bash -c '. "$1/scripts/stack/common.sh" && stack_claim_slot' _ "$1" >"$work/out" 2>&1; }

# Both checkouts address the same compose project for the same slot number.
project_of() { bash -c '. "$1/scripts/stack/common.sh" && echo "$COMPOSE_PROJECT_NAME"' _ "$1" 2>/dev/null; }
same_project() { [ "$(project_of "$a")" = "fw-stack-$slot" ] && [ "$(project_of "$b")" = "fw-stack-$slot" ]; }
expect "both checkouts map slot $slot to fw-stack-$slot" same_project

# A is bringing its stack up: claimed, no container yet.
bash -c '. "$1/scripts/stack/common.sh" && stack_claim_slot && exec sleep 300' _ "$a" >/dev/null 2>&1 &
holder=$!
for _ in $(seq 1 50); do
  grep -q "^pid=$holder\$" "/tmp/frameworks-stack-slots/$engine/slot-$slot" 2>/dev/null && break
  sleep 0.2
done
expect "A's bring-up claims the slot" grep -q "^pid=$holder\$" "/tmp/frameworks-stack-slots/$engine/slot-$slot"
expect "B's up.sh refuses a slot A is bringing up" refused_naming_a "$b" up
expect "B's down.sh refuses a slot A is bringing up" refused_naming_a "$b" down
kill "$holder" 2>/dev/null
wait "$holder" 2>/dev/null
holder=""

# A's stack is up: its containers carry A's working_dir label.
printf '%s\n' "$a" >"$work/containers"
expect "B's up.sh refuses a slot A's stack runs in" refused_naming_a "$b" up
expect "B's down.sh leaves A's project alone" refused_naming_a "$b" down
expect "A's own up claims its running slot" claims "$a"
expect "A's down.sh removes A's project" proceeds "$a" down

# A's stack is gone and its claim's process exited: the slot is free.
: >"$work/containers"
expect "B claims the slot A freed" claims "$b"
expect "B's down.sh removes B's project" proceeds "$b" down

if [ "$failures" -gt 0 ]; then
  echo "slot ownership checks: $failures failed"
  exit 1
fi
echo "slot ownership checks: ok"

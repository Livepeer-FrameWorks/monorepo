#!/usr/bin/env bash
set -euo pipefail
command -v setsid >/dev/null || { echo 'SKIP: remote job lifecycle requires Linux setsid'; exit 0; }
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
runner="$repo/scripts/remote-dev-job.sh"

assert_empty() {
  if find "$1" -mindepth 1 -print -quit | grep -q .; then
    echo "FAIL: remote command left temporary files in $1" >&2
    exit 1
  fi
}

mkdir -p "$fixture/success" "$fixture/failure" "$fixture/cancel"
printf 'streamed input\n' | bash "$runner" "$fixture/success" bash -c '
  test "$TMPDIR" = "$GOTMPDIR"
  test -d "$TMPDIR"
  read -r value
  test "$value" = "streamed input"
  touch "$TMPDIR/compiler-output"
'
assert_empty "$fixture/success"
mkdir -p "$fixture/permissions"
(
  umask 0022
  bash "$runner" "$fixture/permissions" bash -c '
    [[ $(umask) = 0022 ]] || { echo "FAIL: remote job changed the command umask" >&2; exit 1; }
  '
)
assert_empty "$fixture/permissions"
set +e
bash "$runner" "$fixture/failure" bash -c 'touch "$TMPDIR/compiler-output"; exit 7'
status=$?
set -e
[[ "$status" = 7 ]] || { echo "FAIL: command exit status became $status" >&2; exit 1; }
assert_empty "$fixture/failure"

bash "$runner" "$fixture/cancel" bash -c '
  trap '\''echo stopped > "$1/stopped"'\'' EXIT
  printf "%s\n" "$TMPDIR" > "$1/ready"
  touch "$TMPDIR/compiler-output"
  sleep 60 &
  wait
' bash "$fixture" &
job=$!
for _ in $(seq 1 100); do
  [[ ! -f "$fixture/ready" ]] || break
  sleep 0.05
done
[[ -f "$fixture/ready" ]] || { kill -TERM "$job"; echo 'FAIL: command did not start' >&2; exit 1; }
kill -TERM "$job"
set +e
wait "$job"
status=$?
set -e
[[ "$status" = 143 && -f "$fixture/stopped" ]] || { echo 'FAIL: cancellation did not run child teardown' >&2; exit 1; }
assert_empty "$fixture/cancel"

mkdir -p "$fixture/background"
bash "$runner" "$fixture/background" bash -c '
  sleep 60 &
  echo $! > "$1/background-pid"
' bash "$fixture"
background_pid=$(cat "$fixture/background-pid")
if [[ -r "/proc/$background_pid/stat" ]] && [[ $(awk '{print $3}' "/proc/$background_pid/stat") != Z ]]; then
  echo 'FAIL: a background child survived its remote job' >&2
  exit 1
fi
assert_empty "$fixture/background"

# Exercise the SSH wrapper against a private fake remote filesystem. The
# unmodified wrapper leaks this command's file into its inherited TMPDIR.
mkdir -p "$fixture/bin" "$fixture/legacy" "$fixture/remote/workspaces/fixture/job-test/monorepo/scripts"
cp "$runner" "$fixture/remote/workspaces/fixture/job-test/monorepo/scripts/"
cat > "$fixture/bin/ssh" <<'SSH'
#!/usr/bin/env bash
command=${!#}
command=${command//\/srv\/frameworks-dev/$REMOTE_DEV_TEST_ROOT}
exec bash -c "$command"
SSH
chmod +x "$fixture/bin/ssh"
cat > "$fixture/bin/df" <<'DF'
#!/usr/bin/env bash
printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\nfixture 100000000 1000 %s 1%% /\n' "${REMOTE_DEV_TEST_AVAILABLE_KB:-99000000}"
DF
chmod +x "$fixture/bin/df"
PATH="$fixture/bin:$PATH" REMOTE_DEV_TEST_ROOT="$fixture/remote" \
  REMOTE_DEV_TEST_FIXTURE="$fixture" \
  REMOTE_DEV_HOST=fixture REMOTE_DEV_PORT=22 REMOTE_DEV_USER=fixture TMPDIR="$fixture/legacy" \
  bash "$repo/scripts/remote-dev.sh" run job-test bash -c '
    printf "%s\n" "$TMPDIR" > "$REMOTE_DEV_TEST_FIXTURE/actual-tmp"
    touch "$TMPDIR/compiler-output"
  '
actual_tmp=$(cat "$fixture/actual-tmp")
case "$actual_tmp" in
  "$fixture/remote/cache/fixture/job-tmp/"*) ;;
  *) echo 'FAIL: remote-dev run does not own the command temporary directory' >&2; exit 1 ;;
esac
[[ ! -e "$actual_tmp" ]] || { echo 'FAIL: SSH job temporary directory survived completion' >&2; exit 1; }
assert_empty "$fixture/legacy"

set +e
PATH="$fixture/bin:$PATH" REMOTE_DEV_TEST_ROOT="$fixture/remote" \
  REMOTE_DEV_HOST=fixture REMOTE_DEV_PORT=22 REMOTE_DEV_USER=fixture \
  REMOTE_DEV_TEST_AVAILABLE_KB=1000000 \
  bash "$repo/scripts/remote-dev.sh" run job-test touch "$fixture/should-not-start"
status=$?
set -e
[[ "$status" = 75 && ! -e "$fixture/should-not-start" ]] || {
  echo 'FAIL: remote job started without disk headroom' >&2
  exit 1
}

for available_kb in 46874999 46875000; do
  set +e
  PATH="$fixture/bin:$PATH" REMOTE_DEV_TEST_ROOT="$fixture/remote" \
    REMOTE_DEV_HOST=fixture REMOTE_DEV_PORT=22 REMOTE_DEV_USER=fixture \
    REMOTE_DEV_TEST_AVAILABLE_KB="$available_kb" \
    bash "$repo/scripts/remote-dev.sh" run job-test touch "$fixture/budget-$available_kb"
  status=$?
  set -e
  if [[ "$available_kb" = 46874999 ]]; then
    [[ "$status" = 75 && ! -e "$fixture/budget-$available_kb" ]] || {
      echo 'FAIL: remote disk budget admitted less than 48 decimal GB' >&2
      exit 1
    }
  else
    [[ "$status" = 0 && -e "$fixture/budget-$available_kb" ]] || {
      echo 'FAIL: remote disk budget rejected exactly 48 decimal GB' >&2
      exit 1
    }
  fi
done

for available_kb in 80000000 200000000; do
  capacity="$fixture/capacity-$available_kb"
  mkdir -p "$capacity"
  PATH="$fixture/bin:$PATH" REMOTE_DEV_TEST_ROOT="$fixture/remote" \
    REMOTE_DEV_HOST=fixture REMOTE_DEV_PORT=22 REMOTE_DEV_USER=fixture \
    REMOTE_DEV_TEST_AVAILABLE_KB="$available_kb" \
    bash "$repo/scripts/remote-dev.sh" run job-test bash -c '
      touch "$1/first-ready"
      while [[ ! -e "$1/release" ]]; do sleep 0.05; done
    ' bash "$capacity" &
  first_job=$!
  for _ in $(seq 1 100); do
    [[ ! -f "$capacity/first-ready" ]] || break
    sleep 0.05
  done
  if [[ ! -f "$capacity/first-ready" ]]; then
    kill -TERM "$first_job" || true
    wait "$first_job" || true
    echo 'FAIL: first capacity fixture did not start' >&2
    exit 1
  fi
  set +e
  PATH="$fixture/bin:$PATH" REMOTE_DEV_TEST_ROOT="$fixture/remote" \
    REMOTE_DEV_HOST=fixture REMOTE_DEV_PORT=22 REMOTE_DEV_USER=fixture \
    REMOTE_DEV_TEST_AVAILABLE_KB="$available_kb" \
    timeout 5s bash "$repo/scripts/remote-dev.sh" run job-test touch "$capacity/second-started"
  status=$?
  set -e
  touch "$capacity/release"
  wait "$first_job"
  if [[ "$available_kb" = 80000000 ]]; then
    [[ "$status" = 75 && ! -e "$capacity/second-started" ]] || {
      echo 'FAIL: concurrent remote jobs shared one disk headroom allocation' >&2
      exit 1
    }
  else
    [[ "$status" = 0 && -e "$capacity/second-started" ]] || {
      echo 'FAIL: sufficient headroom did not allow concurrent remote jobs' >&2
      exit 1
    }
  fi
  PATH="$fixture/bin:$PATH" REMOTE_DEV_TEST_ROOT="$fixture/remote" \
    REMOTE_DEV_HOST=fixture REMOTE_DEV_PORT=22 REMOTE_DEV_USER=fixture \
    REMOTE_DEV_TEST_AVAILABLE_KB="$available_kb" \
    bash "$repo/scripts/remote-dev.sh" run job-test touch "$capacity/after-release"
done

cat > "$fixture/bin/python3" <<'PYTHON'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$REMOTE_DEV_TEST_STORAGE_ARGS"
PYTHON
chmod +x "$fixture/bin/python3"
PATH="$fixture/bin:$PATH" REMOTE_DEV_TEST_ROOT="$fixture/remote" \
  REMOTE_DEV_HOST=fixture REMOTE_DEV_PORT=22 REMOTE_DEV_USER=fixture \
  REMOTE_DEV_TEST_AVAILABLE_KB=1 REMOTE_DEV_TEST_STORAGE_ARGS="$fixture/storage-args" \
  bash "$repo/scripts/remote-dev.sh" storage job-test
[[ $(cat "$fixture/storage-args") = scripts/remote-dev-storage.py ]] || {
  echo 'FAIL: storage audit gained an empty argument or required build disk headroom' >&2
  exit 1
}

# The real env targets must honor the shared cache rather than compiling a
# second standard library in every checkout. The stub records the child env.
mkdir -p "$fixture/env-targets/scripts/env"
cat > "$fixture/bin/go" <<'GO'
#!/usr/bin/env bash
printf '%s\n' "$GOCACHE" >> "$REMOTE_DEV_TEST_CACHE_LOG"
[[ -z "${REMOTE_DEV_TEST_GO_TEST_NAME:-}" ]] || printf '%s\n' "$REMOTE_DEV_TEST_GO_TEST_NAME"
exit "${REMOTE_DEV_TEST_GO_EXIT:-0}"
GO
chmod +x "$fixture/bin/go"
PATH="$fixture/bin:$PATH" GOCACHE="$fixture/shared-cache" REMOTE_DEV_TEST_CACHE_LOG="$fixture/cache-log" \
  make --no-print-directory -f "$repo/Makefile" -C "$fixture/env-targets" env frontend-env >/dev/null
[[ $(wc -l < "$fixture/cache-log") = 3 ]] || { echo 'FAIL: env targets did not run all three Go commands' >&2; exit 1; }
if grep -vxF "$fixture/shared-cache" "$fixture/cache-log"; then
  echo 'FAIL: env targets replaced the explicitly configured shared Go cache' >&2
  exit 1
fi
mkdir -p "$fixture/env-targets/config/env"
cp "$repo/scripts/verify-compose-profiles.sh" "$fixture/env-targets/scripts/"
set +e
PATH="$fixture/bin:$PATH" GOCACHE="$fixture/shared-cache" REMOTE_DEV_TEST_CACHE_LOG="$fixture/compose-cache-log" REMOTE_DEV_TEST_GO_EXIT=42 \
  bash "$fixture/env-targets/scripts/verify-compose-profiles.sh"
status=$?
set -e
[[ "$status" = 42 ]] || { echo 'FAIL: compose verifier lost the Go command failure' >&2; exit 1; }
[[ $(cat "$fixture/compose-cache-log") = "$fixture/shared-cache" ]] || { echo 'FAIL: compose verifier replaced the shared Go cache' >&2; exit 1; }
PATH="$fixture/bin:$PATH" GOCACHE="$fixture/shared-cache" REMOTE_DEV_TEST_CACHE_LOG="$fixture/selection-cache-log" REMOTE_DEV_TEST_GO_TEST_NAME=TestFixture \
  bash "$repo/scripts/check-go-test-selection.sh" "$fixture/env-targets" ./fixture TestFixture '^TestFixture$' >/dev/null
[[ $(cat "$fixture/selection-cache-log") = "$fixture/shared-cache" ]] || { echo 'FAIL: test selection replaced the shared Go cache' >&2; exit 1; }
PATH="$fixture/bin:$PATH" GOCACHE="$fixture/shared-cache" FRAMEWORKS_TEST_SELECTION_GOCACHE="$fixture/explicit-selection-cache" \
  REMOTE_DEV_TEST_CACHE_LOG="$fixture/explicit-cache-log" REMOTE_DEV_TEST_GO_TEST_NAME=TestFixture \
  bash "$repo/scripts/check-go-test-selection.sh" "$fixture/env-targets" ./fixture TestFixture '^TestFixture$' >/dev/null
[[ $(cat "$fixture/explicit-cache-log") = "$fixture/explicit-selection-cache" ]] || { echo 'FAIL: explicit selection cache override was ignored' >&2; exit 1; }
echo 'PASS: remote job lifecycle and shared build-cache configuration'

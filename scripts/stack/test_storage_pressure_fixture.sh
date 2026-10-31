#!/usr/bin/env bash
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
. "$here/storage-pressure-fixture.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
STORE="$work/media"
OLD=$(($(date +%s) - 7200))
STACK_TENANT_ID=fixture-tenant
FOGHORN_A_DB=fixture-foghorn
mkdir -p "$STORE/vod" "$STORE/clips" "$STORE/dvr/recording" "$STORE/upload"
for file in vod/delivered.mkv vod/delivered.mkv.dtsh vod/unrelated.mkv dvr/recording/manifest.m3u8 upload/delivered.mp4; do
  printf 'fixture' >"$STORE/$file"
done
pg() {
  [ "$1" = "$FOGHORN_A_DB" ]
  [[ "$2" == *"tenant_id='fixture-tenant'"* && "$2" == *"status='completed'"* && "$2" == *"sync_status='synced'"* ]]
  printf 'delivered\nrecording\n'
}
# The container uses GNU touch; provide its narrow fixture operation on macOS too.
mkdir -p "$work/bin"
cat >"$work/bin/touch" <<'PYTOUCH'
#!/usr/bin/env python3
import os, sys
assert sys.argv[1] == '-d' and sys.argv[2].startswith('@')
at = int(sys.argv[2][1:])
for path in sys.argv[3:]:
    os.utime(path, (at, at))
PYTOUCH
chmod +x "$work/bin/touch"
export PATH="$work/bin:$PATH"
as_helmsman() { bash -c "$1"; }
age_synced_pressure_fixture_cache
python3 - "$STORE" "$OLD" <<'PY'
import os,sys
root,old=sys.argv[1],int(sys.argv[2])
for path in ['vod/delivered.mkv','vod/delivered.mkv.dtsh','dvr/recording/manifest.m3u8']:
    assert abs(os.stat(root+'/'+path).st_mtime-old)<2, path
for path in ['vod/unrelated.mkv','upload/delivered.mp4']:
    assert os.stat(root+'/'+path).st_mtime>old+3600, path
PY
STACK_TARGET=staging
if age_synced_pressure_fixture_cache; then echo 'staging fixture write accepted' >&2; exit 1; fi
unset STACK_TARGET
pg() { printf "bad';touch malicious\n"; }
if age_synced_pressure_fixture_cache 2>/dev/null; then echo 'unsafe hash accepted' >&2; exit 1; fi
printf 'storage pressure fixture isolation: ok\n'

#!/bin/sh
# Removes the redis role's vm.overcommit_memory drop-in once no Redis/Valkey
# server is left on the host. The drop-in is host-wide, so it stays while any
# other named server unit exists or a default package server is enabled;
# Sentinels and exporters never fork for a save and do not keep it.
#
# Usage: redis-release-overcommit.sh [root]
# Prints "removed" when it deleted the drop-in, "kept" when a server still
# needs it, and "absent" when there was nothing to remove.
set -eu

root=${1:-}
conf="$root/etc/sysctl.d/92-frameworks-redis.conf"

if [ ! -e "$conf" ]; then
  echo absent
  exit 0
fi

for unit in "$root"/etc/systemd/system/frameworks-redis-*.service; do
  [ -e "$unit" ] || continue
  case "$unit" in
    *-sentinel.service | *-exporter.service) continue ;;
  esac
  echo kept
  exit 0
done

for unit in redis redis-server valkey valkey-server; do
  if [ -e "$root/etc/systemd/system/multi-user.target.wants/$unit.service" ]; then
    echo kept
    exit 0
  fi
done

rm -f "$conf"
# Removing the drop-in does not change the running kernel. Restore the kernel
# default (heuristic overcommit, 0), then re-apply whatever the remaining
# drop-ins declare. A root argument is a test tree, not this host.
if [ -z "$root" ]; then
  sysctl -q -w vm.overcommit_memory=0
  sysctl -q --system
fi
echo removed

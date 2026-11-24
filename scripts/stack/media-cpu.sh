#!/bin/sh
set -eu

# FFmpeg sizes native decoder/encoder pools from affinity, not the cgroup CPU
# time quota. Keep those pools within the same budget as the orchestrator.
media_cpu_set() {
  awk -v allowed="$1" -v quota="$2" -v period="$3" 'BEGIN {
    if (period !~ /^[0-9]+$/ || period <= 0) exit 2
    if (quota == "max" || quota == "-1") limit = 2147483647
    else {
      if (quota !~ /^[0-9]+$/ || quota <= 0) exit 2
      limit = int(quota / period)
      if (limit < 1) limit = 1
    }
    n = split(allowed, ranges, ",")
    for (i = 1; i <= n; i++) {
      if (ranges[i] !~ /^[0-9]+(-[0-9]+)?$/) exit 2
      parts = split(ranges[i], ends, "-")
      first = ends[1] + 0
      last = parts == 2 ? ends[2] + 0 : first
      if (last < first) exit 2
      for (cpu = first; cpu <= last && count < limit; cpu++) {
        result = result (count ? "," : "") cpu
        count++
      }
    }
    if (!count) exit 2
    print result
  }'
}

media_cpu_limits() {
  if [ -r /sys/fs/cgroup/cpu.max ]; then
    cat /sys/fs/cgroup/cpu.max
  else
    printf '%s %s\n' "$(cat /sys/fs/cgroup/cpu/cpu.cfs_quota_us)" "$(cat /sys/fs/cgroup/cpu/cpu.cfs_period_us)"
  fi
}

media_cpu_allowed() { awk '/^Cpus_allowed_list:/ { print $2 }' /proc/self/status; }

media_cpu_main() {
  limits=$(media_cpu_limits)
  quota=${limits%% *}
  period=${limits#* }
  cpus=$(media_cpu_set "$(media_cpu_allowed)" "$quota" "$period")
  exec taskset -c "$cpus" "$@"
}

media_cpu_main "$@"

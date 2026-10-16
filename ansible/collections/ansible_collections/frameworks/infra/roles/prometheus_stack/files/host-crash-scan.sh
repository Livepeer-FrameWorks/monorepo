#!/bin/sh
# Writes the host crash scan for node_exporter's textfile collector:
#   host-crash-scan.sh <awk program> <textfile dir> <window seconds>
# Crashes of child processes (Mist's MistProc*, transcoders) never fail a
# systemd unit, so the kernel and systemd-coredump journal records are the only
# host-wide signal. The file is replaced atomically so a scrape never reads a
# partial write.
set -eu

program="$1"
dir="$2"
window="$3"
out="$dir/frameworks_host_crashes.prom"
tmp="$(mktemp "$dir/.frameworks_host_crashes.XXXXXX")"
trap 'rm -f "$tmp"' EXIT

journalctl --quiet --no-pager --output=cat --since="-${window}s" \
	_TRANSPORT=kernel + SYSLOG_IDENTIFIER=systemd-coredump |
	awk -f "$program" >"$tmp"
{
	echo "# HELP frameworks_host_crash_scan_timestamp_seconds Unix time the host crash scan last completed."
	echo "# TYPE frameworks_host_crash_scan_timestamp_seconds gauge"
	echo "frameworks_host_crash_scan_timestamp_seconds $(date +%s)"
} >>"$tmp"

chmod 0644 "$tmp"
mv "$tmp" "$out"
trap - EXIT

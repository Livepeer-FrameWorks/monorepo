# Counts process crash reports in journal messages (journalctl -o cat) and
# prints them in the Prometheus text format for node_exporter's textfile
# collector. The kernel names the crashing thread's comm (truncated to 15
# characters) in "comm[pid]: segfault at ..." and "traps: comm[pid] general
# protection|trap ..." reports; systemd-coredump logs "Process pid (comm) of
# user uid dumped core." for every dump it stores. A segfault that also dumps
# core is reported once per source.
/\[[0-9]+\]: segfault at / {
	p = $0
	sub(/\[[0-9]+\]: segfault at .*/, "", p)
	reports["kernel" SUBSEP p]++
	next
}
/traps: .*\[[0-9]+\] (general protection|trap )/ {
	p = $0
	sub(/^.*traps: /, "", p)
	sub(/\[[0-9]+\] .*/, "", p)
	reports["kernel" SUBSEP p]++
	next
}
/^Process [0-9]+ \(.*\) of user [0-9]+ dumped core/ {
	p = $0
	sub(/^Process [0-9]+ \(/, "", p)
	sub(/\) of user [0-9]+ dumped core.*/, "", p)
	reports["coredump" SUBSEP p]++
	next
}
END {
	print "# HELP frameworks_host_process_crash_reports Process crash reports logged in the trailing scan window, by reporting source and process name."
	print "# TYPE frameworks_host_process_crash_reports gauge"
	for (key in reports) {
		split(key, parts, SUBSEP)
		p = parts[2]
		gsub(/\\/, "\\\\", p)
		gsub(/"/, "\\\"", p)
		printf "frameworks_host_process_crash_reports{source=\"%s\",process=\"%s\"} %d\n", parts[1], p, reports[key]
	}
}

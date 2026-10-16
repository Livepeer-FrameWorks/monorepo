package cmd

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// hostCrashProbe reads, from the journal since SINCE, the systemd manager's
// unit failures and automatic restarts, systemd-coredump's dumps, and the
// kernel's segfault, general-protection and OOM-kill reports. It reads only;
// sections are separated by ## markers.
func hostCrashProbe(since string) string {
	return fmt.Sprintf(`set +e
SINCE=%s
echo "##systemd"
journalctl _PID=1 --since "$SINCE" --no-pager -o cat 2>/dev/null \
  | grep -E '\.service: (Failed with result|Scheduled restart job|Main process exited, code=dumped)'
echo "##coredump"
journalctl -t systemd-coredump --since "$SINCE" --no-pager -o cat 2>/dev/null \
  | grep -E '^Process [0-9]+ \(.*\) of user [0-9]+ (dumped core|terminated abnormally)'
echo "##kernel"
journalctl -k --since "$SINCE" --no-pager -o cat 2>/dev/null \
  | grep -Ei 'segfault at|general protection|out of memory: Killed process'
echo "##end"
`, ssh.ShellQuote(since))
}

var (
	crashUnitFailed   = regexp.MustCompile(`^(\S+\.service): Failed with result '([^']+)'`)
	crashUnitRestart  = regexp.MustCompile(`^(\S+\.service): Scheduled restart job`)
	crashCoreDump     = regexp.MustCompile(`^Process [0-9]+ \((.*)\) of user [0-9]+ (dumped core|terminated abnormally)`)
	crashKernelFault  = regexp.MustCompile(`(?:traps: )?([^\s\[]+)\[[0-9]+\]:? (segfault at|general protection)`)
	crashKernelOOM    = regexp.MustCompile(`(?i)out of memory: Killed process [0-9]+ \(([^)]+)\)`)
	crashSectionMarks = map[string]bool{"##systemd": true, "##coredump": true, "##kernel": true, "##end": true}
)

// hostCrashReport is one host's crash history in the probe window.
type hostCrashReport struct {
	UnitFailures map[string]map[string]int // unit -> systemd result -> count
	UnitRestarts map[string]int
	CoreDumps    map[string]int // process -> count
	KernelFaults map[string]int // process -> segfault/GP count
	OOMKills     map[string]int
	complete     bool
}

func parseHostCrashProbe(output string) hostCrashReport {
	report := hostCrashReport{
		UnitFailures: map[string]map[string]int{},
		UnitRestarts: map[string]int{},
		CoreDumps:    map[string]int{},
		KernelFaults: map[string]int{},
		OOMKills:     map[string]int{},
	}
	section := ""
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if crashSectionMarks[line] {
			section = line
			if line == "##end" {
				report.complete = true
			}
			continue
		}
		switch section {
		case "##systemd":
			if m := crashUnitFailed.FindStringSubmatch(line); m != nil {
				if report.UnitFailures[m[1]] == nil {
					report.UnitFailures[m[1]] = map[string]int{}
				}
				report.UnitFailures[m[1]][m[2]]++
			} else if m := crashUnitRestart.FindStringSubmatch(line); m != nil {
				report.UnitRestarts[m[1]]++
			}
		case "##coredump":
			if m := crashCoreDump.FindStringSubmatch(line); m != nil {
				report.CoreDumps[m[1]]++
			}
		case "##kernel":
			if m := crashKernelOOM.FindStringSubmatch(line); m != nil {
				report.OOMKills[m[1]]++
			} else if m := crashKernelFault.FindStringSubmatch(line); m != nil {
				report.KernelFaults[m[1]]++
			}
		}
	}
	return report
}

func countList(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s x%d", k, counts[k]))
	}
	return strings.Join(parts, ", ")
}

// findings lists one line per crashed unit and per crash class. Every finding
// is a failure: a unit that failed or restarted on its own, a core dump, a
// kernel-reported fault or an OOM kill all mean a process died unplanned.
func (r hostCrashReport) findings() []string {
	var out []string
	units := map[string]bool{}
	for unit := range r.UnitFailures {
		units[unit] = true
	}
	for unit := range r.UnitRestarts {
		units[unit] = true
	}
	names := make([]string, 0, len(units))
	for unit := range units {
		names = append(names, unit)
	}
	sort.Strings(names)
	for _, unit := range names {
		total := 0
		for _, n := range r.UnitFailures[unit] {
			total += n
		}
		line := unit + ":"
		if total > 0 {
			line += fmt.Sprintf(" failed %d time(s) (%s)", total, countList(r.UnitFailures[unit]))
		}
		if restarts := r.UnitRestarts[unit]; restarts > 0 {
			if total > 0 {
				line += ","
			}
			line += fmt.Sprintf(" restarted automatically %d time(s)", restarts)
		}
		out = append(out, line)
	}
	if len(r.CoreDumps) > 0 {
		out = append(out, "core dumps: "+countList(r.CoreDumps))
	}
	if len(r.KernelFaults) > 0 {
		out = append(out, "kernel-reported segfaults/protection faults: "+countList(r.KernelFaults))
	}
	if len(r.OOMKills) > 0 {
		out = append(out, "OOM kills: "+countList(r.OOMKills))
	}
	return out
}

// probeHostCrashes runs the crash probe on one host.
func probeHostCrashes(ctx context.Context, target diagnoseHost, since string, runnerFor func(inventory.Host) (ssh.Runner, error)) (hostCrashReport, error) {
	runner, err := runnerFor(target.Host)
	if err != nil {
		return hostCrashReport{}, fmt.Errorf("connect: %w", err)
	}
	result, err := runner.Run(ctx, "sh -c "+ssh.ShellQuote(hostCrashProbe(since)))
	if err != nil || result == nil {
		return hostCrashReport{}, fmt.Errorf("probe failed: %s", diagnosticCommandError(result, err))
	}
	report := parseHostCrashProbe(result.Stdout)
	if !report.complete {
		return report, fmt.Errorf("probe output incomplete")
	}
	return report, nil
}

// diagnoseCrashes reports unit failures, automatic restarts, core dumps,
// kernel faults and OOM kills on every target since `since`, and returns the
// number of findings and unreadable hosts.
func diagnoseCrashes(ctx context.Context, stdout, stderr io.Writer, targets []diagnoseHost, since string, runnerFor func(inventory.Host) (ssh.Runner, error)) int {
	fmt.Fprintf(stdout, "Crash Diagnostics (unit failures, restarts, core dumps, kernel faults, OOM kills since %s)\n", since)
	failures := 0
	for _, target := range targets {
		fmt.Fprintf(stdout, "\nHost: %s\n", diagnoseHostLabel(target))
		report, err := probeHostCrashes(ctx, target, since, runnerFor)
		if err != nil {
			failures++
			ux.Fail(stderr, fmt.Sprintf("%s: %v", target.Name, err))
			continue
		}
		findings := report.findings()
		if len(findings) == 0 {
			ux.Success(stdout, "  no unit failures, automatic restarts, core dumps, kernel faults or OOM kills")
			continue
		}
		for _, finding := range findings {
			failures++
			ux.Fail(stdout, "  "+finding)
		}
	}
	return failures
}

// doctorCrashWindow is how far back `cluster doctor` looks for crashes.
const doctorCrashWindow = "24 hours ago"

// doctorHostCrashes runs the crash probe on every target and folds the
// findings into one doctor result.
func doctorHostCrashes(ctx context.Context, targets []diagnoseHost, runnerFor func(inventory.Host) (ssh.Runner, error)) *health.CheckResult {
	result := &health.CheckResult{Name: "host_crashes", CheckedAt: time.Now(), Metadata: map[string]string{"window": "since " + doctorCrashWindow}}
	var problems []string
	for _, target := range targets {
		report, err := probeHostCrashes(ctx, target, doctorCrashWindow, runnerFor)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", target.Name, err))
			continue
		}
		for _, finding := range report.findings() {
			problems = append(problems, target.Name+": "+finding)
		}
	}
	if len(problems) > 0 {
		result.Status = "unhealthy"
		result.Error = fmt.Sprintf("%d finding(s) since %s: %s", len(problems), doctorCrashWindow, strings.Join(problems, "; "))
		result.Metadata["findings"] = strconv.Itoa(len(problems))
		return result
	}
	result.OK, result.Status = true, "healthy"
	result.Message = fmt.Sprintf("no unit failures, restarts, core dumps, kernel faults or OOM kills on %d host(s) since %s", len(targets), doctorCrashWindow)
	return result
}

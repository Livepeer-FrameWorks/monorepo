package cmd

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
)

// mirrorMakerReconfigureFailure is the line Kafka Connect's DistributedHerder
// logs each time a worker cannot publish recomputed connector task configs.
// A follower without a reachable leader REST server logs it once a minute.
const mirrorMakerReconfigureFailure = "Failed to reconfigure connector's tasks"

// mirrorMakerJournalWindow is how far back the worker check reads each
// worker's journal.
const mirrorMakerJournalWindow = "15 min ago"

// mirrorMakerWorkerProbeScript prints the count of task reconfiguration
// failures in the worker's recent journal and every TCP listener on the
// internal REST port. An unreadable journal fails the probe instead of
// reading as zero failures.
func mirrorMakerWorkerProbeScript(port int) string {
	return fmt.Sprintf(`set +e
if ! JOURNAL="$(journalctl -q -u frameworks-kafka-mirrormaker.service --since %s --no-pager -o cat 2>&1)"; then
  echo "journal of frameworks-kafka-mirrormaker could not be read: ${JOURNAL}" >&2
  exit 2
fi
printf 'RECONFIGURE_FAILURES %%s\n' "$(printf '%%s\n' "$JOURNAL" | grep -cF %s)"
if ! LISTENERS="$(ss -Hltn 'sport = :%d' 2>&1)"; then
  echo "ss failed: ${LISTENERS}" >&2
  exit 2
fi
printf '%%s\n' "$LISTENERS" | awk 'NF >= 4 { print "LISTEN " $4 }'
`, ssh.ShellQuote(mirrorMakerJournalWindow), ssh.ShellQuote(mirrorMakerReconfigureFailure), port)
}

// mirrorMakerWorkerProbe is one worker's parsed probe output.
type mirrorMakerWorkerProbe struct {
	ReconfigureFailures int
	Listeners           []string
}

func parseMirrorMakerWorkerProbe(output string) (mirrorMakerWorkerProbe, error) {
	var probe mirrorMakerWorkerProbe
	sawCount := false
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "RECONFIGURE_FAILURES "):
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "RECONFIGURE_FAILURES ")))
			if err != nil {
				return probe, fmt.Errorf("unexpected probe line %q", line)
			}
			probe.ReconfigureFailures = n
			sawCount = true
		case strings.HasPrefix(line, "LISTEN "):
			probe.Listeners = append(probe.Listeners, strings.TrimSpace(strings.TrimPrefix(line, "LISTEN ")))
		}
	}
	if !sawCount {
		return probe, fmt.Errorf("probe printed no reconfiguration count")
	}
	return probe, nil
}

// listensOn reports whether a listener serves addr:port. ss prints an
// IPv4 address a Java socket bound as IPv4-mapped IPv6 ([::ffff:a.b.c.d]:p),
// and a wildcard bind serves every address.
func (p mirrorMakerWorkerProbe) listensOn(addr string, port int) bool {
	suffix := ":" + strconv.Itoa(port)
	for _, listener := range p.Listeners {
		host, ok := strings.CutSuffix(listener, suffix)
		if !ok {
			continue
		}
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		host = strings.TrimPrefix(host, "::ffff:")
		switch host {
		case addr, "*", "0.0.0.0", "::":
			return true
		}
	}
	return false
}

// checkMirrorMakerWorkers verifies that every MirrorMaker2 worker runs its
// internal REST server on the address it advertises and that no worker failed
// to reconfigure connector tasks in the journal window. Mirroring of existing
// topics keeps flowing in both failure modes; what stops is task
// reconfiguration, so new topics and partitions never replicate. It is
// read-only.
func checkMirrorMakerWorkers(ctx context.Context, manifest *inventory.Manifest, runnerFor func(inventory.Host) (ssh.Runner, error)) *health.CheckResult {
	result := &health.CheckResult{Name: "kafka_mirrormaker_workers", CheckedAt: time.Now(), Metadata: map[string]string{}}
	hosts := orchestrator.KafkaMirrorMakerHosts(manifest)
	if len(hosts) == 0 {
		return nil
	}
	port := provisioner.KafkaMirrorMakerRESTPort
	var problems []string
	for _, hostName := range hosts {
		host, ok := manifest.GetHost(hostName)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: host not found in manifest", hostName))
			continue
		}
		runner, err := runnerFor(host)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: connect: %v", hostName, err))
			continue
		}
		out, runErr := runner.Run(ctx, "sh -c "+ssh.ShellQuote(mirrorMakerWorkerProbeScript(port)))
		if runErr != nil || out == nil || out.ExitCode != 0 {
			problems = append(problems, fmt.Sprintf("%s: probe: %s", hostName, diagnosticCommandError(out, runErr)))
			continue
		}
		probe, err := parseMirrorMakerWorkerProbe(out.Stdout)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", hostName, err))
			continue
		}
		addr := provisioner.KafkaMirrorMakerRESTAddress(host)
		result.Metadata[hostName] = fmt.Sprintf("rest=%s:%d listeners=%s reconfigure_failures=%d", addr, port, strings.Join(probe.Listeners, ","), probe.ReconfigureFailures)
		if !probe.listensOn(addr, port) {
			problems = append(problems, fmt.Sprintf("%s: internal REST server is not listening on %s:%d, so this worker cannot forward task configs to the leader or accept them as leader", hostName, addr, port))
		}
		if probe.ReconfigureFailures > 0 {
			problems = append(problems, fmt.Sprintf("%s: %d \"%s\" in the journal since %s; new topics and partitions get no replication task", hostName, probe.ReconfigureFailures, mirrorMakerReconfigureFailure, mirrorMakerJournalWindow))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		result.Status = "unhealthy"
		result.Error = strings.Join(problems, "; ")
		return result
	}
	result.OK = true
	result.Status = "healthy"
	result.Message = fmt.Sprintf("%d worker(s) serve internal REST and reconfigure tasks", len(hosts))
	return result
}

package cmd

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// kafkaQuorumFollowerLagLimit is how far behind the leader a voter may be
// before the quorum counts as degraded: past it a further voter loss stalls
// metadata commits.
const kafkaQuorumFollowerLagLimit = 30 * time.Second

// doctorKafkaQuorumWindow is how far back `cluster doctor` counts controller
// leadership losses.
const doctorKafkaQuorumWindow = "1 hour ago"

// kafkaQuorumStatus is the parsed output of
// `kafka-metadata-quorum.sh describe --status`.
type kafkaQuorumStatus struct {
	LeaderID             int
	LeaderEpoch          int64
	MaxFollowerLagTimeMs int64
	Voters               []int
	hasLeader            bool
}

var (
	kafkaQuorumJSONVoterID = regexp.MustCompile(`"id"\s*:\s*(\d+)`)
	kafkaQuorumPlainID     = regexp.MustCompile(`\d+`)
)

func parseKafkaQuorumStatus(output string) (kafkaQuorumStatus, error) {
	status := kafkaQuorumStatus{MaxFollowerLagTimeMs: -1}
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "LeaderId":
			id, err := strconv.Atoi(value)
			if err != nil {
				return status, fmt.Errorf("unreadable LeaderId %q", value)
			}
			status.LeaderID, status.hasLeader = id, true
		case "LeaderEpoch":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil {
				status.LeaderEpoch = n
			}
		case "MaxFollowerLagTimeMs":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil {
				status.MaxFollowerLagTimeMs = n
			}
		case "CurrentVoters":
			// Kafka 3.x prints [1,2,3]; 3.9+/4.x print a JSON list of voter objects.
			var ids [][]string
			if strings.Contains(value, `"id"`) {
				ids = kafkaQuorumJSONVoterID.FindAllStringSubmatch(value, -1)
			} else {
				for _, m := range kafkaQuorumPlainID.FindAllString(value, -1) {
					ids = append(ids, []string{m, m})
				}
			}
			for _, m := range ids {
				if id, err := strconv.Atoi(m[1]); err == nil {
					status.Voters = append(status.Voters, id)
				}
			}
		}
	}
	if !status.hasLeader {
		return status, fmt.Errorf("no LeaderId in quorum status output")
	}
	return status, nil
}

// problems lists what makes the quorum unhealthy. expectedVoters is the
// manifest's controller count; zero skips the voter-count comparison.
func (s kafkaQuorumStatus) problems(expectedVoters int) []string {
	var out []string
	if s.LeaderID < 0 {
		out = append(out, fmt.Sprintf("no leader (LeaderId %d, epoch %d)", s.LeaderID, s.LeaderEpoch))
	}
	if expectedVoters > 0 && len(s.Voters) != expectedVoters {
		out = append(out, fmt.Sprintf("%d voter(s) %v, manifest declares %d controller(s)", len(s.Voters), s.Voters, expectedVoters))
	}
	if lag := time.Duration(s.MaxFollowerLagTimeMs) * time.Millisecond; lag > kafkaQuorumFollowerLagLimit {
		out = append(out, fmt.Sprintf("a voter is %s behind the leader", lag))
	}
	return out
}

// kafkaControllerJournalProbe counts, in a controller unit's journal since a
// point in time, how often it gave up quorum leadership with no successor and
// how many controller events were exceptionally slow.
func kafkaControllerJournalProbe(unit, since string) string {
	return fmt.Sprintf("journalctl -u %s --since %s --no-pager -o cat 2>/dev/null | awk '/Renouncing the leadership/ && /the leader is \\(none\\)/{r++} /Exceptionally slow controller event/{s++} END{printf \"renounced=%%d slow=%%d\\n\", r, s}'",
		ssh.ShellQuote(unit), ssh.ShellQuote(since))
}

func parseKafkaControllerJournalProbe(output string) (renounced, slow int, err error) {
	for _, field := range strings.Fields(output) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		n, convErr := strconv.Atoi(value)
		if convErr != nil {
			return 0, 0, fmt.Errorf("unreadable %s count %q", key, value)
		}
		switch key {
		case "renounced":
			renounced = n
		case "slow":
			slow = n
		}
	}
	if !strings.Contains(output, "renounced=") {
		return 0, 0, fmt.Errorf("no journal counts in %q", strings.TrimSpace(output))
	}
	return renounced, slow, nil
}

type kafkaControllerTarget struct {
	ID   int
	Host string
	Unit string
}

// kafkaQuorumControllers returns the cluster's KRaft voters and the systemd
// unit each runs as: dedicated controllers, or every broker in combined mode.
func kafkaQuorumControllers(cluster kafkaClusterView) []kafkaControllerTarget {
	var out []kafkaControllerTarget
	for _, ctrl := range cluster.Controllers {
		out = append(out, kafkaControllerTarget{ID: ctrl.ID, Host: ctrl.Host, Unit: "frameworks-kafka-controller"})
	}
	if len(out) == 0 {
		for _, broker := range cluster.Brokers {
			out = append(out, kafkaControllerTarget{ID: broker.ID, Host: broker.Host, Unit: "frameworks-kafka"})
		}
	}
	return out
}

func kafkaClusterLabel(cluster kafkaClusterView) string {
	if region := strings.TrimSpace(cluster.RegionID); region != "" {
		return region
	}
	return cluster.Role
}

// checkKafkaQuorum reads the cluster's KRaft metadata quorum through its first
// broker and counts leadership losses in every controller's journal since
// `since`. A missing leader, a voter set that differs from the manifest, a
// lagging voter, or any leadership loss in the window makes it unhealthy;
// slow controller events alone only annotate the message.
func checkKafkaQuorum(ctx context.Context, manifest *inventory.Manifest, cluster kafkaClusterView, mode, since string, runnerFor func(inventory.Host) (ssh.Runner, error)) *health.CheckResult {
	result := &health.CheckResult{Name: "kafka_quorum", CheckedAt: time.Now(), Metadata: map[string]string{"cluster": kafkaClusterLabel(cluster)}}
	if len(cluster.Brokers) == 0 {
		result.Status, result.Error = "unhealthy", "no brokers configured"
		return result
	}
	broker := cluster.Brokers[0]
	host, ok := manifest.GetHost(broker.Host)
	if !ok {
		result.Status, result.Error = "unhealthy", fmt.Sprintf("broker host %q not in manifest", broker.Host)
		return result
	}
	runner, err := runnerFor(host)
	if err != nil {
		result.Status, result.Error = "unhealthy", fmt.Sprintf("connect to %s: %v", broker.Host, err)
		return result
	}
	port := broker.Port
	if port == 0 {
		port = 9092
	}
	controllers := kafkaQuorumControllers(cluster)
	var problems []string
	cmdResult, runErr := runner.Run(ctx, kafkaToolCommand(mode, kafkaLocalBootstrap(host, port), "kafka-metadata-quorum", "describe --status"))
	if runErr != nil || cmdResult == nil || cmdResult.ExitCode != 0 {
		problems = append(problems, "quorum status unreadable: "+diagnosticCommandError(cmdResult, runErr))
	} else if status, parseErr := parseKafkaQuorumStatus(cmdResult.Stdout); parseErr != nil {
		problems = append(problems, "quorum status unreadable: "+parseErr.Error())
	} else {
		problems = append(problems, status.problems(len(controllers))...)
		result.Metadata["leader_id"] = strconv.Itoa(status.LeaderID)
		result.Metadata["leader_epoch"] = strconv.FormatInt(status.LeaderEpoch, 10)
		result.Message = fmt.Sprintf("leader %d (epoch %d), voters %v", status.LeaderID, status.LeaderEpoch, status.Voters)
	}

	var slowNotes []string
	if !strings.EqualFold(strings.TrimSpace(mode), "docker") {
		for _, ctrl := range controllers {
			ctrlHost, found := manifest.GetHost(ctrl.Host)
			if !found {
				problems = append(problems, fmt.Sprintf("controller %d host %q not in manifest", ctrl.ID, ctrl.Host))
				continue
			}
			ctrlRunner, connErr := runnerFor(ctrlHost)
			if connErr != nil {
				problems = append(problems, fmt.Sprintf("controller %d on %s: connect: %v", ctrl.ID, ctrl.Host, connErr))
				continue
			}
			out, probeErr := ctrlRunner.Run(ctx, kafkaControllerJournalProbe(ctrl.Unit, since))
			if probeErr != nil || out == nil {
				problems = append(problems, fmt.Sprintf("controller %d on %s: journal unreadable: %s", ctrl.ID, ctrl.Host, diagnosticCommandError(out, probeErr)))
				continue
			}
			renounced, slow, parseErr := parseKafkaControllerJournalProbe(out.Stdout)
			if parseErr != nil {
				problems = append(problems, fmt.Sprintf("controller %d on %s: %v", ctrl.ID, ctrl.Host, parseErr))
				continue
			}
			if renounced > 0 {
				problems = append(problems, fmt.Sprintf("controller %d on %s gave up leadership with no successor %d time(s) since %s", ctrl.ID, ctrl.Host, renounced, since))
			}
			if slow > 0 {
				slowNotes = append(slowNotes, fmt.Sprintf("controller %d: %d slow event(s)", ctrl.ID, slow))
			}
		}
	}
	if len(slowNotes) > 0 {
		result.Metadata["slow_controller_events"] = strings.Join(slowNotes, "; ")
	}
	if len(problems) > 0 {
		result.Status = "unhealthy"
		result.Message = ""
		result.Error = strings.Join(problems, "; ")
		if len(slowNotes) > 0 {
			result.Error += " (" + strings.Join(slowNotes, "; ") + ")"
		}
		return result
	}
	result.OK, result.Status = true, "healthy"
	if len(slowNotes) > 0 {
		result.Message += "; " + strings.Join(slowNotes, "; ") + " since " + since
	}
	return result
}

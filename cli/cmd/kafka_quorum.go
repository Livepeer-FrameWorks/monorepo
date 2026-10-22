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

// kafkaQuorumFollowerLagLimit is how long a voter may go without catching up
// to the leader before the quorum counts as degraded. Healthy voters catch up
// within one fetch (sub-second); past 2.5x controller.quorum.fetch.timeout.ms
// (2 s, the Kafka default the kafka role keeps) the leader's quorum check no
// longer counts the voter, so one more voter loss makes the leader resign.
const kafkaQuorumFollowerLagLimit = 5 * time.Second

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

// kafkaControllerLogProbe reads, in one pass over a controller unit's logs
// since a point in time, how often it gave up quorum leadership, how many of
// those were followed by no new leader within the election timeout, and the
// count and largest duration of exceptionally slow controller events.
//
// The kafka role's log4j config sends the QuorumController logger only to
// controller.log and raft state transitions only to server.log (each with
// dated .gz rotations under the unit's LOG_DIR), never to the journal. A
// leader that loses the quorum always logs "the leader is (none)": it passes
// through Unattached before it learns the winner. Its successor is the first
// leader the node sees for that epoch or a later one: a server.log
// FollowerState with a leader or a Leader transition, or a controller.log
// "In the new epoch N, the leader is X" / "Becoming the active controller".
// Matching by epoch rather than line order holds while the controller event
// queue is stalled and logs the renouncement after raft already followed.
// Only files written since the cutoff can hold lines in the window.
//
// The election timeout is controller.quorum.election.timeout.ms from the
// unit's server.properties (Kafka's 1000 ms default when unset). since is a
// GNU date expression ("1 hour ago"); log4j stamps lines in host local time,
// as does date, so the cutoff compares as text.
func kafkaControllerLogProbe(unit, since string) string {
	return fmt.Sprintf(`unit=%s
cutoff=$(date -d %s '+%%Y-%%m-%%d %%H:%%M:%%S') || exit 3
dir=$(systemctl show -p Environment --value "$unit" 2>/dev/null | tr ' ' '\n' | sed -n 's/^LOG_DIR=//p' | tail -n 1)
[ -n "$dir" ] || dir=/var/log/kafka/$unit
props=$(systemctl show -p ExecStart --value "$unit" 2>/dev/null | tr ' ' '\n' | grep '\.properties$' | tail -n 1)
timeout=$(sed -n 's/^controller\.quorum\.election\.timeout\.ms=\([0-9]*\).*/\1/p' "$props" 2>/dev/null | tail -n 1)
case "$timeout" in ''|*[!0-9]*) timeout=1000 ;; esac
set -- "$dir"/controller.log*
[ -e "$1" ] || { echo "no controller.log in $dir" >&2; exit 4; }
files=$(find "$dir" -maxdepth 1 \( -name 'controller.log*' -o -name 'server.log*' \) -newermt "$cutoff") || { echo "cannot list the logs in $dir written since $cutoff" >&2; exit 5; }
printf '%%s\n' "$files" | while IFS= read -r f; do [ -n "$f" ] || continue; case "$f" in *.gz) gzip -dc "$f" ;; *) cat "$f" ;; esac; done 2>/dev/null |
  awk -v cutoff="$cutoff" -v timeout="$timeout" '
function ms(ts,   y, m, d, a, days) {
  y = substr(ts, 1, 4) + 0; m = substr(ts, 6, 2) + 0; d = substr(ts, 9, 2) + 0
  a = (m <= 2); y -= a; m += 12 * a - 3
  days = 365 * y + int(y / 4) - int(y / 100) + int(y / 400) + int((153 * m + 2) / 5) + d
  return ((days * 24 + substr(ts, 12, 2)) * 60 + substr(ts, 15, 2)) * 60000 + substr(ts, 18, 2) * 1000 + substr(ts, 21, 3)
}
function num(s, re) { if (!match(s, re)) return -1; s = substr(s, RSTART, RLENGTH); gsub(/[^0-9]/, "", s); return s + 0 }
function successor(epoch) { k++; sat[k] = ms(substr($0, 2, 23)); sepoch[k] = epoch }
/Renouncing the leadership/ {
  if (substr($0, 2, 19) < cutoff) next
  r++
  if (/the leader is \(none\)/) { n++; rat[n] = ms(substr($0, 2, 23)); repoch[n] = num($0, "new epoch [0-9]+") }
  else successor(num($0, "new epoch [0-9]+"))
  next
}
/Exceptionally slow controller event/ {
  if (substr($0, 2, 19) < cutoff) next
  s++; t = num($0, "took [0-9]+ ms"); if (t > maxslow) maxslow = t
  next
}
/In the new epoch [0-9]+, the leader is [0-9]/ { successor(num($0, "new epoch [0-9]+")); next }
/Becoming the active controller at epoch [0-9]/ { successor(num($0, "at epoch [0-9]+")); next }
/Completed transition to (FollowerState|Leader|LeaderState)\(/ {
  state = $0; sub(/ from .*/, "", state)
  if (state ~ /FollowerState\(/ && state !~ /leader(Id)?=[0-9]/) next
  successor(num(state, "epoch=[0-9]+"))
}
END {
  for (i = 1; i <= n; i++) {
    found = 0
    for (j = 1; j <= k; j++) if (sepoch[j] >= repoch[i] && sat[j] <= rat[i] + timeout) { found = 1; break }
    if (!found) l++
  }
  printf "renounced=%%d leaderless=%%d slow=%%d max_slow_ms=%%d election_timeout_ms=%%d\n", r, l, s, maxslow, timeout
}'`,
		ssh.ShellQuote(unit), ssh.ShellQuote(since))
}

// kafkaControllerLogCounts is one controller's parsed kafkaControllerLogProbe
// output. Renounced counts every leadership loss; Leaderless is the subset no
// new leader followed within ElectionTimeoutMs.
type kafkaControllerLogCounts struct {
	Renounced, Leaderless, Slow  int
	MaxSlowMs, ElectionTimeoutMs int
}

func parseKafkaControllerLogProbe(output string) (kafkaControllerLogCounts, error) {
	var counts kafkaControllerLogCounts
	fields := map[string]*int{
		"renounced":           &counts.Renounced,
		"leaderless":          &counts.Leaderless,
		"slow":                &counts.Slow,
		"max_slow_ms":         &counts.MaxSlowMs,
		"election_timeout_ms": &counts.ElectionTimeoutMs,
	}
	seen := map[string]bool{}
	for _, field := range strings.Fields(output) {
		key, value, ok := strings.Cut(field, "=")
		target, known := fields[key]
		if !ok || !known {
			continue
		}
		n, convErr := strconv.Atoi(value)
		if convErr != nil {
			return kafkaControllerLogCounts{}, fmt.Errorf("unreadable %s count %q", key, value)
		}
		*target, seen[key] = n, true
	}
	if len(seen) != len(fields) {
		return kafkaControllerLogCounts{}, fmt.Errorf("no controller log counts in %q", strings.TrimSpace(output))
	}
	return counts, nil
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
// broker and counts leadership losses in every controller's logs since
// `since`. A missing leader, a voter set that differs from the manifest, a
// lagging voter, or any leadership loss in the window makes it unhealthy, as
// cluster doctor treats every self-recovered disruption in its window (a unit
// that restarted automatically counts too); the message tells a failover with
// a successor apart from a leaderless quorum. Slow controller events and the
// largest stall alone only annotate the message.
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
			out, probeErr := ctrlRunner.Run(ctx, kafkaControllerLogProbe(ctrl.Unit, since))
			if probeErr != nil || out == nil || out.ExitCode != 0 {
				problems = append(problems, fmt.Sprintf("controller %d on %s: controller log unreadable: %s", ctrl.ID, ctrl.Host, diagnosticCommandError(out, probeErr)))
				continue
			}
			counts, parseErr := parseKafkaControllerLogProbe(out.Stdout)
			if parseErr != nil {
				problems = append(problems, fmt.Sprintf("controller %d on %s: %v", ctrl.ID, ctrl.Host, parseErr))
				continue
			}
			if counts.Leaderless > 0 {
				problems = append(problems, fmt.Sprintf("controller %d on %s was left without a leader %d time(s) since %s: it gave up leadership and saw no new leader within the %d ms election timeout", ctrl.ID, ctrl.Host, counts.Leaderless, since, counts.ElectionTimeoutMs))
			}
			if handovers := counts.Renounced - counts.Leaderless; handovers > 0 {
				problems = append(problems, fmt.Sprintf("controller %d on %s lost leadership in %d unplanned leader change(s) since %s, each followed by a new leader", ctrl.ID, ctrl.Host, handovers, since))
			}
			if counts.Slow > 0 {
				slowNotes = append(slowNotes, fmt.Sprintf("controller %d: %d slow event(s), largest stall %d ms", ctrl.ID, counts.Slow, counts.MaxSlowMs))
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

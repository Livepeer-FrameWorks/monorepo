package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/pkg/orchestrator"
)

// Release Kafka convergence applies the kafka role to every controller and
// broker of each Kafka cluster, one host at a time: controllers first, then
// brokers. Before a host changes and again before the next one starts, the
// cluster must have a quorum leader with every controller caught up and no
// under-replicated or unavailable partition, read through a broker of the
// same cluster.

// kafkaQuorumMaxLag is how many metadata records a controller may trail the
// quorum leader and still count as caught up; an idle quorum appends a few
// records a second, and a restarted controller trails by thousands.
const kafkaQuorumMaxLag = 100

var defaultKafkaGateTiming = serviceGateTiming{Timeout: 15 * time.Minute, Interval: 5 * time.Second}

// kafkaQuorumVoter is one controller row of kafka-metadata-quorum describe
// --replication.
type kafkaQuorumVoter struct {
	NodeID int
	Lag    int64
	Status string
}

// kafkaReleaseHealth is what a broker reports about its cluster.
type kafkaReleaseHealth struct {
	Voters          []kafkaQuorumVoter
	UnderReplicated []string
	Unavailable     []string
}

func (h kafkaReleaseHealth) problems(controllers int) []string {
	var problems []string
	leaders := 0
	for _, voter := range h.Voters {
		if voter.Status == "Leader" {
			leaders++
		}
		if voter.Lag > kafkaQuorumMaxLag {
			problems = append(problems, fmt.Sprintf("controller %d trails the quorum leader by %d records", voter.NodeID, voter.Lag))
		}
	}
	if leaders != 1 {
		problems = append(problems, fmt.Sprintf("%d controller quorum leader(s)", leaders))
	}
	if len(h.Voters) < controllers {
		problems = append(problems, fmt.Sprintf("%d of %d controllers in the quorum", len(h.Voters), controllers))
	}
	if len(h.UnderReplicated) > 0 {
		problems = append(problems, fmt.Sprintf("%d under-replicated partition(s) (%s)", len(h.UnderReplicated), firstFew(h.UnderReplicated)))
	}
	if len(h.Unavailable) > 0 {
		problems = append(problems, fmt.Sprintf("%d unavailable partition(s) (%s)", len(h.Unavailable), firstFew(h.Unavailable)))
	}
	return problems
}

func firstFew(items []string) string {
	if len(items) <= 3 {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:3], ", ") + ", ..."
}

// kafkaReleaseOps reads a Kafka cluster's health through one of its brokers.
type kafkaReleaseOps interface {
	Health(ctx context.Context, broker *orchestrator.Task) (kafkaReleaseHealth, error)
}

// kafkaConvergenceCluster is one Kafka cluster's tasks.
type kafkaConvergenceCluster struct {
	Label       string
	Controllers []*orchestrator.Task
	Brokers     []*orchestrator.Task
}

func kafkaConvergenceClusters(tasks []*orchestrator.Task) []kafkaConvergenceCluster {
	var clusters []kafkaConvergenceCluster
	index := map[string]int{}
	for _, task := range tasks {
		i, ok := index[task.ClusterID]
		if !ok {
			i = len(clusters)
			index[task.ClusterID] = i
			label := task.ClusterID
			if label == "" {
				label = "primary"
			}
			clusters = append(clusters, kafkaConvergenceCluster{Label: label})
		}
		if task.Type == "kafka-controller" {
			clusters[i].Controllers = append(clusters[i].Controllers, task)
		} else {
			clusters[i].Brokers = append(clusters[i].Brokers, task)
		}
	}
	return clusters
}

func describeKafkaConvergence(out io.Writer, tasks []*orchestrator.Task) {
	for _, cluster := range kafkaConvergenceClusters(tasks) {
		fmt.Fprintf(out, "     · Kafka %s, one host at a time, each gated on a quorum leader with every controller caught up and no under-replicated or unavailable partition: controllers %s, then brokers %s\n",
			cluster.Label, strings.Join(taskHosts(cluster.Controllers), " -> "), strings.Join(taskHosts(cluster.Brokers), " -> "))
	}
}

func runKafkaConvergence(ctx context.Context, c *releaseHostConvergence, tasks []*orchestrator.Task, dryRun bool) error {
	out := c.cmd.OutOrStdout()
	ops := c.kafkaOps()
	timing := c.serviceTiming(defaultKafkaGateTiming)
	clusters := kafkaConvergenceClusters(tasks)
	for i, cluster := range clusters {
		fmt.Fprintf(out, "\nKafka %s (%d/%d)\n", cluster.Label, i+1, len(clusters))
		if len(cluster.Brokers) == 0 {
			return fmt.Errorf("kafka %s has no broker to read its health through", cluster.Label)
		}
		for _, task := range append(append([]*orchestrator.Task(nil), cluster.Controllers...), cluster.Brokers...) {
			role := "broker"
			if task.Type == "kafka-controller" {
				role = "controller"
			}
			fmt.Fprintf(out, "  %s on %s\n", role, task.Host)
			gate := func() []string { return kafkaClusterProblems(ctx, ops, cluster, task) }
			if err := c.convergeGated(ctx, task, dryRun, out, timing, "Kafka "+cluster.Label, gate); err != nil {
				return fmt.Errorf("%s on %s: %w", role, task.Host, err)
			}
		}
	}
	return nil
}

// kafkaClusterProblems reads the cluster's health through its first broker
// that answers, preferring brokers other than target, which may be down.
func kafkaClusterProblems(ctx context.Context, ops kafkaReleaseOps, cluster kafkaConvergenceCluster, target *orchestrator.Task) []string {
	observers := make([]*orchestrator.Task, 0, len(cluster.Brokers))
	for _, broker := range cluster.Brokers {
		if broker.Host != target.Host {
			observers = append(observers, broker)
		}
	}
	for _, broker := range cluster.Brokers {
		if broker.Host == target.Host {
			observers = append(observers, broker)
		}
	}
	var errs []error
	for _, broker := range observers {
		health, err := ops.Health(ctx, broker)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", broker.Host, err))
			continue
		}
		return health.problems(len(cluster.Controllers))
	}
	return []string{"no broker answered: " + errors.Join(errs...).Error()}
}

func (c *releaseHostConvergence) kafkaOps() kafkaReleaseOps {
	if c.serviceOps != nil && c.serviceOps.kafka != nil {
		return c.serviceOps.kafka
	}
	return sshKafkaReleaseOps{c: c}
}

// sshKafkaReleaseOps runs the Kafka admin tools on a broker host against its
// local listener, the way the kafka role's validate and init tasks do.
type sshKafkaReleaseOps struct {
	c *releaseHostConvergence
}

const kafkaReleaseHealthScript = `set -u
bin=/opt/kafka/bin
export KAFKA_HEAP_OPTS="-Xms64m -Xmx256m" KAFKA_JMX_OPTS="-Djava.awt.headless=true" JMX_PORT=
server=127.0.0.1:%d
echo "== quorum"
"$bin/kafka-metadata-quorum.sh" --bootstrap-server "$server" describe --replication || exit 1
echo "== under-replicated"
"$bin/kafka-topics.sh" --bootstrap-server "$server" --describe --under-replicated-partitions || exit 1
echo "== unavailable"
"$bin/kafka-topics.sh" --bootstrap-server "$server" --describe --unavailable-partitions || exit 1
`

func (o sshKafkaReleaseOps) Health(ctx context.Context, broker *orchestrator.Task) (kafkaReleaseHealth, error) {
	host, ok := o.c.manifest.GetHost(broker.Host)
	if !ok {
		return kafkaReleaseHealth{}, fmt.Errorf("host %s not found in manifest", broker.Host)
	}
	config, err := buildTaskConfig(broker, o.c.manifest, o.c.runtimeData, false, o.c.manifestDir, o.c.sharedEnv, o.c.clusterEnvs, o.c.releaseRepos)
	if err != nil {
		return kafkaReleaseHealth{}, err
	}
	port := config.Port
	if port == 0 {
		port = 9092
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	result, err := o.c.pool.Run(probeCtx, sshConfigFor(host), fmt.Sprintf(kafkaReleaseHealthScript, port))
	if err != nil {
		return kafkaReleaseHealth{}, err
	}
	return parseKafkaReleaseHealth(result.Stdout)
}

// parseKafkaReleaseHealth reads the sections kafkaReleaseHealthScript prints:
// the quorum replication table, then the partition lines of each
// kafka-topics listing.
func parseKafkaReleaseHealth(out string) (kafkaReleaseHealth, error) {
	var health kafkaReleaseHealth
	section := ""
	var header []string
	sawQuorum := false
	for line := range strings.SplitSeq(strings.ReplaceAll(out, "\r", ""), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "== ") {
			section = strings.TrimPrefix(trimmed, "== ")
			continue
		}
		if trimmed == "" {
			continue
		}
		switch section {
		case "quorum":
			fields := strings.Fields(trimmed)
			if len(fields) > 0 && fields[0] == "NodeId" {
				header = fields
				sawQuorum = true
				continue
			}
			if header == nil || len(fields) != len(header) {
				continue
			}
			row := map[string]string{}
			for i, name := range header {
				row[name] = fields[i]
			}
			if row["Status"] != "Leader" && row["Status"] != "Follower" {
				continue
			}
			nodeID, err := strconv.Atoi(row["NodeId"])
			if err != nil {
				return kafkaReleaseHealth{}, fmt.Errorf("quorum row %q: %w", trimmed, err)
			}
			lag, err := strconv.ParseInt(row["Lag"], 10, 64)
			if err != nil {
				return kafkaReleaseHealth{}, fmt.Errorf("quorum row %q: %w", trimmed, err)
			}
			health.Voters = append(health.Voters, kafkaQuorumVoter{NodeID: nodeID, Lag: lag, Status: row["Status"]})
		case "under-replicated", "unavailable":
			partition, ok := kafkaPartitionLabel(trimmed)
			if !ok {
				continue
			}
			if section == "under-replicated" {
				health.UnderReplicated = append(health.UnderReplicated, partition)
			} else {
				health.Unavailable = append(health.Unavailable, partition)
			}
		}
	}
	if !sawQuorum {
		return kafkaReleaseHealth{}, fmt.Errorf("no controller quorum table in %q", strings.TrimSpace(out))
	}
	return health, nil
}

// kafkaPartitionLabel reads "topic/partition" from a kafka-topics --describe
// partition line.
func kafkaPartitionLabel(line string) (string, bool) {
	fields := strings.Fields(line)
	topic, partition := "", ""
	for i := 0; i+1 < len(fields); i++ {
		switch fields[i] {
		case "Topic:":
			topic = fields[i+1]
		case "Partition:":
			partition = fields[i+1]
		}
	}
	if topic == "" || partition == "" {
		return "", false
	}
	return topic + "/" + partition, true
}

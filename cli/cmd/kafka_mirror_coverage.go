package cmd

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// kafkaEndOffsetsCommand lists the latest offset of every partition of the
// named topics on the local broker. It only reads broker metadata.
func kafkaEndOffsetsCommand(mode string, port int, topics []string) string {
	quoted := make([]string, len(topics))
	for i, topic := range topics {
		quoted[i] = regexp.QuoteMeta(topic)
	}
	pattern := "^(" + strings.Join(quoted, "|") + ")$"
	return kafkaToolCommand(mode, port, "kafka-get-offsets", "--time -1 --topic "+ssh.ShellQuote(pattern))
}

// parseKafkaEndOffsets sums kafka-get-offsets output (topic:partition:offset
// per line) into end offsets per topic. A topic absent from the output does
// not exist on the cluster.
func parseKafkaEndOffsets(output string) (map[string]int64, error) {
	totals := map[string]int64{}
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		offsetAt := strings.LastIndex(line, ":")
		if offsetAt <= 0 {
			return nil, fmt.Errorf("unexpected kafka-get-offsets line %q", line)
		}
		partitionAt := strings.LastIndex(line[:offsetAt], ":")
		if partitionAt <= 0 {
			return nil, fmt.Errorf("unexpected kafka-get-offsets line %q", line)
		}
		offset, err := strconv.ParseInt(line[offsetAt+1:], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("unexpected kafka-get-offsets line %q: %w", line, err)
		}
		if offset > 0 {
			totals[line[:partitionAt]] += offset
		} else if _, ok := totals[line[:partitionAt]]; !ok {
			totals[line[:partitionAt]] = 0
		}
	}
	return totals, nil
}

// mirrorTopicCoverage is one mirrored topic's end offsets on both sides of a
// link. TargetExists is false when the remote topic was never created.
type mirrorTopicCoverage struct {
	Topic, RemoteTopic string
	Source, Target     int64
	TargetExists       bool
}

// Stalled reports a source topic with records whose remote copy has none,
// which is what a topic without a MirrorMaker2 replication task looks like.
func (c mirrorTopicCoverage) Stalled() bool {
	return c.Source > 0 && c.Target == 0
}

func mirrorCoverageRows(sourceAlias string, topics []string, source, target map[string]int64) []mirrorTopicCoverage {
	rows := make([]mirrorTopicCoverage, 0, len(topics))
	for _, topic := range topics {
		remote := sourceAlias + "." + topic
		targetOffset, exists := target[remote]
		rows = append(rows, mirrorTopicCoverage{
			Topic:        topic,
			RemoteTopic:  remote,
			Source:       source[topic],
			Target:       targetOffset,
			TargetExists: exists,
		})
	}
	return rows
}

// checkMirrorMakerTopicCoverage compares, for every enabled MirrorMaker2 link,
// each mirrored topic's end offset on the source cluster with its
// `<source>.<topic>` copy on the target cluster, and warns when the source has
// records but the copy has none. It is read-only and returns the number of
// links it could not probe.
func checkMirrorMakerTopicCoverage(ctx context.Context, out, errOut io.Writer, manifest *inventory.Manifest, runnerFor func(inventory.Host) (ssh.Runner, error)) int {
	if manifest == nil || manifest.Infrastructure.Kafka == nil {
		return 0
	}
	mm := manifest.Infrastructure.Kafka.MirrorMaker
	if mm == nil || !mm.Enabled || len(mm.Links) == 0 {
		return 0
	}
	fmt.Fprintln(out, "\nMirrorMaker2 topic coverage:")
	aggregatorAlias := kafkaClusterAlias(manifest, aggregatorKafkaClusterView(manifest))
	mode := manifest.Infrastructure.Kafka.Mode
	failures := 0
	for _, link := range mm.Links {
		sourceAlias, targetAlias := strings.TrimSpace(link.Source), strings.TrimSpace(link.Target)
		label := sourceAlias + " -> " + targetAlias
		topics := mirrorLinkTopics(link, aggregatorAlias)
		remoteTopics := make([]string, len(topics))
		for i, topic := range topics {
			remoteTopics[i] = sourceAlias + "." + topic
		}
		source, err := readClusterEndOffsets(ctx, manifest, sourceAlias, mode, topics, runnerFor)
		if err != nil {
			failures++
			ux.Fail(errOut, fmt.Sprintf("%s: read source end offsets: %v", label, err))
			continue
		}
		target, err := readClusterEndOffsets(ctx, manifest, targetAlias, mode, remoteTopics, runnerFor)
		if err != nil {
			failures++
			ux.Fail(errOut, fmt.Sprintf("%s: read target end offsets: %v", label, err))
			continue
		}
		stalled := 0
		for _, row := range mirrorCoverageRows(sourceAlias, topics, source, target) {
			if !row.Stalled() {
				continue
			}
			stalled++
			state := "has no records"
			if !row.TargetExists {
				state = "does not exist"
			}
			ux.Warn(out, fmt.Sprintf("%s: %s has %d record(s) on %s but %s on %s %s; MirrorMaker2 has no working replication task for it", label, row.Topic, row.Source, sourceAlias, row.RemoteTopic, targetAlias, state))
		}
		if stalled == 0 {
			ux.Success(out, fmt.Sprintf("%s: %d topic(s) mirrored", label, len(topics)))
		}
	}
	return failures
}

// readClusterEndOffsets runs kafka-get-offsets on the first broker of the
// Kafka cluster with the given alias.
func readClusterEndOffsets(ctx context.Context, manifest *inventory.Manifest, alias, mode string, topics []string, runnerFor func(inventory.Host) (ssh.Runner, error)) (map[string]int64, error) {
	cluster := kafkaClusterViewByAlias(manifest, alias)
	if cluster == nil || len(cluster.Brokers) == 0 {
		return nil, fmt.Errorf("no Kafka brokers for region %q", alias)
	}
	broker := cluster.Brokers[0]
	host, ok := manifest.GetHost(broker.Host)
	if !ok {
		return nil, fmt.Errorf("broker host %s not found in manifest", broker.Host)
	}
	runner, err := runnerFor(host)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", broker.Host, err)
	}
	port := broker.Port
	if port == 0 {
		port = 9092
	}
	sorted := append([]string(nil), topics...)
	sort.Strings(sorted)
	result, err := runner.Run(ctx, kafkaEndOffsetsCommand(mode, port, sorted))
	if err != nil || result == nil || result.ExitCode != 0 {
		return nil, fmt.Errorf("%s: %s", broker.Host, diagnosticCommandError(result, err))
	}
	return parseKafkaEndOffsets(result.Stdout)
}

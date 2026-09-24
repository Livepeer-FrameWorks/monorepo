package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// offsetsRunner answers kafka-get-offsets per broker host and records every
// command so the test can prove the check stays read-only.
type offsetsRunner struct {
	host     string
	outputs  map[string]string
	commands *[]string
}

func (r *offsetsRunner) Run(_ context.Context, command string) (*ssh.CommandResult, error) {
	*r.commands = append(*r.commands, r.host+": "+command)
	return &ssh.CommandResult{Stdout: r.outputs[r.host]}, nil
}

func (r *offsetsRunner) RunScript(context.Context, string) (*ssh.CommandResult, error) {
	return &ssh.CommandResult{ExitCode: 1}, nil
}
func (r *offsetsRunner) Upload(context.Context, ssh.UploadOptions) error { return nil }
func (r *offsetsRunner) Close() error                                    { return nil }

func TestMirrorMakerTopicCoverageWarnsOnStalledTopic(t *testing.T) {
	manifest := multiRegionReleaseManifest()
	outputs := map[string]string{
		// us-east broker: its own topics (source of us-east -> eu-west) and the
		// eu-west copies (target of eu-west -> us-east).
		"regional-us-1": strings.Join([]string{
			"analytics_events:0:40", "service_events:0:7", "analytics.raw_mist_triggers:0:3",
			"billing.usage_reports:0:1", "decklog_events_dlq:0:0", "domain.events:0:5", "domain.events:1:4",
			"eu-west.analytics_events:0:2", "eu-west.service_events:0:1", "eu-west.domain.events:0:1",
		}, "\n"),
		// eu-west broker: us-east.domain.events exists but is empty.
		"regional-eu-1": strings.Join([]string{
			"analytics_events:0:2", "service_events:0:1", "domain.events:0:1",
			"us-east.analytics_events:0:40", "us-east.service_events:0:7", "us-east.analytics.raw_mist_triggers:0:3",
			"us-east.billing.usage_reports:0:1", "us-east.domain.events:0:0", "us-east.domain.events:1:0",
		}, "\n"),
	}
	var commands []string
	var out, errOut bytes.Buffer
	failures := checkMirrorMakerTopicCoverage(context.Background(), &out, &errOut, manifest, func(host inventory.Host) (ssh.Runner, error) {
		return &offsetsRunner{host: host.Name, outputs: outputs, commands: &commands}, nil
	})
	if failures != 0 {
		t.Fatalf("failures = %d, stderr:\n%s", failures, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "us-east -> eu-west: domain.events has 9 record(s) on us-east but us-east.domain.events on eu-west has no records") {
		t.Fatalf("missing stalled-topic warning:\n%s", got)
	}
	if strings.Contains(got, "analytics_events has") || strings.Contains(got, "decklog_events_dlq has") {
		t.Fatalf("warned on a replicating or empty source topic:\n%s", got)
	}
	if !strings.Contains(got, "eu-west -> us-east: 3 topic(s) mirrored") {
		t.Fatalf("healthy link not reported:\n%s", got)
	}
	for _, command := range commands {
		if !strings.Contains(command, "/opt/kafka/bin/kafka-get-offsets.sh --bootstrap-server localhost:9092 --time -1 --topic ") {
			t.Fatalf("coverage check ran a non-offset command: %s", command)
		}
	}
	if len(commands) != 4 {
		t.Fatalf("commands = %d, want source and target probes for 2 links: %v", len(commands), commands)
	}
}

func TestMirrorMakerTopicCoverageReportsMissingRemoteTopic(t *testing.T) {
	rows := mirrorCoverageRows("us-east", []string{"domain.events"}, map[string]int64{"domain.events": 3}, map[string]int64{})
	if len(rows) != 1 || !rows[0].Stalled() || rows[0].TargetExists {
		t.Fatalf("rows = %+v, want a stalled row without a remote topic", rows)
	}
}

func TestParseKafkaEndOffsets(t *testing.T) {
	got, err := parseKafkaEndOffsets("us-east.domain.events:0:5\nus-east.domain.events:1:7\nempty:0:0\n")
	if err != nil {
		t.Fatal(err)
	}
	if got["us-east.domain.events"] != 12 {
		t.Fatalf("us-east.domain.events = %d, want 12", got["us-east.domain.events"])
	}
	if offset, ok := got["empty"]; !ok || offset != 0 {
		t.Fatalf("empty topic = %d (present %v), want present with 0", offset, ok)
	}
	if _, err := parseKafkaEndOffsets("Error: timeout"); err == nil {
		t.Fatal("expected a parse error for unexpected output")
	}
}

func TestKafkaEndOffsetsCommandQuotesTopicRegex(t *testing.T) {
	got := kafkaEndOffsetsCommand("native", 19092, []string{"domain.events", "us-east.domain.events"})
	want := `/opt/kafka/bin/kafka-get-offsets.sh --bootstrap-server localhost:19092 --time -1 --topic '^(domain\.events|us-east\.domain\.events)$'`
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

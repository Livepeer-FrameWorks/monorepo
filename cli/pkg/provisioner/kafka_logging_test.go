package provisioner

import (
	"context"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

// The broker's size cap comes from pkg/topology, and the role default a direct
// role run uses must be the same value.
func TestKafkaRoleVarsPassTopologyRetentionBytes(t *testing.T) {
	helpers := RoleBuildHelpers{
		DetectRemoteOS: func(context.Context, inventory.Host) (string, string, error) { return "linux", "amd64", nil },
		ResolveArtifact: func(string, string, string, map[string]any) (ResolvedArtifact, error) {
			return ResolvedArtifact{URL: "https://example.test/kafka.tgz", Checksum: "sha512:aa", Version: "4.2.0"}, nil
		},
	}
	vars, err := kafkaRoleVarsFor("broker")(context.Background(), inventory.Host{}, ServiceConfig{Metadata: map[string]any{}}, helpers)
	if err != nil {
		t.Fatalf("kafka role vars: %v", err)
	}
	if vars["kafka_log_retention_bytes"] != topology.PartitionRetentionBytes || vars["kafka_log_segment_bytes"] != topology.BrokerSegmentBytes {
		t.Fatalf("retention vars = %v / %v", vars["kafka_log_retention_bytes"], vars["kafka_log_segment_bytes"])
	}
	role := "ansible/collections/ansible_collections/frameworks/infra/roles/kafka/"
	defaults := readRepoFile(t, role+"defaults/main.yml")
	for _, want := range []string{"kafka_log_retention_bytes: 2147483648\n", "kafka_log_segment_bytes: 1073741824\n"} {
		if !strings.Contains(defaults, want) {
			t.Fatalf("kafka defaults must mirror pkg/topology; missing %q", want)
		}
	}
	if topology.PartitionRetentionBytes != 2147483648 || topology.BrokerSegmentBytes != 1073741824 {
		t.Fatal("pkg/topology changed; update the kafka role defaults and meta")
	}
	properties := readRepoFile(t, role+"templates/server.properties.j2")
	for _, want := range []string{"log.retention.bytes={{ kafka_log_retention_bytes }}\n", "log.segment.bytes={{ kafka_log_segment_bytes }}\n"} {
		if !strings.Contains(properties, want) {
			t.Fatalf("server.properties missing %q", want)
		}
	}
}

// Stock Kafka log4j2 rolls hourly and never deletes, so a broker or MM2 worker
// accumulates thousands of files. Both units must point log4j2 at the rendered
// config, which deletes rolled files by age and total size.
func TestKafkaAndMirrorMakerLogsRollAndDelete(t *testing.T) {
	roles := "ansible/collections/ansible_collections/frameworks/infra/roles/"
	for _, c := range []struct {
		unit, template, configure, vars, pathVar, prefix string
	}{
		{"kafka/templates/kafka.service.j2", "kafka/templates/log4j2.yaml.j2", "kafka/tasks/configure.yml", "kafka/vars/main.yml", "kafka_log4j_path", "kafka_log"},
		{"kafka_mirrormaker/templates/kafka-mirrormaker.service.j2", "kafka_mirrormaker/templates/log4j2.yaml.j2", "kafka_mirrormaker/tasks/configure.yml", "kafka_mirrormaker/vars/main.yml", "kafka_mm_log4j_path", "kafka_mm_log"},
	} {
		unit := readRepoFile(t, roles+c.unit)
		if !strings.Contains(unit, `Environment="KAFKA_LOG4J_OPTS=-Dlog4j2.configurationFile={{ `+c.pathVar+` }}"`) {
			t.Fatalf("%s must set KAFKA_LOG4J_OPTS to the rendered config:\n%s", c.unit, unit)
		}
		template := readRepoFile(t, roles+c.template)
		for _, want := range []string{
			"SizeBasedTriggeringPolicy:", "TimeBasedTriggeringPolicy:", "Delete:", `glob: "*.log.*"`,
			"age: \"{{ " + c.prefix + "_retention_age }}\"", "exceeds: \"{{ " + c.prefix + "_retention_size }}\"",
		} {
			if !strings.Contains(template, want) {
				t.Fatalf("%s missing %q", c.template, want)
			}
		}
		vars := readRepoFile(t, roles+c.vars)
		for _, want := range []string{c.prefix + "_retention_age: 7d\n", c.prefix + "_retention_size: 1 GB\n"} {
			if !strings.Contains(vars, want) {
				t.Fatalf("%s missing %q", c.vars, want)
			}
		}
		configure := readRepoFile(t, roles+c.configure)
		if !strings.Contains(configure, "src: log4j2.yaml.j2") {
			t.Fatalf("%s does not render log4j2.yaml.j2", c.configure)
		}
	}
}

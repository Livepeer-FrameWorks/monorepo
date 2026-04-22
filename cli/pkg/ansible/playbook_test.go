package ansible

import (
	"strings"
	"testing"
)

func TestBuildKafkaBrokerUnit_referencesBrokerConfigPath(t *testing.T) {
	t.Parallel()
	unit := string(BuildKafkaBrokerUnit())
	if !strings.Contains(unit, "ExecStart=/opt/kafka/bin/kafka-server-start.sh /etc/kafka/server.properties") {
		t.Errorf("broker unit ExecStart must reference /etc/kafka/server.properties; got:\n%s", unit)
	}
	if strings.Contains(unit, "/etc/kafka-controller/") {
		t.Errorf("broker unit must not reference /etc/kafka-controller/; got:\n%s", unit)
	}
	if !strings.Contains(unit, "Description=FrameWorks Kafka Broker") {
		t.Error("broker unit must keep its Description line")
	}
}

func TestBuildKafkaControllerUnit_referencesControllerConfigPath(t *testing.T) {
	t.Parallel()
	unit := string(BuildKafkaControllerUnit())
	if !strings.Contains(unit, "ExecStart=/opt/kafka/bin/kafka-server-start.sh /etc/kafka-controller/server.properties") {
		t.Errorf("controller unit ExecStart must reference /etc/kafka-controller/server.properties; got:\n%s", unit)
	}
	// Direct regression guard on the production bug: the controller unit used
	// to inherit the broker's /etc/kafka/server.properties path and crash-loop.
	if strings.Contains(unit, "/etc/kafka/server.properties") {
		t.Errorf("controller unit must not reference the broker config path /etc/kafka/server.properties; got:\n%s", unit)
	}
	if !strings.Contains(unit, "Description=FrameWorks Kafka Controller") {
		t.Error("controller unit must keep its Description line")
	}
}

func TestGenerateKafkaControllerPlaybook_wiresControllerPaths(t *testing.T) {
	t.Parallel()
	pb := GenerateKafkaControllerPlaybook(
		"4.2.0",
		100,
		"10.0.0.1",
		9093,
		"10.0.0.1:9092",
		"test-cluster-id",
		"100@10.0.0.1:9093:abc",
		"https://downloads.apache.org/kafka/4.2.0/kafka_2.13-4.2.0.tgz",
		"sha512:abc",
		DistroPackageSpec{PackageName: "default-jre-headless"},
	)
	if pb == nil || len(pb.Plays) == 0 {
		t.Fatal("playbook must have at least one play")
	}

	// Aggregate every string-shaped Args value across every task; assert that
	// the controller-specific paths appear and the broker config path does not.
	var corpus []string
	for _, task := range pb.Plays[0].Tasks {
		for _, v := range task.Args {
			if s, ok := v.(string); ok {
				corpus = append(corpus, s)
			}
		}
	}
	joined := strings.Join(corpus, "\n")

	want := []string{
		"/etc/kafka-controller/server.properties",
		"frameworks-kafka-controller.service",
	}
	for _, fragment := range want {
		if !strings.Contains(joined, fragment) {
			t.Errorf("generated controller playbook missing %q in any task arg", fragment)
		}
	}
	if strings.Contains(joined, "/etc/kafka/server.properties") {
		t.Errorf("generated controller playbook must not reference broker config path /etc/kafka/server.properties")
	}
}

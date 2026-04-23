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

	var ownershipFixFound bool
	for _, task := range pb.Plays[0].Tasks {
		if task.Module != "ansible.builtin.file" {
			continue
		}
		if task.Args["path"] == "/var/lib/kafka-controller/logs" && task.Args["recurse"] == true && task.Args["owner"] == "kafka" && task.Args["group"] == "kafka" {
			ownershipFixFound = true
			break
		}
	}
	if !ownershipFixFound {
		t.Fatal("generated controller playbook must normalize kafka controller log dir ownership recursively")
	}

	var waitTask Task
	var waitFound bool
	for _, task := range pb.Plays[0].Tasks {
		if task.Module == "ansible.builtin.wait_for" {
			waitTask = task
			waitFound = true
			break
		}
	}
	if !waitFound {
		t.Fatal("generated controller playbook must include a wait_for readiness gate")
	}
	if waitTask.Args["host"] != "127.0.0.1" {
		t.Fatalf("controller wait_for host = %v, want 127.0.0.1", waitTask.Args["host"])
	}
}

func TestGenerateKafkaBrokerPlaybook_waitsOnLoopback(t *testing.T) {
	t.Parallel()

	pb := GenerateKafkaBrokerPlaybook(
		"4.2.0",
		1,
		"10.0.0.2",
		9092,
		"10.0.0.10:9093",
		"test-cluster-id",
		nil,
		"https://downloads.apache.org/kafka/4.2.0/kafka_2.13-4.2.0.tgz",
		"sha512:abc",
		DistroPackageSpec{PackageName: "default-jre-headless"},
	)

	var waitTask Task
	var waitFound bool
	for _, task := range pb.Plays[0].Tasks {
		if task.Module == "ansible.builtin.wait_for" {
			waitTask = task
			waitFound = true
			break
		}
	}
	if !waitFound {
		t.Fatal("generated broker playbook must include a wait_for readiness gate")
	}
	if waitTask.Args["host"] != "127.0.0.1" {
		t.Fatalf("broker wait_for host = %v, want 127.0.0.1", waitTask.Args["host"])
	}
}

func TestGenerateKafkaKRaftPlaybook_waitsOnLoopback(t *testing.T) {
	t.Parallel()

	pb := GenerateKafkaKRaftPlaybook(
		"4.2.0",
		1,
		"10.0.0.3",
		9092,
		9093,
		"1@10.0.0.3:9093",
		"test-cluster-id",
		nil,
		"https://downloads.apache.org/kafka/4.2.0/kafka_2.13-4.2.0.tgz",
		"sha512:abc",
		DistroPackageSpec{PackageName: "default-jre-headless"},
	)

	var waitTask Task
	var waitFound bool
	for _, task := range pb.Plays[0].Tasks {
		if task.Module == "ansible.builtin.wait_for" {
			waitTask = task
			waitFound = true
			break
		}
	}
	if !waitFound {
		t.Fatal("generated kraft playbook must include a wait_for readiness gate")
	}
	if waitTask.Args["host"] != "127.0.0.1" {
		t.Fatalf("kraft wait_for host = %v, want 127.0.0.1", waitTask.Args["host"])
	}
}

func TestGenerateKafkaControllerPlaybook_skipsJavaPackageWhenCompatibleJavaExists(t *testing.T) {
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
		DistroPackageSpec{PackageName: "jre-openjdk-headless"},
	)
	if pb == nil || len(pb.Plays) == 0 {
		t.Fatal("playbook must have at least one play")
	}

	var probeFound bool
	var conditionalPkgFound bool
	for _, task := range pb.Plays[0].Tasks {
		if task.Name == "probe java runtime" {
			probeFound = true
			if task.Register != "frameworks_java_runtime_probe" {
				t.Fatalf("probe register = %q, want frameworks_java_runtime_probe", task.Register)
			}
		}
		if task.Module == "ansible.builtin.package" && task.Args["name"] == "jre-openjdk-headless" {
			conditionalPkgFound = true
			if task.When != "frameworks_java_runtime_probe.rc != 0" {
				t.Fatalf("java package when = %q, want probe failure gate", task.When)
			}
		}
	}

	if !probeFound {
		t.Fatal("generated controller playbook must probe java before package install")
	}
	if !conditionalPkgFound {
		t.Fatal("generated controller playbook must conditionally install the distro java package")
	}
}

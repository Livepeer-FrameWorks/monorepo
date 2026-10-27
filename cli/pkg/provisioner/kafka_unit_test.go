package provisioner

import (
	"strings"
	"testing"
)

// A host runs the broker and the controller as separate units. kafka-server-stop.sh signals every Kafka JVM on the
// host, so a unit that stops through it takes the other role down with it; each unit must stop only its own process.
func TestKafkaUnitStopsOnlyItsOwnProcess(t *testing.T) {
	unit := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/kafka/templates/kafka.service.j2")
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "ExecStop") && strings.Contains(line, "kafka-server-stop.sh") {
			t.Fatalf("kafka.service.j2 stops through kafka-server-stop.sh, which also stops the host's other Kafka role: %s", line)
		}
	}
	if !strings.Contains(unit, "SuccessExitStatus=143") {
		t.Fatalf("kafka.service.j2 must treat the JVM's exit after SIGTERM as a clean stop:\n%s", unit)
	}
}

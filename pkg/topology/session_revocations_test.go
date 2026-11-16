package topology

import "testing"

func TestRevocationConsumersDeclareAuthorityAndKafkaIndependentlyOfSignalman(t *testing.T) {
	for _, service := range []string{"quartermaster", "purser", "periscope-query", "bosun", "lookout", "foghorn", "skipper"} {
		t.Run(service, func(t *testing.T) {
			authority, broker := false, false
			for _, dep := range ServiceDependencies(service) {
				if dep.TargetServiceID == "signalman" {
					t.Error("backend auth unexpectedly depends on realtime hub")
				}
				if dep.TargetServiceID == "commodore" && dep.EnvKey == "COMMODORE_GRPC_ADDR" && !dep.Optional {
					authority = true
				}
			}
			for _, dep := range InfraDependencies(service) {
				if dep.Kind == InfraKafka && !dep.Optional {
					broker = true
				}
			}
			if !authority {
				t.Error("standalone auth consumer lacks its authority dependency")
			}
			if !broker {
				t.Error("standalone auth consumer lacks its Kafka dependency")
			}
		})
	}
}

package cmd

import (
	"strings"
	"testing"
)

const healthyYugabyteMastersJSON = `{"masters":[{"instance_id":{"permanent_uuid":"uuid-a"},"registration":{"private_rpc_addresses":[{"host":"10.0.0.1","port":7100}]},"role":"LEADER"},{"instance_id":{"permanent_uuid":"uuid-b"},"registration":{"private_rpc_addresses":[{"host":"10.0.0.2","port":7100}]},"role":"FOLLOWER"},{"instance_id":{"permanent_uuid":"uuid-c"},"registration":{"private_rpc_addresses":[{"host":"10.0.0.3","port":7100}]},"role":"FOLLOWER"}]}`

const healthyYugabyteRaftConfig = `Current raft config: current_term: 12 leader_uuid: "uuid-a" config { opid_index: 42 peers { permanent_uuid: "uuid-a" member_type: VOTER last_known_private_addr { host: "10.0.0.1" port: 7100 } } peers { permanent_uuid: "uuid-b" member_type: VOTER last_known_private_addr { host: "10.0.0.2" port: 7100 } } peers { permanent_uuid: "uuid-c" member_type: VOTER last_known_private_addr { host: "10.0.0.3" port: 7100 } } }`

func yugabyteProbeFixture(liveJSON, raftConfig string) string {
	return yugabyteLiveMastersMarker + "\n" + liveJSON + "\n" + yugabyteRaftConfigMarker + "\n" + raftConfig + "\n"
}

func TestParseAndValidateYugabyteConsensusAcceptsMatchingVoters(t *testing.T) {
	consensus, err := parseAndValidateYugabyteConsensus(
		yugabyteProbeFixture(healthyYugabyteMastersJSON, healthyYugabyteRaftConfig),
		[]string{"10.0.0.1:7100", "10.0.0.2:7100", "10.0.0.3:7100"},
	)
	if err != nil {
		t.Fatalf("parse consensus: %v", err)
	}
	if consensus.Term != 12 || consensus.OpID != 42 || consensus.LeaderUUID != "uuid-a" {
		t.Fatalf("unexpected consensus: %+v", consensus)
	}
	if len(consensus.Peers) != 3 {
		t.Fatalf("peer count = %d, want 3", len(consensus.Peers))
	}
}

func TestParseAndValidateYugabyteConsensusRejectsReplacedIdentity(t *testing.T) {
	live := strings.Replace(healthyYugabyteMastersJSON, `"permanent_uuid":"uuid-b"`, `"permanent_uuid":"replacement-b"`, 1)
	_, err := parseAndValidateYugabyteConsensus(
		yugabyteProbeFixture(live, healthyYugabyteRaftConfig),
		[]string{"10.0.0.1:7100", "10.0.0.2:7100", "10.0.0.3:7100"},
	)
	if err == nil || !strings.Contains(err.Error(), "master identity mismatch at 10.0.0.2:7100") {
		t.Fatalf("error = %v, want identity mismatch", err)
	}
}

func TestParseAndValidateYugabyteConsensusRejectsStaleCommittedAddress(t *testing.T) {
	raft := strings.Replace(healthyYugabyteRaftConfig, `host: "10.0.0.2"`, `host: "10.0.0.9"`, 1)
	_, err := parseAndValidateYugabyteConsensus(
		yugabyteProbeFixture(healthyYugabyteMastersJSON, raft),
		[]string{"10.0.0.1:7100", "10.0.0.2:7100", "10.0.0.3:7100"},
	)
	if err == nil || !strings.Contains(err.Error(), "membership differs from manifest") {
		t.Fatalf("error = %v, want membership mismatch", err)
	}
}

func TestParseAndValidateYugabyteConsensusRejectsDivergentCommittedConfigs(t *testing.T) {
	divergent := strings.Replace(healthyYugabyteRaftConfig, "opid_index: 42", "opid_index: 43", 1)
	_, err := parseAndValidateYugabyteConsensus(
		yugabyteProbeFixture(healthyYugabyteMastersJSON, healthyYugabyteRaftConfig+"\n"+divergent),
		[]string{"10.0.0.1:7100", "10.0.0.2:7100", "10.0.0.3:7100"},
	)
	if err == nil || !strings.Contains(err.Error(), "disagree on committed Raft config") {
		t.Fatalf("error = %v, want divergent config failure", err)
	}
}

func TestYugabyteConsensusProbeUsesManifestAddressesAndStructuredEndpoint(t *testing.T) {
	command := yugabyteConsensusProbeCommand("10.0.0.1:7100,10.0.0.2:7100", "10.0.0.2")
	for _, want := range []string{
		"--master_addresses '10.0.0.1:7100,10.0.0.2:7100'",
		"http://10.0.0.2:7000/api/v1/masters",
		"dump_masters_state CONSOLE",
		"^Current raft config:",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("probe command missing %q:\n%s", want, command)
		}
	}
}

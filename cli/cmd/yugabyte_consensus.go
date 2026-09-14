package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
)

const (
	yugabyteLiveMastersMarker = "__FRAMEWORKS_LIVE_MASTERS__"
	yugabyteRaftConfigMarker  = "__FRAMEWORKS_MASTER_RAFT_CONFIG__"
)

var (
	yugabyteRaftHeaderPattern = regexp.MustCompile(`^Current raft config:\s*current_term:\s*([0-9]+)\s+leader_uuid:\s*"([^"]+)"\s+config\s*\{\s*opid_index:\s*(-?[0-9]+)\s+(.*)\s*\}\s*$`)
	yugabyteRaftPeerPattern   = regexp.MustCompile(`peers\s*\{\s*permanent_uuid:\s*"([^"]+)"\s+member_type:\s*([A-Z_]+)\s+last_known_private_addr\s*\{\s*host:\s*"([^"]+)"\s+port:\s*([0-9]+)\s*\}`)
)

type yugabyteMasterRegistration struct {
	Host string
	Port int
	UUID string
	Role string
}

type yugabyteMasterConsensus struct {
	Term       int64
	OpID       int64
	LeaderUUID string
	Peers      map[string]yugabyteMasterRegistration
}

type yugabyteMastersResponse struct {
	Masters []struct {
		InstanceID struct {
			PermanentUUID string `json:"permanent_uuid"`
		} `json:"instance_id"`
		Registration struct {
			PrivateRPCAddresses []struct {
				Host string `json:"host"`
				Port int    `json:"port"`
			} `json:"private_rpc_addresses"`
		} `json:"registration"`
		Role string `json:"role"`
	} `json:"masters"`
}

func auditYugabyteMasterConsensus(ctx context.Context, manifest *inventory.Manifest, pool *ssh.Pool) (*yugabyteMasterConsensus, error) {
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled || !pg.IsYugabyte() || len(pg.Nodes) == 0 {
		return nil, nil
	}

	masterAddresses := pg.MasterAddresses(manifest.MeshAddress)
	expected := strings.Split(masterAddresses, ",")
	base := provisioner.NewBaseProvisioner("yugabyte-consensus", pool)
	errorsByHost := make([]string, 0, len(pg.Nodes))

	for _, node := range pg.Nodes {
		host, ok := manifest.GetHost(node.Host)
		if !ok {
			errorsByHost = append(errorsByHost, fmt.Sprintf("%s: host not found in manifest", node.Host))
			continue
		}
		meshHost := manifest.MeshAddress(node.Host)
		if strings.TrimSpace(meshHost) == "" {
			errorsByHost = append(errorsByHost, fmt.Sprintf("%s: mesh address is empty", node.Host))
			continue
		}

		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		result, err := base.RunCommand(probeCtx, host, yugabyteConsensusProbeCommand(masterAddresses, meshHost))
		cancel()
		if err != nil {
			errorsByHost = append(errorsByHost, fmt.Sprintf("%s: %v", node.Host, err))
			continue
		}
		consensus, err := parseAndValidateYugabyteConsensus(result.Stdout, expected)
		if err != nil {
			errorsByHost = append(errorsByHost, fmt.Sprintf("%s: %v", node.Host, err))
			continue
		}
		return consensus, nil
	}

	return nil, fmt.Errorf("no Yugabyte master returned a valid consensus audit:\n  %s", strings.Join(errorsByHost, "\n  "))
}

func yugabyteConsensusProbeCommand(masterAddresses, localMasterHost string) string {
	masters := ssh.ShellQuote(masterAddresses)
	mastersURL := ssh.ShellQuote("http://" + net.JoinHostPort(localMasterHost, "7000") + "/api/v1/masters")
	return fmt.Sprintf(`set -eu
admin=/opt/yugabyte/bin/yb-admin
state_file="$(mktemp)"
trap 'rm -f "$state_file"' EXIT
echo %s
curl -fsS --max-time 10 %s
echo
if ! "$admin" --master_addresses %s dump_masters_state CONSOLE >"$state_file" 2>&1; then
  cat "$state_file" >&2
  exit 1
fi
echo %s
grep '^Current raft config:' "$state_file" | sort -u
`, yugabyteLiveMastersMarker, mastersURL, masters, yugabyteRaftConfigMarker)
}

func parseAndValidateYugabyteConsensus(output string, expectedAddresses []string) (*yugabyteMasterConsensus, error) {
	liveJSON, raftLines, err := splitYugabyteConsensusProbe(output)
	if err != nil {
		return nil, err
	}

	var liveResponse yugabyteMastersResponse
	if decodeErr := json.Unmarshal([]byte(liveJSON), &liveResponse); decodeErr != nil {
		return nil, fmt.Errorf("decode live master registrations: %w", decodeErr)
	}
	live, liveLeaderUUID, err := normalizeLiveYugabyteMasters(liveResponse)
	if err != nil {
		return nil, err
	}
	consensus, err := parseYugabyteRaftConfig(raftLines)
	if err != nil {
		return nil, err
	}

	expected := make(map[string]struct{}, len(expectedAddresses))
	for _, address := range expectedAddresses {
		address = strings.TrimSpace(address)
		if address == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(address); err != nil {
			return nil, fmt.Errorf("invalid expected master address %q: %w", address, err)
		}
		expected[address] = struct{}{}
	}
	if len(expected) == 0 {
		return nil, fmt.Errorf("manifest has no Yugabyte master addresses")
	}

	if err := compareYugabyteMasterSets(expected, consensus.Peers, live); err != nil {
		return nil, err
	}
	for address, peer := range consensus.Peers {
		registration := live[address]
		if peer.Role != "VOTER" {
			return nil, fmt.Errorf("committed master %s (%s) is %s, want VOTER", address, peer.UUID, peer.Role)
		}
		if registration.UUID != peer.UUID {
			return nil, fmt.Errorf("master identity mismatch at %s: committed UUID %s, live UUID %s", address, peer.UUID, registration.UUID)
		}
	}
	if consensus.LeaderUUID != liveLeaderUUID {
		return nil, fmt.Errorf("leader identity mismatch: committed leader %s, live leader %s", consensus.LeaderUUID, liveLeaderUUID)
	}
	if consensus.Term <= 0 || consensus.OpID < 0 {
		return nil, fmt.Errorf("master Raft config is not committed: term=%d opid=%d", consensus.Term, consensus.OpID)
	}
	return consensus, nil
}

func splitYugabyteConsensusProbe(output string) (string, []string, error) {
	liveStart := strings.Index(output, yugabyteLiveMastersMarker)
	raftStart := strings.Index(output, yugabyteRaftConfigMarker)
	if liveStart < 0 || raftStart < 0 || raftStart <= liveStart {
		return "", nil, fmt.Errorf("yugabyte consensus probe returned an incomplete response")
	}
	liveJSON := strings.TrimSpace(output[liveStart+len(yugabyteLiveMastersMarker) : raftStart])
	var raftLines []string
	for _, line := range strings.Split(output[raftStart+len(yugabyteRaftConfigMarker):], "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			raftLines = append(raftLines, line)
		}
	}
	if liveJSON == "" || len(raftLines) == 0 {
		return "", nil, fmt.Errorf("yugabyte consensus probe returned empty live or committed state")
	}
	return liveJSON, raftLines, nil
}

func normalizeLiveYugabyteMasters(response yugabyteMastersResponse) (map[string]yugabyteMasterRegistration, string, error) {
	live := make(map[string]yugabyteMasterRegistration, len(response.Masters))
	leaderUUID := ""
	for _, master := range response.Masters {
		if len(master.Registration.PrivateRPCAddresses) == 0 {
			return nil, "", fmt.Errorf("live master %s has no private RPC address", master.InstanceID.PermanentUUID)
		}
		addr := master.Registration.PrivateRPCAddresses[0]
		address := net.JoinHostPort(addr.Host, strconv.Itoa(addr.Port))
		if _, exists := live[address]; exists {
			return nil, "", fmt.Errorf("duplicate live master registration for %s", address)
		}
		registration := yugabyteMasterRegistration{Host: addr.Host, Port: addr.Port, UUID: master.InstanceID.PermanentUUID, Role: master.Role}
		live[address] = registration
		if master.Role == "LEADER" {
			if leaderUUID != "" {
				return nil, "", fmt.Errorf("multiple live Yugabyte masters report LEADER")
			}
			leaderUUID = registration.UUID
		}
	}
	if leaderUUID == "" {
		return nil, "", fmt.Errorf("no live Yugabyte master reports LEADER")
	}
	return live, leaderUUID, nil
}

func parseYugabyteRaftConfig(lines []string) (*yugabyteMasterConsensus, error) {
	unique := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		unique[line] = struct{}{}
	}
	if len(unique) != 1 {
		return nil, fmt.Errorf("masters disagree on committed Raft config (%d distinct configs)", len(unique))
	}
	var line string
	for value := range unique {
		line = value
	}

	header := yugabyteRaftHeaderPattern.FindStringSubmatch(line)
	if len(header) != 5 {
		return nil, fmt.Errorf("cannot parse committed master Raft config")
	}
	term, err := strconv.ParseInt(header[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse master Raft term: %w", err)
	}
	opID, err := strconv.ParseInt(header[3], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse master Raft opid: %w", err)
	}
	consensus := &yugabyteMasterConsensus{Term: term, OpID: opID, LeaderUUID: header[2], Peers: map[string]yugabyteMasterRegistration{}}
	for _, match := range yugabyteRaftPeerPattern.FindAllStringSubmatch(header[4], -1) {
		port, err := strconv.Atoi(match[4])
		if err != nil {
			return nil, fmt.Errorf("parse master RPC port: %w", err)
		}
		address := net.JoinHostPort(match[3], strconv.Itoa(port))
		if _, exists := consensus.Peers[address]; exists {
			return nil, fmt.Errorf("duplicate committed master peer for %s", address)
		}
		consensus.Peers[address] = yugabyteMasterRegistration{Host: match[3], Port: port, UUID: match[1], Role: match[2]}
	}
	if len(consensus.Peers) == 0 {
		return nil, fmt.Errorf("committed master Raft config contains no peers")
	}
	return consensus, nil
}

func compareYugabyteMasterSets(expected map[string]struct{}, committed, live map[string]yugabyteMasterRegistration) error {
	missingCommitted, unexpectedCommitted := diffYugabyteMasterSet(expected, committed)
	missingLive, unexpectedLive := diffYugabyteMasterSet(expected, live)
	if len(missingCommitted)+len(unexpectedCommitted)+len(missingLive)+len(unexpectedLive) == 0 {
		return nil
	}
	return fmt.Errorf("master membership differs from manifest: committed missing=%v unexpected=%v; live missing=%v unexpected=%v", missingCommitted, unexpectedCommitted, missingLive, unexpectedLive)
}

func diffYugabyteMasterSet(expected map[string]struct{}, actual map[string]yugabyteMasterRegistration) ([]string, []string) {
	missing := make([]string, 0)
	unexpected := make([]string, 0)
	for address := range expected {
		if _, ok := actual[address]; !ok {
			missing = append(missing, address)
		}
	}
	for address := range actual {
		if _, ok := expected[address]; !ok {
			unexpected = append(unexpected, address)
		}
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	return missing, unexpected
}

func doctorYugabyteMasterConsensus(ctx context.Context, manifest *inventory.Manifest, pool *ssh.Pool) *health.CheckResult {
	result := &health.CheckResult{Name: "yugabyte_master_consensus", CheckedAt: time.Now(), Metadata: map[string]string{"check_kind": "raft_membership"}}
	consensus, err := auditYugabyteMasterConsensus(ctx, manifest, pool)
	if err != nil {
		result.Status = "unhealthy"
		result.Error = err.Error()
		return result
	}
	result.OK = true
	result.Status = "healthy"
	result.Message = fmt.Sprintf("%d live voter(s) match committed Raft config (term=%d, opid=%d)", len(consensus.Peers), consensus.Term, consensus.OpID)
	result.Metadata["leader_uuid"] = consensus.LeaderUUID
	return result
}

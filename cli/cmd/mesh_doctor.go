package cmd

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sort"
	"strings"
	"text/tabwriter"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/pkg/mesh/wgpolicy"
	pb "frameworks/pkg/proto"

	"github.com/spf13/cobra"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// newMeshDoctorCmd builds 'mesh doctor': for each host in the GitOps
// manifest, simulate the wireguard.Config that Quartermaster would return
// on SyncMesh and run it through the same policy rules Privateer enforces
// at apply time. Surfaces per-peer policy violations without requiring a
// deploy or any private-key material.
func newMeshDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Simulate the runtime apply for each manifest host and report policy violations",
		Long: `For each host declared in the GitOps cluster manifest, build the
peer set Quartermaster would return on SyncMesh (same-cluster active
nodes with WireGuard configured) and run the same FrameWorks policy
rules that Privateer enforces before touching wg0. Two layers run per
host:

  Self identity (matches what runtime apply checks):
    - Self address is IPv4 /32
    - Listen port 1-65535
    - Quartermaster has a row for this host
    - Manifest's stored WG identity matches Quartermaster's

  Peer set (against the host's public key):
    - No self-peer, no duplicate peer keys
    - Each peer has an endpoint
    - AllowedIPs are IPv4 /32 with no host bits

Doctor's policy logic uses each host's public key only — the rules live
in pkg/mesh/wgpolicy and are shared with the Privateer runtime, so
'doctor passed' means 'Privateer would accept this'. Mesh-CIDR
containment is a manifest-shape concern owned by 'mesh wg check' and
'mesh wg generate', not by runtime apply, so doctor does not enforce it.

Loading the manifest itself uses the same SOPS path as audit and status
(the underlying inventory loader needs the age key for SOPS-encrypted
hosts.enc.yaml); doctor does not handle private-key bytes beyond what
manifest loading already does.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()

			hostNames := meshCheckHostNames(rc.Manifest)
			// Doctor does its own per-host identity validation through
			// wgpolicy.ValidateIdentity; it deliberately does NOT call
			// mesh.ValidateIdentity here because that helper enforces
			// manifest-shape constraints (e.g. private-key derivation
			// match) that audit and generate own. Doctor's job is to
			// answer "would Privateer accept this", not "is the manifest
			// well-formed".

			client, err := getMeshQuartermasterGRPCClient()
			if err != nil {
				return fmt.Errorf("connect to Quartermaster: %w", err)
			}
			defer client.Close()

			resp, err := client.ListNodes(context.Background(), "", "", "", nil)
			if err != nil {
				return fmt.Errorf("list infrastructure_nodes: %w", err)
			}

			results := diagnoseManifest(rc.Manifest, hostNames, resp.GetNodes())
			printDoctorReport(cmd.OutOrStdout(), results)

			failed := 0
			for _, r := range results {
				if !r.identityOK || !r.peersOK {
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("mesh doctor: %d host(s) would fail policy validation", failed)
			}
			ux.Success(cmd.OutOrStdout(), fmt.Sprintf("mesh doctor: %d host(s) would pass policy validation", len(results)))
			return nil
		},
	}
}

// doctorResult is the per-host outcome of simulating an apply.
type doctorResult struct {
	host          string
	clusterID     string
	identityOK    bool
	peersOK       bool
	peerCount     int
	identityIssue string
	peerIssue     string
}

// diagnoseManifest walks each manifest host, constructs the peer set
// Quartermaster would return for it (same-cluster nodes with WireGuard
// identity, excluding self), and runs wgpolicy validation. Self public
// key comes from the manifest — no private-key material is touched.
func diagnoseManifest(manifest *inventory.Manifest, hostNames []string, qmNodes []*pb.InfrastructureNode) []doctorResult {
	// Index QM rows by (cluster, node_name) for cluster-scoped peer lookup.
	type qmKeyInner struct{ cluster, name string }
	byKey := make(map[qmKeyInner]*pb.InfrastructureNode, len(qmNodes))
	byCluster := make(map[string][]*pb.InfrastructureNode)
	for _, n := range qmNodes {
		byKey[qmKeyInner{cluster: n.GetClusterId(), name: n.GetNodeName()}] = n
		byCluster[n.GetClusterId()] = append(byCluster[n.GetClusterId()], n)
	}

	out := make([]doctorResult, 0, len(hostNames))
	for _, name := range hostNames {
		host := manifest.Hosts[name]
		clusterID := manifest.HostCluster(name)
		// Fall back to the auto-generated cluster ID for single-cluster manifests.
		if clusterID == "" && len(manifest.Clusters) == 0 {
			ids := manifest.AllClusterIDs()
			if len(ids) == 1 {
				clusterID = ids[0]
			}
		}

		res := doctorResult{host: name, clusterID: clusterID}

		// Identity: build a Config with a placeholder valid private key
		// (ValidateIdentity only checks PrivateKey != zero, not its
		// derivation). Tooling does not have access to the actual private
		// key and shouldn't need it for shape checks.
		placeholder, genErr := wgtypes.GeneratePrivateKey()
		if genErr != nil {
			res.identityIssue = fmt.Sprintf("placeholder key generation: %v", genErr)
			out = append(out, res)
			continue
		}
		selfAddrText := fmt.Sprintf("%s/32", host.WireguardIP)
		selfAddr, prefixErr := netip.ParsePrefix(selfAddrText)
		if prefixErr != nil {
			res.identityIssue = fmt.Sprintf("invalid wireguard_ip %q: %v", host.WireguardIP, prefixErr)
			out = append(out, res)
			continue
		}
		identityCfg := wgpolicy.Config{
			PrivateKey: placeholder,
			Address:    selfAddr,
			ListenPort: int(host.WireguardPort),
		}
		if err := wgpolicy.ValidateIdentity(identityCfg); err != nil {
			res.identityIssue = err.Error()
			out = append(out, res)
			continue
		}

		// Cross-check the manifest self identity against Quartermaster's
		// stored row. SyncMesh would reject a host that has no row
		// (NotFound) or whose stored WG identity diverges from what the
		// agent reports (FailedPrecondition); doctor surfaces those
		// without needing to actually issue SyncMesh.
		selfQM := byKey[qmKeyInner{cluster: clusterID, name: name}]
		if selfQM == nil {
			res.identityIssue = "no Quartermaster row for this host (real SyncMesh would return NotFound — provision the node first)"
			out = append(out, res)
			continue
		}
		if mismatch := selfIdentityMismatch(host, selfQM); mismatch != "" {
			res.identityIssue = mismatch
			out = append(out, res)
			continue
		}
		res.identityOK = true

		// Self public key from the manifest — used for the no-self-peer
		// check inside ValidatePeers without needing the private key.
		selfPub, pubErr := wgtypes.ParseKey(host.WireguardPublicKey)
		if pubErr != nil {
			res.peerIssue = fmt.Sprintf("manifest wireguard_public_key parse: %v", pubErr)
			out = append(out, res)
			continue
		}

		// Peer set: same logic Quartermaster's SyncMesh applies — every
		// active QM node in the cluster except the requesting host.
		peers, peerBuildErr := buildPeerSetForHost(byCluster[clusterID], selfQM)
		if peerBuildErr != nil {
			res.peerIssue = peerBuildErr.Error()
			out = append(out, res)
			continue
		}
		res.peerCount = len(peers)
		if err := wgpolicy.ValidatePeers(peers, selfPub); err != nil {
			res.peerIssue = err.Error()
		} else {
			res.peersOK = true
		}

		out = append(out, res)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].clusterID != out[j].clusterID {
			return out[i].clusterID < out[j].clusterID
		}
		return out[i].host < out[j].host
	})
	return out
}

// selfIdentityMismatch reports the SyncMesh-equivalent precondition
// failures that would block a host before any peers are returned: the
// manifest's wireguard_ip, wireguard_public_key, or wireguard_port not
// matching the row Quartermaster stored. Returns "" if everything
// matches.
func selfIdentityMismatch(host inventory.Host, qm *pb.InfrastructureNode) string {
	qmIP := ""
	if qm.WireguardIp != nil {
		qmIP = *qm.WireguardIp
	}
	qmPubKey := ""
	if qm.WireguardPublicKey != nil {
		qmPubKey = *qm.WireguardPublicKey
	}
	qmPort := int32(0)
	if qm.WireguardPort != nil {
		qmPort = *qm.WireguardPort
	}
	var diffs []string
	if host.WireguardIP != qmIP {
		diffs = append(diffs, fmt.Sprintf("wireguard_ip GitOps=%q QM=%q", host.WireguardIP, qmIP))
	}
	if host.WireguardPublicKey != qmPubKey {
		diffs = append(diffs, fmt.Sprintf("wireguard_public_key GitOps=%q QM=%q", host.WireguardPublicKey, qmPubKey))
	}
	if int32(host.WireguardPort) != qmPort {
		diffs = append(diffs, fmt.Sprintf("wireguard_port GitOps=%d QM=%d", host.WireguardPort, qmPort))
	}
	if len(diffs) == 0 {
		return ""
	}
	return "manifest diverges from Quartermaster (real SyncMesh would FailedPrecondition): " + strings.Join(diffs, "; ")
}

// buildPeerSetForHost mirrors Quartermaster's SyncMesh peer construction:
// every active QM node in the same cluster except self, with endpoint
// built from external_ip (preferred) or internal_ip and AllowedIPs =
// wireguard_ip/32. Nodes with missing required fields are skipped
// silently here — those exclusions are surfaced by 'mesh wg audit' and
// Quartermaster's own SyncMesh logging, not by doctor.
func buildPeerSetForHost(clusterNodes []*pb.InfrastructureNode, self *pb.InfrastructureNode) ([]wgpolicy.Peer, error) {
	selfNodeID := ""
	if self != nil {
		selfNodeID = self.GetNodeId()
	}
	var peers []wgpolicy.Peer
	for _, n := range clusterNodes {
		if n.GetNodeId() == selfNodeID {
			continue
		}
		// SyncMesh's SQL filters peers to status='active'; doctor must
		// match or it will simulate peers Privateer would never receive.
		if n.GetStatus() != "active" {
			continue
		}
		pubText := ""
		if n.WireguardPublicKey != nil {
			pubText = *n.WireguardPublicKey
		}
		if pubText == "" {
			continue
		}
		pub, err := wgtypes.ParseKey(pubText)
		if err != nil {
			return nil, fmt.Errorf("peer %q: parse public key: %w", n.GetNodeName(), err)
		}

		endpointHost := ""
		if n.ExternalIp != nil && *n.ExternalIp != "" {
			endpointHost = *n.ExternalIp
		} else if n.InternalIp != nil && *n.InternalIp != "" {
			endpointHost = *n.InternalIp
		}
		if endpointHost == "" {
			continue
		}
		port := int32(51820)
		if n.WireguardPort != nil && *n.WireguardPort > 0 {
			port = *n.WireguardPort
		}
		ap, err := netip.ParseAddrPort(fmt.Sprintf("%s:%d", endpointHost, port))
		if err != nil {
			return nil, fmt.Errorf("peer %q: parse endpoint: %w", n.GetNodeName(), err)
		}
		ep := net.UDPAddrFromAddrPort(ap)

		if n.WireguardIp == nil || *n.WireguardIp == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(*n.WireguardIp + "/32")
		if err != nil {
			return nil, fmt.Errorf("peer %q: parse wireguard_ip: %w", n.GetNodeName(), err)
		}

		peers = append(peers, wgpolicy.Peer{
			PublicKey:  pub,
			Endpoint:   ep,
			AllowedIPs: []net.IPNet{*ipnet},
			KeepAlive:  25,
		})
	}
	return peers, nil
}

func printDoctorReport(w io.Writer, results []doctorResult) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CLUSTER\tHOST\tIDENTITY\tPEERS\tDETAIL")
	for _, r := range results {
		cluster := r.clusterID
		if cluster == "" {
			cluster = "-"
		}
		identity := "FAIL"
		if r.identityOK {
			identity = "ok"
		}
		peerCol := "-"
		if r.identityOK || r.peerCount > 0 {
			if r.peersOK {
				peerCol = fmt.Sprintf("ok (%d peers)", r.peerCount)
			} else {
				peerCol = "FAIL"
			}
		}
		detail := r.identityIssue
		if detail == "" {
			detail = r.peerIssue
		}
		if detail == "" {
			detail = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", cluster, r.host, identity, peerCol, detail)
	}
	tw.Flush()
}

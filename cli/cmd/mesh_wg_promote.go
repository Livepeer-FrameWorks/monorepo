package cmd

import (
	"context"
	"fmt"
	"strings"

	"frameworks/cli/internal/ux"
	pb "frameworks/pkg/proto"

	"github.com/spf13/cobra"
)

// newMeshWgPromoteCmd finishes the adopted_local → gitops_seed promotion by
// flipping Quartermaster's enrollment_origin for a host whose public identity
// now matches the manifest (i.e., a prior `mesh wg rotate` + `cluster
// provision` cycle has already propagated the SOPS-managed key to the host
// and Privateer has SyncMesh'd the new public key back).
//
// The command is deliberately separate from `rotate`: rotate writes files,
// promote verifies convergence. Flipping origin at rotate time would claim
// GitOps authority before the running node has actually adopted the new key.
func newMeshWgPromoteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "promote <host>",
		Short: "Finish adopted_local → gitops_seed promotion after rotate + provision",
		Long: `Verifies that Quartermaster's recorded WireGuard public key for <host>
matches the manifest (i.e. Privateer on the target has synced the
SOPS-managed key written by a prior 'mesh wg rotate') and, on match,
flips enrollment_origin from adopted_local to gitops_seed.

Before running this, you must have:

  1. run 'frameworks mesh wg rotate <host>' to write a new SOPS key and
     clear the preserve-key markers, and
  2. run 'frameworks cluster provision' so Ansible renders the new
     /etc/privateer/wg.key and Privateer has SyncMesh'd at least once.

If QM's recorded public key hasn't converged yet, promote fails with a
retry-after-SyncMesh message rather than flipping a stale origin.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hostName := strings.TrimSpace(args[0])
			if hostName == "" {
				return fmt.Errorf("host name is required")
			}

			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()

			host, ok := rc.Manifest.Hosts[hostName]
			if !ok {
				return fmt.Errorf("host %q not found in manifest", hostName)
			}
			if strings.TrimSpace(host.WireguardPublicKey) == "" {
				return fmt.Errorf("host %q has no wireguard_public_key in the manifest — run `frameworks mesh wg generate` first", hostName)
			}
			clusterID := rc.Manifest.HostCluster(hostName)

			client, err := getMeshQuartermasterGRPCClient()
			if err != nil {
				return fmt.Errorf("connect to Quartermaster: %w", err)
			}
			defer client.Close()

			qmNode, err := findQMNode(cmd.Context(), client, hostName, clusterID)
			if err != nil {
				return err
			}

			origin := qmNode.GetEnrollmentOrigin()
			switch origin {
			case enrollmentOriginGitopsSeed:
				ux.Success(cmd.OutOrStdout(), fmt.Sprintf("mesh wg promote: %s is already gitops_seed — nothing to do", hostName))
				return nil
			case enrollmentOriginRuntimeEnrolled:
				return fmt.Errorf("host %q is runtime_enrolled — run `frameworks mesh reconcile --write-gitops` to adopt it into GitOps first", hostName)
			case enrollmentOriginAdoptedLocal:
				// fall through to verification + flip
			default:
				return fmt.Errorf("host %q has unexpected enrollment_origin=%q; promote only handles adopted_local", hostName, origin)
			}

			qmPubKey := ""
			if qmNode.WireguardPublicKey != nil {
				qmPubKey = *qmNode.WireguardPublicKey
			}
			if qmPubKey != host.WireguardPublicKey {
				return fmt.Errorf("host %q has not yet converged on the new key: manifest=%q Quartermaster=%q. Wait for the next SyncMesh tick (up to 30s) or re-run `frameworks cluster provision`, then retry promote", hostName, host.WireguardPublicKey, qmPubKey)
			}

			if host.WireguardPrivateKeyManaged != nil && !*host.WireguardPrivateKeyManaged {
				return fmt.Errorf("host %q still carries wireguard_private_key_managed: false in hosts.enc.yaml — run `frameworks mesh wg rotate %s` to re-key into SOPS first", hostName, hostName)
			}

			if _, err := client.SetNodeEnrollmentOrigin(cmd.Context(), &pb.SetNodeEnrollmentOriginRequest{
				NodeId:           qmNode.GetNodeId(),
				EnrollmentOrigin: enrollmentOriginGitopsSeed,
				ExpectedCurrent:  enrollmentOriginAdoptedLocal,
			}); err != nil {
				return fmt.Errorf("flip origin for %s: %w", hostName, err)
			}

			ux.Success(cmd.OutOrStdout(), fmt.Sprintf("mesh wg promote: %s is now gitops_seed", hostName))
			fmt.Fprintln(cmd.OutOrStdout(), "\nRun `frameworks mesh wg audit` to confirm the cluster view.")
			return nil
		},
	}
}

// findQMNode looks up the QM row for the named host, filtering to clusterID
// when provided to disambiguate multi-cluster manifests where the same node
// name can exist in two clusters.
func findQMNode(ctx context.Context, client interface {
	ListNodes(ctx context.Context, clusterID, nodeType, region string, pagination *pb.CursorPaginationRequest) (*pb.ListNodesResponse, error)
}, hostName, clusterID string) (*pb.InfrastructureNode, error) {
	resp, err := client.ListNodes(ctx, clusterID, "", "", nil)
	if err != nil {
		return nil, fmt.Errorf("list infrastructure_nodes: %w", err)
	}
	for _, n := range resp.GetNodes() {
		if n.GetNodeName() != hostName {
			continue
		}
		if clusterID != "" && n.GetClusterId() != clusterID {
			continue
		}
		return n, nil
	}
	return nil, fmt.Errorf("host %q not found in Quartermaster", hostName)
}

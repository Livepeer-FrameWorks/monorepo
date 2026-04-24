package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"frameworks/cli/internal/mesh"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	pb "frameworks/pkg/proto"

	"github.com/spf13/cobra"
)

// newMeshReconcileCmd adopts runtime-enrolled nodes into GitOps. Currently
// implements the Adopt-Without-Import model: only the node's public identity
// is written back to `cluster.yaml`; the private key stays on disk and the
// inventory records `wireguard_private_key_managed: false` so Ansible
// preserves it. Origin flips `runtime_enrolled` → `adopted_local`.
func newMeshReconcileCmd() *cobra.Command {
	var (
		hostsPath string
		dryRun    bool
		writeGit  bool
	)
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Adopt runtime-enrolled mesh nodes into GitOps",
		Long: `Queries Quartermaster for nodes with enrollment_origin=runtime_enrolled
inside the manifest's clusters and writes their public identity into
cluster.yaml and a private-key-preserved placeholder into hosts.enc.yaml.

Only --write-gitops actually mutates files; without it, reconcile prints a
plan. Private keys are never fetched from the node — Ansible is told to
preserve the on-disk key via wireguard_private_key_managed: false.

After a successful reconcile, each adopted node's enrollment_origin flips
to adopted_local. To fully take GitOps authority over its private key,
run 'frameworks mesh wg rotate <host>' afterwards.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := resolveMeshMutationTarget(cmd, stringFlag(cmd, "manifest").Value, hostsPath)
			if err != nil {
				return err
			}

			manifest, err := inventory.LoadWithHostsFileNoValidate(target.manifestPath, target.hostsPath, target.ageKey)
			if err != nil {
				return fmt.Errorf("load manifest: %w", err)
			}
			manifestClusters := map[string]bool{}
			for _, id := range manifest.AllClusterIDs() {
				manifestClusters[id] = true
			}

			client, err := getMeshQuartermasterGRPCClient()
			if err != nil {
				return fmt.Errorf("connect to Quartermaster: %w", err)
			}
			defer client.Close()

			resp, err := client.ListNodes(context.Background(), "", "", "", nil)
			if err != nil {
				return fmt.Errorf("list infrastructure_nodes: %w", err)
			}

			pending, inProgress := filterRuntimeEnrolled(resp.GetNodes(), manifestClusters, manifest)
			if len(pending) == 0 && len(inProgress) == 0 {
				ux.Success(cmd.OutOrStdout(), "mesh reconcile: no runtime-enrolled nodes to adopt")
				return nil
			}

			sort.Slice(pending, func(i, j int) bool {
				if pending[i].ClusterID != pending[j].ClusterID {
					return pending[i].ClusterID < pending[j].ClusterID
				}
				return pending[i].Name < pending[j].Name
			})
			sort.Slice(inProgress, func(i, j int) bool {
				if inProgress[i].ClusterID != inProgress[j].ClusterID {
					return inProgress[i].ClusterID < inProgress[j].ClusterID
				}
				return inProgress[i].Name < inProgress[j].Name
			})

			if len(pending) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "mesh reconcile: %d runtime-enrolled node(s) to adopt\n", len(pending))
				for _, h := range pending {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s [cluster=%s ip=%s port=%d]\n", h.Name, h.ClusterID, h.WireguardIP, h.WireguardPort)
				}
			}
			if len(inProgress) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "mesh reconcile: %d node(s) already in GitOps but still runtime_enrolled in Quartermaster — will finish the origin flip\n", len(inProgress))
				for _, h := range inProgress {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s [cluster=%s]\n", h.Name, h.ClusterID)
				}
			}

			if !writeGit {
				fmt.Fprintln(cmd.OutOrStdout(), "\npass --write-gitops to commit the changes above and flip origins to adopted_local.")
				return nil
			}
			if dryRun {
				fmt.Fprintln(cmd.OutOrStdout(), "\n--dry-run: no files written, no origins flipped.")
				return nil
			}

			if len(pending) > 0 {
				if err := writeAdoptedHostsToGitOps(cmd.Context(), target, pending); err != nil {
					return err
				}
			}

			// Flip origins for everything — both newly-written and
			// already-in-manifest in_progress hosts. The server RPC is
			// idempotent (no-op if already adopted_local) and uses
			// expected_current="runtime_enrolled" to refuse stale flips.
			flipped := 0
			for _, h := range append(pending, inProgress...) {
				if _, err := client.SetNodeEnrollmentOrigin(cmd.Context(), &pb.SetNodeEnrollmentOriginRequest{
					NodeId:           h.NodeID,
					EnrollmentOrigin: "adopted_local",
					ExpectedCurrent:  "runtime_enrolled",
				}); err != nil {
					return fmt.Errorf("flip origin for %s (%s): %w", h.Name, h.NodeID, err)
				}
				flipped++
			}
			ux.Success(cmd.OutOrStdout(), fmt.Sprintf("mesh reconcile: adopted %d node(s) into GitOps", flipped))
			if len(pending) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "\nNext: commit cluster.yaml + hosts.enc.yaml, then re-run `frameworks cluster provision`.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&hostsPath, "hosts-file", "", "path to SOPS-encrypted hosts inventory (default: manifest hosts_file or sibling hosts.enc.yaml)")
	cmd.Flags().BoolVar(&writeGit, "write-gitops", false, "actually mutate cluster.yaml + hosts.enc.yaml and flip origins")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "with --write-gitops, print the plan without touching disk or Quartermaster")
	return cmd
}

type reconcileHost struct {
	NodeID             string
	Name               string
	ClusterID          string
	WireguardIP        string
	WireguardPort      int32
	WireguardPublicKey string
	ExternalIP         string
	NodeType           string
}

// filterRuntimeEnrolled partitions QM's runtime-enrolled nodes (inside this
// manifest's clusters) into two groups:
//
//   - pending: not yet present in the manifest. Need both the GitOps file
//     write AND the origin flip.
//   - inProgress: already present in the manifest (prior reconcile wrote the
//     host but failed to flip origin). Need only the origin flip. This makes
//     reconcile idempotent across partial failures — a retried run finishes
//     the work without re-writing the manifest.
func filterRuntimeEnrolled(qmNodes []*pb.InfrastructureNode, manifestClusters map[string]bool, manifest *inventory.Manifest) (pending, inProgress []reconcileHost) {
	for _, n := range qmNodes {
		if n.GetEnrollmentOrigin() != "runtime_enrolled" {
			continue
		}
		cid := n.GetClusterId()
		if !manifestClusters[cid] {
			continue
		}
		h := reconcileHost{
			NodeID:    n.GetNodeId(),
			Name:      n.GetNodeName(),
			ClusterID: cid,
			NodeType:  n.GetNodeType(),
		}
		if n.WireguardIp != nil {
			h.WireguardIP = *n.WireguardIp
		}
		if n.WireguardPublicKey != nil {
			h.WireguardPublicKey = *n.WireguardPublicKey
		}
		if n.WireguardPort != nil {
			h.WireguardPort = *n.WireguardPort
		}
		if n.ExternalIp != nil {
			h.ExternalIP = *n.ExternalIp
		}
		if _, alreadyInManifest := manifest.Hosts[n.GetNodeName()]; alreadyInManifest {
			inProgress = append(inProgress, h)
		} else {
			pending = append(pending, h)
		}
	}
	return pending, inProgress
}

// writeAdoptedHostsToGitOps mutates cluster.yaml and hosts.enc.yaml using the
// same staged-then-committed flow as `mesh wg generate`, so a partial failure
// can't leave the two files out of sync.
func writeAdoptedHostsToGitOps(ctx context.Context, target *meshMutationTarget, hosts []reconcileHost) error {
	adopted := make([]mesh.AdoptedHost, 0, len(hosts))
	for _, h := range hosts {
		adopted = append(adopted, mesh.AdoptedHost{
			Name:               h.Name,
			ClusterID:          h.ClusterID,
			ExternalIP:         h.ExternalIP,
			WireguardIP:        h.WireguardIP,
			WireguardPublicKey: h.WireguardPublicKey,
			WireguardPort:      int(h.WireguardPort),
			NodeType:           h.NodeType,
		})
	}

	rawManifest, err := os.ReadFile(target.manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	updatedManifest, err := mesh.InsertAdoptedHostsIntoClusterYAML(rawManifest, adopted)
	if err != nil {
		return err
	}

	// Stage SOPS hosts file with preserve-key placeholders.
	if _, statErr := os.Stat(target.hostsPath); statErr != nil {
		return fmt.Errorf("hosts-file %s: %w", target.hostsPath, statErr)
	}
	stagedHosts, err := mesh.StageEncryptedYAML(ctx, target.hostsPath, target.ageKey, func(plaintext []byte) ([]byte, error) {
		return mesh.InsertAdoptedHostsIntoInventoryYAML(plaintext, adopted)
	})
	if err != nil {
		return fmt.Errorf("stage %s: %w", target.hostsPath, err)
	}
	defer stagedHosts.Discard()

	manifestTmp, err := os.CreateTemp(filepath.Dir(target.manifestPath), ".cluster-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("stage manifest: %w", err)
	}
	manifestTmpPath := manifestTmp.Name()
	defer os.Remove(manifestTmpPath) //nolint:errcheck
	if _, writeErr := manifestTmp.Write(updatedManifest); writeErr != nil {
		manifestTmp.Close()
		return fmt.Errorf("write staged manifest: %w", writeErr)
	}
	if closeErr := manifestTmp.Close(); closeErr != nil {
		return fmt.Errorf("close staged manifest: %w", closeErr)
	}

	manifestBackup, err := os.ReadFile(target.manifestPath)
	if err != nil {
		return fmt.Errorf("read original manifest for rollback: %w", err)
	}
	return mesh.CommitManifestAndHosts(target.manifestPath, manifestTmpPath, manifestBackup, stagedHosts)
}

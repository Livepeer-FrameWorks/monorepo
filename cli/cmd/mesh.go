package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"text/tabwriter"
	"time"

	"frameworks/cli/internal/config"
	"frameworks/cli/internal/ux"
	"frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"

	"github.com/spf13/cobra"
)

// newMeshCmd returns the Mesh command group
func newMeshCmd() *cobra.Command {
	mesh := &cobra.Command{
		Use:   "mesh",
		Short: "Mesh network status and verification",
		Long: `Inspect the state of the internal WireGuard mesh (managed by Privateer).

		This command queries the Quartermaster inventory to show the expected mesh topology.
		Use 'frameworks mesh status' to see which nodes are peered.`,
	}

	mesh.AddCommand(newMeshStatusCmd())
	mesh.AddCommand(newMeshWgCmd())
	mesh.AddCommand(newMeshJoinCmd())
	mesh.AddCommand(newMeshReconcileCmd())
	mesh.AddCommand(newMeshDoctorCmd())

	mesh.PersistentFlags().String("manifest", "", "path to a single cluster.yaml (overrides gitops sources)")
	mesh.PersistentFlags().String("gitops-dir", "", "path to a local gitops repo (uses <dir>/clusters/<cluster>/cluster.yaml)")
	mesh.PersistentFlags().String("github-repo", "", "GitHub repo (owner/repo) to fetch the manifest from")
	mesh.PersistentFlags().String("github-ref", "", "branch/tag for --github-repo (default 'main')")
	mesh.PersistentFlags().String("cluster", "", "cluster name within the gitops repo (e.g. 'production')")
	mesh.PersistentFlags().String("age-key", "", "path to an age private key for SOPS-encrypted files (default: $SOPS_AGE_KEY_FILE)")
	mesh.PersistentFlags().Int64("github-app-id", 0, "GitHub App ID (for --github-repo)")
	mesh.PersistentFlags().Int64("github-installation-id", 0, "GitHub Installation ID (for --github-repo)")
	mesh.PersistentFlags().String("github-private-key", "", "path to GitHub App private key PEM (for --github-repo)")

	return mesh
}

// getMeshQuartermasterGRPCClient creates a gRPC client to Quartermaster for mesh operations
func getMeshQuartermasterGRPCClient() (*quartermaster.GRPCClient, error) {
	ctxConfig, err := activeContextWithAuth()
	if err != nil {
		return nil, err
	}

	grpcAddr, err := config.RequireEndpoint(ctxConfig, "quartermaster_grpc_addr", ctxConfig.Endpoints.QuartermasterGRPCAddr, false)
	if err != nil {
		return nil, err
	}

	return quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      grpcAddr,
		Logger:        logging.NewLogger(),
		ServiceToken:  ctxConfig.Auth.ServiceToken,
		AllowInsecure: true,
	})
}

// newMeshStatusCmd shows the mesh status
func newMeshStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show internal mesh status",
		Long: `Inspect the live mesh state recorded in Quartermaster.

When a manifest source is provided (--manifest, --gitops-dir, --github-repo,
or one of the FRAMEWORKS_* env vars), each row is cross-referenced with the
GitOps cluster manifest: ORIGIN and KEY-MATCH columns are added so operators
can spot divergence at a glance. The command never errors on mismatch — for
exit-coded divergence checking, use 'frameworks mesh wg audit'.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := getMeshQuartermasterGRPCClient()
			if err != nil {
				return err
			}
			defer client.Close()

			isJSON := output == "json"
			if !isJSON {
				ux.Heading(cmd.OutOrStdout(), "Privateer Mesh Status")
				fmt.Fprint(cmd.OutOrStdout(), "Fetching topology from Quartermaster... ")
			}

			// Fetch Nodes via gRPC
			resp, err := client.ListNodes(context.Background(), "", "", "", nil)
			if err != nil {
				if !isJSON {
					fmt.Fprintln(cmd.OutOrStdout(), "❌")
				}
				return fmt.Errorf("failed to get nodes: %w", err)
			}
			if !isJSON {
				ux.Success(cmd.OutOrStdout(), fmt.Sprintf("(%d nodes)", len(resp.Nodes)))
				fmt.Fprintln(cmd.OutOrStdout())
			}

			// Sort by NodeId for stable output
			nodes := resp.Nodes
			sort.Slice(nodes, func(i, j int) bool {
				return nodes[i].Id < nodes[j].Id
			})

			// Cross-reference with the GitOps manifest if any manifest
			// source is set. The lookup table is keyed by (node_name,
			// cluster_id) — node names are not globally unique across
			// clusters, so a single name can exist legitimately in two
			// clusters with different identities.
			auditByKey := buildStatusAuditIndex(cmd)

			type meshNode struct {
				ID          string `json:"id"`
				NodeName    string `json:"node_name"`
				ClusterID   string `json:"cluster_id"`
				Role        string `json:"role"`
				InternalIP  string `json:"internal_ip"`
				WireguardIP string `json:"wireguard_ip"`
				LastSeen    string `json:"last_seen"`
				AgentStatus string `json:"agent_status"`
				Origin      string `json:"origin,omitempty"`
				KeyMatch    string `json:"key_match,omitempty"`
				Revision    string `json:"applied_mesh_revision,omitempty"`
			}

			var meshNodes []meshNode
			for _, node := range nodes {
				wgIP := "-"
				if node.WireguardIp != nil {
					wgIP = *node.WireguardIp
				}

				internalIP := "-"
				if node.InternalIp != nil {
					internalIP = *node.InternalIp
				}

				lastSeen := "-"
				agentStatus := "Offline"

				if node.LastHeartbeat != nil {
					duration := time.Since(node.LastHeartbeat.AsTime()).Round(time.Second)
					lastSeen = fmt.Sprintf("%s ago", duration)
					if duration < 90*time.Second {
						agentStatus = "Healthy"
					} else {
						agentStatus = "Stale/Offline"
					}
				}

				origin := ""
				keyMatch := ""
				revision := ""
				if auditByKey != nil {
					if r, ok := auditByKey[statusKey{nodeName: node.NodeName, clusterID: node.ClusterId}]; ok {
						origin = r.origin
						keyMatch = statusKeyMatch(r.severity)
						revision = r.revision
					} else {
						keyMatch = "no-manifest-row"
					}
				}

				meshNodes = append(meshNodes, meshNode{
					ID: node.Id, NodeName: node.NodeName, ClusterID: node.ClusterId, Role: node.NodeType,
					InternalIP: internalIP, WireguardIP: wgIP,
					LastSeen: lastSeen, AgentStatus: agentStatus,
					Origin: origin, KeyMatch: keyMatch, Revision: revision,
				})
			}

			if isJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(meshNodes)
			}

			// Display Table
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			if auditByKey != nil {
				fmt.Fprintln(w, "CLUSTER\tNODE ID\tROLE\tINTERNAL IP\tWG IP\tLAST SEEN\tAGENT STATUS\tORIGIN\tKEY-MATCH\tREVISION")
				for _, n := range meshNodes {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						dashIfEmpty(n.ClusterID), n.ID, n.Role, n.InternalIP, n.WireguardIP, n.LastSeen, n.AgentStatus, dashIfEmpty(n.Origin), dashIfEmpty(n.KeyMatch), revisionLabel(n.Revision))
				}
			} else {
				fmt.Fprintln(w, "NODE ID\tROLE\tINTERNAL IP\tWG IP\tLAST SEEN\tAGENT STATUS")
				for _, n := range meshNodes {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
						n.ID, n.Role, n.InternalIP, n.WireguardIP, n.LastSeen, n.AgentStatus)
				}
			}
			w.Flush()

			fmt.Fprintln(cmd.OutOrStdout(), "\nNote: To join a new node, add it to GitOps, run 'frameworks mesh wg generate', then provision.")
			return nil
		},
	}
}

// statusKey is the composite (node_name, cluster_id) lookup used by
// 'mesh status' to join QM rows with audit findings. Names are not
// globally unique across clusters, so node_name alone would mis-join in
// multi-cluster manifests.
type statusKey struct {
	nodeName  string
	clusterID string
}

// buildStatusAuditIndex returns a (node_name, cluster_id) → audit row
// map when the user supplied a manifest source, or nil otherwise. Errors
// during manifest resolution are surfaced as a notice and the command
// falls back to the QM-only view — status should never fail because the
// manifest is offline.
func buildStatusAuditIndex(cmd *cobra.Command) map[statusKey]auditRow {
	if !statusManifestSourceProvided(cmd) {
		return nil
	}
	rc, err := resolveClusterManifest(cmd)
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Note: manifest cross-reference unavailable: %v\n", err)
		return nil
	}
	defer rc.Cleanup()

	hostNames := meshCheckHostNames(rc.Manifest)
	manifestClusters := map[string]bool{}
	for _, id := range rc.Manifest.AllClusterIDs() {
		manifestClusters[id] = true
	}

	client, err := getMeshQuartermasterGRPCClient()
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Note: manifest cross-reference unavailable: %v\n", err)
		return nil
	}
	defer client.Close()
	resp, err := client.ListNodes(context.Background(), "", "", "", nil)
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Note: manifest cross-reference unavailable: %v\n", err)
		return nil
	}

	findings := auditMeshIdentity(rc.Manifest, hostNames, resp.GetNodes(), manifestClusters, time.Now(), defaultLivenessWindow)
	idx := make(map[statusKey]auditRow, len(findings.rows))
	for _, r := range findings.rows {
		idx[statusKey{nodeName: r.host, clusterID: r.clusterID}] = r
	}
	return idx
}

func statusManifestSourceProvided(cmd *cobra.Command) bool {
	for _, name := range []string{"manifest", "gitops-dir", "github-repo"} {
		if f := cmd.Flag(name); f != nil && f.Changed {
			return true
		}
	}
	return manifestSourceInEnv()
}

func statusKeyMatch(s auditSeverity) string {
	switch s {
	case auditOK:
		return "match"
	case auditInfo:
		return "info"
	case auditWarn:
		return "warn"
	case auditError:
		return "MISMATCH"
	default:
		return "?"
	}
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

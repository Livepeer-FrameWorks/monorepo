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

			type meshNode struct {
				ID          string `json:"id"`
				Role        string `json:"role"`
				InternalIP  string `json:"internal_ip"`
				WireguardIP string `json:"wireguard_ip"`
				LastSeen    string `json:"last_seen"`
				AgentStatus string `json:"agent_status"`
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

				meshNodes = append(meshNodes, meshNode{
					ID: node.Id, Role: node.NodeType,
					InternalIP: internalIP, WireguardIP: wgIP,
					LastSeen: lastSeen, AgentStatus: agentStatus,
				})
			}

			if isJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(meshNodes)
			}

			// Display Table
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NODE ID\tROLE\tINTERNAL IP\tWG IP\tLAST SEEN\tAGENT STATUS")
			for _, n := range meshNodes {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					n.ID, n.Role, n.InternalIP, n.WireguardIP, n.LastSeen, n.AgentStatus)
			}
			w.Flush()

			fmt.Fprintln(cmd.OutOrStdout(), "\nNote: To join a new node, add it to GitOps, run 'frameworks mesh wg generate', then provision.")
			return nil
		},
	}
}

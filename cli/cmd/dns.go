package cmd

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"text/tabwriter"

	"frameworks/cli/internal/config"
	"frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"

	"github.com/spf13/cobra"
)

// newDNSCmd returns the DNS command group
func newDNSCmd() *cobra.Command {
	dns := &cobra.Command{
		Use:   "dns",
		Short: "DNS infrastructure verification",
		Long: `Verify public DNS records against the Quartermaster inventory.

Note: Management of DNS records is now handled automatically by the Navigator service.
This command allows you to verify that the public state matches the internal inventory.`,
	}

	dns.AddCommand(newDNSDoctorCmd())

	return dns
}

// newDNSDoctorCmd verifies DNS state
func newDNSDoctorCmd() *cobra.Command {
	var domain string

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Verify public DNS records match inventory",
		RunE: func(cmd *cobra.Command, args []string) error {
			// 1. Get Quartermaster gRPC Client
			qmClient, err := getQuartermasterGRPCClient()
			if err != nil {
				return err
			}
			defer qmClient.Close()

			fmt.Fprintln(cmd.OutOrStdout(), "🏥 DNS Health Check")
			fmt.Fprintln(cmd.OutOrStdout(), "===================")

			// 2. Fetch Inventory
			fmt.Fprint(cmd.OutOrStdout(), "• Fetching inventory from Quartermaster... ")
			nodesResp, err := qmClient.ListNodes(context.Background(), "", "", "", nil)
			if err != nil {
				fmt.Fprintln(cmd.OutOrStdout(), "❌")
				return fmt.Errorf("failed to get nodes: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ (%d active nodes)\n", len(nodesResp.Nodes))

			// 3. Group Expected IPs by Role
			expectedIPs := make(map[string][]string)

			// Define mapping of role -> subdomain
			// This mirrors the logic in api_dns (Navigator)
			serviceMap := map[string]string{
				"edge":    "edge",
				"ingest":  "ingest",
				"play":    "play",
				"bridge":  "api",
				"gateway": "api",
				"api":     "api",
				"app":     "app",
				"website": "@",
				"docs":    "docs",
				"forms":   "forms",
			}

			for _, node := range nodesResp.Nodes {
				if node.ExternalIp == nil || *node.ExternalIp == "" {
					continue
				}

				// Check node type/role against map
				// Assuming node.NodeType maps roughly to our service roles
				if subdomain, ok := serviceMap[node.NodeType]; ok {
					fqdn := fmt.Sprintf("%s.%s", subdomain, domain)
					if subdomain == "" || subdomain == "@" {
						fqdn = domain
					}
					expectedIPs[fqdn] = append(expectedIPs[fqdn], *node.ExternalIp)
				}
			}

			// 4. Verify Records
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "\nDOMAIN\tEXPECTED IPs\tACTUAL IPs\tSTATUS")

			allHealthy := true

			// Check each expected domain
			for fqdn, wantIPs := range expectedIPs {
				sort.Strings(wantIPs)

				// Resolve Public DNS
				ips, err := net.LookupHost(fqdn)
				var gotIPs []string
				if err == nil {
					gotIPs = ips
				}
				sort.Strings(gotIPs)

				// Compare
				status := "✅ OK"
				if err != nil {
					status = "❌ NXDOMAIN"
					allHealthy = false
				} else if !slicesEqual(wantIPs, gotIPs) {
					status = "⚠️  MISMATCH"
					allHealthy = false
				}

				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					fqdn,
					strings.Join(wantIPs, ","),
					strings.Join(gotIPs, ","),
					status,
				)
			}
			w.Flush()

			if !allHealthy {
				return fmt.Errorf("DNS mismatch detected")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&domain, "domain", "frameworks.network", "Root domain to verify")

	return cmd
}

func getQuartermasterGRPCClient() (*quartermaster.GRPCClient, error) {
	cfg, _, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	ctxConfig := config.GetCurrent(cfg)
	ctxConfig.Auth = config.ResolveAuth(ctxConfig)

	grpcAddr, err := config.RequireEndpoint(ctxConfig, "quartermaster_grpc_addr", ctxConfig.Endpoints.QuartermasterGRPCAddr, false)
	if err != nil {
		return nil, err
	}

	return quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:     grpcAddr,
		Logger:       logging.NewLogger(),
		ServiceToken: ctxConfig.Auth.ServiceToken,
	})
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

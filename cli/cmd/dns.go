package cmd

import (
	"context"
	"encoding/json"
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

			isJSON := output == "json"
			if !isJSON {
				fmt.Fprintln(cmd.OutOrStdout(), "🏥 DNS Health Check")
				fmt.Fprintln(cmd.OutOrStdout(), "===================")
				fmt.Fprint(cmd.OutOrStdout(), "• Fetching inventory from Quartermaster... ")
			}

			// 2. Fetch Inventory
			nodesResp, err := qmClient.ListNodes(context.Background(), "", "", "", nil)
			if err != nil {
				if !isJSON {
					fmt.Fprintln(cmd.OutOrStdout(), "❌")
				}
				return fmt.Errorf("failed to get nodes: %w", err)
			}
			if !isJSON {
				fmt.Fprintf(cmd.OutOrStdout(), "✓ (%d active nodes)\n", len(nodesResp.Nodes))
			}

			// 3. Group Expected IPs by Role
			expectedIPs := make(map[string][]string)

			// Define mapping of role -> subdomain
			// This mirrors the logic in api_dns (Navigator)
			serviceMap := map[string]string{
				"edge-egress": "edge-egress",
				"edge-ingest": "edge-ingest",
				"foghorn":     "foghorn",
				"bridge":      "bridge",
				"chartroom":   "chartroom",
				"foredeck":    "@",
				"logbook":     "logbook",
				"steward":     "steward",
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
			type dnsResult struct {
				Domain      string   `json:"domain"`
				ExpectedIPs []string `json:"expected_ips"`
				ActualIPs   []string `json:"actual_ips"`
				OK          bool     `json:"ok"`
				Status      string   `json:"status"`
			}

			var results []dnsResult
			allHealthy := true

			for fqdn, wantIPs := range expectedIPs {
				sort.Strings(wantIPs)
				ips, err := net.LookupHost(fqdn)
				var gotIPs []string
				if err == nil {
					gotIPs = ips
				}
				sort.Strings(gotIPs)

				r := dnsResult{Domain: fqdn, ExpectedIPs: wantIPs, ActualIPs: gotIPs, OK: true, Status: "OK"}
				if err != nil {
					r.OK = false
					r.Status = "NXDOMAIN"
					allHealthy = false
				} else if !slicesEqual(wantIPs, gotIPs) {
					r.OK = false
					r.Status = "MISMATCH"
					allHealthy = false
				}
				results = append(results, r)
			}

			if isJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(results)
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "\nDOMAIN\tEXPECTED IPs\tACTUAL IPs\tSTATUS")
			for _, r := range results {
				var statusIcon string
				switch r.Status {
				case "NXDOMAIN":
					statusIcon = "❌ NXDOMAIN"
				case "MISMATCH":
					statusIcon = "⚠️  MISMATCH"
				default:
					statusIcon = "✅ OK"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					r.Domain,
					strings.Join(r.ExpectedIPs, ","),
					strings.Join(r.ActualIPs, ","),
					statusIcon,
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

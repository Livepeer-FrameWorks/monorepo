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
	"frameworks/cli/internal/ux"
	"frameworks/pkg/clients/quartermaster"
	pkgdns "frameworks/pkg/dns"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

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
				ux.Heading(cmd.OutOrStdout(), "DNS Health Check")
				fmt.Fprint(cmd.OutOrStdout(), "Fetching service inventory from Quartermaster... ")
			}

			// 2. Fetch expected service-backed IPs using the same Quartermaster
			// query path Navigator relies on.
			expectedIPs := make(map[string][]string)
			serviceTypes := pkgdns.ManagedServiceTypes()
			staleThresholdSeconds := 300
			clustersResp, err := qmClient.ListClusters(context.Background(), nil)
			if err != nil {
				if !isJSON {
					fmt.Fprintln(cmd.OutOrStdout(), "❌")
				}
				return fmt.Errorf("failed to list clusters: %w", err)
			}
			clusterSlugs := make(map[string]string, len(clustersResp.Clusters))
			for _, cluster := range clustersResp.Clusters {
				if !cluster.GetIsActive() {
					continue
				}
				clusterSlugs[cluster.GetClusterId()] = pkgdns.ClusterSlug(cluster.GetClusterId(), cluster.GetClusterName())
			}

			for _, serviceType := range serviceTypes {
				nodesResp, err := qmClient.ListHealthyNodesForDNS(context.Background(), staleThresholdSeconds, serviceType)
				if err != nil {
					if !isJSON {
						fmt.Fprintln(cmd.OutOrStdout(), "❌")
					}
					return fmt.Errorf("failed to get healthy nodes for %s: %w", serviceType, err)
				}
				wantIPs := uniqueExternalIPs(nodesResp.Nodes)
				if len(wantIPs) == 0 {
					continue
				}

				switch pkgdns.ProviderForServiceType(serviceType) {
				case pkgdns.ProviderCloudflare:
					fqdn, ok := pkgdns.RootServiceFQDN(serviceType, domain)
					if !ok {
						continue
					}
					expectedIPs[fqdn] = wantIPs
					continue
				case pkgdns.ProviderBunny:
					if !pkgdns.IsClusterScopedServiceType(serviceType) {
						continue
					}
				default:
					continue
				}

				for clusterID, clusterIPs := range clusterExternalIPs(nodesResp.Nodes) {
					clusterSlug := clusterSlugs[clusterID]
					if clusterSlug == "" {
						continue
					}
					clusterFQDN, ok := pkgdns.ServiceFQDN(serviceType, clusterSlug+"."+domain)
					if !ok || len(clusterIPs) == 0 {
						continue
					}
					expectedIPs[clusterFQDN] = clusterIPs
				}
			}

			if !isJSON {
				ux.Success(cmd.OutOrStdout(), fmt.Sprintf("(%d service types checked)", len(serviceTypes)))
			}

			// 3. Verify Records
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
				// Inline status icons for the DNS-row table. CI/non-TTY
				// falls through to the ASCII fallbacks; keep the icon
				// choice consistent with the rest of the CLI's palette.
				var statusIcon string
				mode := ux.DetectMode(cmd.OutOrStdout())
				switch r.Status {
				case "NXDOMAIN":
					if mode.Unicode {
						statusIcon = "✗ NXDOMAIN"
					} else {
						statusIcon = "[FAIL] NXDOMAIN"
					}
				case "MISMATCH":
					if mode.Unicode {
						statusIcon = "⚠ MISMATCH"
					} else {
						statusIcon = "[WARN] MISMATCH"
					}
				default:
					if mode.Unicode {
						statusIcon = "✓ OK"
					} else {
						statusIcon = "[OK] OK"
					}
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

func uniqueExternalIPs(nodes []*pb.InfrastructureNode) []string {
	seen := make(map[string]struct{}, len(nodes))
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ip := node.GetExternalIp()
		if ip == "" {
			continue
		}
		if _, exists := seen[ip]; exists {
			continue
		}
		seen[ip] = struct{}{}
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}

func clusterExternalIPs(nodes []*pb.InfrastructureNode) map[string][]string {
	clusterSets := make(map[string]map[string]struct{})
	for _, node := range nodes {
		clusterID := node.GetClusterId()
		ip := node.GetExternalIp()
		if clusterID == "" || ip == "" {
			continue
		}
		if _, ok := clusterSets[clusterID]; !ok {
			clusterSets[clusterID] = make(map[string]struct{})
		}
		clusterSets[clusterID][ip] = struct{}{}
	}

	out := make(map[string][]string, len(clusterSets))
	for clusterID, ips := range clusterSets {
		clusterIPs := make([]string, 0, len(ips))
		for ip := range ips {
			clusterIPs = append(clusterIPs, ip)
		}
		sort.Strings(clusterIPs)
		out[clusterID] = clusterIPs
	}
	return out
}

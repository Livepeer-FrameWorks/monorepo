package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"text/tabwriter"

	"frameworks/cli/internal/controlplane"
	"frameworks/cli/internal/ux"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

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
			qmClient, cleanup, err := getQuartermasterGRPCClient(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			defer qmClient.Close()

			return runDNSDoctor(cmd.Context(), cmd.OutOrStdout(), qmClient, domain, net.LookupHost, output == "json")
		},
	}

	cmd.Flags().StringVar(&domain, "domain", "frameworks.network", "Root domain to verify")

	return cmd
}

// dnsQMClient is the narrow Quartermaster surface the dns doctor handler uses:
// the same query path Navigator relies on to compute expected DNS records.
type dnsQMClient interface {
	ListClusters(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error)
	ListHealthyNodesForDNS(ctx context.Context, staleThresholdSeconds int, serviceType string) (*quartermasterpb.ListHealthyNodesForDNSResponse, error)
}

// runDNSDoctor fetches expected service-backed IPs from Quartermaster, resolves
// the corresponding public records via lookupHost, and renders the comparison.
// It returns an error when any record is missing (NXDOMAIN) or mismatched.
func runDNSDoctor(ctx context.Context, w io.Writer, cli dnsQMClient, domain string, lookupHost func(string) ([]string, error), outputJSON bool) error {
	if !outputJSON {
		ux.Heading(w, "DNS Health Check")
		fmt.Fprint(w, "Fetching service inventory from Quartermaster... ")
	}

	// Fetch expected service-backed IPs using the same Quartermaster query
	// path Navigator relies on.
	type expectedDNSRecord struct {
		IPs      []string
		Provider pkgdns.Provider
	}
	expectedRecords := make(map[string]expectedDNSRecord)
	serviceTypes := pkgdns.ManagedServiceTypes()
	staleThresholdSeconds := 300
	clustersResp, err := cli.ListClusters(ctx, nil)
	if err != nil {
		if !outputJSON {
			fmt.Fprintln(w, "❌")
		}
		return fmt.Errorf("failed to list clusters: %w", err)
	}
	clusterSlugs := make(map[string]string, len(clustersResp.Clusters))
	officialClusters := make(map[string]struct{}, len(clustersResp.Clusters))
	for _, cluster := range clustersResp.Clusters {
		if !cluster.GetIsActive() {
			continue
		}
		clusterSlugs[cluster.GetClusterId()] = pkgdns.ClusterSlug(cluster.GetClusterId(), cluster.GetClusterName())
		if cluster.GetIsPlatformOfficial() {
			officialClusters[cluster.GetClusterId()] = struct{}{}
		}
	}

	for _, serviceType := range serviceTypes {
		nodesResp, err := cli.ListHealthyNodesForDNS(ctx, staleThresholdSeconds, serviceType)
		if err != nil {
			if !outputJSON {
				fmt.Fprintln(w, "❌")
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
			expectedRecords[fqdn] = expectedDNSRecord{IPs: wantIPs, Provider: pkgdns.ProviderCloudflare}
			continue
		case pkgdns.ProviderBunny:
			if !pkgdns.IsClusterScopedServiceType(serviceType) {
				continue
			}
		default:
			continue
		}

		var officialNodes []*quartermasterpb.InfrastructureNode
		for _, node := range nodesResp.Nodes {
			if _, ok := officialClusters[node.GetClusterId()]; ok {
				officialNodes = append(officialNodes, node)
			}
		}
		if rootIPs := uniqueExternalIPs(officialNodes); len(rootIPs) > 0 {
			if rootFQDN, ok := pkgdns.RootServiceFQDN(serviceType, domain); ok {
				expectedRecords[rootFQDN] = expectedDNSRecord{IPs: rootIPs, Provider: pkgdns.ProviderBunny}
			}
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
			expectedRecords[clusterFQDN] = expectedDNSRecord{IPs: clusterIPs, Provider: pkgdns.ProviderBunny}
		}
	}

	if !outputJSON {
		ux.Success(w, fmt.Sprintf("(%d service types checked)", len(serviceTypes)))
	}

	type dnsResult struct {
		Domain      string   `json:"domain"`
		Provider    string   `json:"provider"`
		Validation  string   `json:"validation"`
		ExpectedIPs []string `json:"expected_ips"`
		ActualIPs   []string `json:"actual_ips"`
		OK          bool     `json:"ok"`
		Status      string   `json:"status"`
	}

	var results []dnsResult
	allHealthy := true

	domains := make([]string, 0, len(expectedRecords))
	for fqdn := range expectedRecords {
		domains = append(domains, fqdn)
	}
	sort.Strings(domains)
	for _, fqdn := range domains {
		expected := expectedRecords[fqdn]
		wantIPs := expected.IPs
		sort.Strings(wantIPs)
		ips, err := lookupHost(fqdn)
		var gotIPs []string
		if err == nil {
			gotIPs = ips
		}
		sort.Strings(gotIPs)

		validation := "resolves"
		if expected.Provider == pkgdns.ProviderBunny {
			validation = "geo_subset"
		}
		r := dnsResult{Domain: fqdn, Provider: string(expected.Provider), Validation: validation, ExpectedIPs: wantIPs, ActualIPs: gotIPs, OK: true, Status: "OK"}
		if err != nil {
			r.OK = false
			r.Status = "NXDOMAIN"
			allHealthy = false
		} else if len(gotIPs) == 0 {
			r.OK = false
			r.Status = "EMPTY"
			allHealthy = false
		} else if expected.Provider == pkgdns.ProviderBunny && !isIPSubset(gotIPs, wantIPs) {
			r.OK = false
			r.Status = "MISMATCH"
			allHealthy = false
		}
		results = append(results, r)
	}

	if outputJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return err
		}
		if !allHealthy {
			return fmt.Errorf("DNS mismatch detected")
		}
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nDOMAIN\tEXPECTED IPs\tACTUAL IPs\tSTATUS")
	for _, r := range results {
		// Inline status icons for the DNS-row table. CI/non-TTY falls
		// through to the ASCII fallbacks; keep the icon choice
		// consistent with the rest of the CLI's palette.
		var statusIcon string
		mode := ux.DetectMode(w)
		switch r.Status {
		case "NXDOMAIN", "EMPTY":
			if mode.Unicode {
				statusIcon = "✗ " + r.Status
			} else {
				statusIcon = "[FAIL] " + r.Status
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
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			r.Domain,
			strings.Join(r.ExpectedIPs, ","),
			strings.Join(r.ActualIPs, ","),
			statusIcon,
		)
	}
	tw.Flush()

	if !allHealthy {
		return fmt.Errorf("DNS mismatch detected")
	}
	return nil
}

func isIPSubset(actual, desired []string) bool {
	if len(actual) == 0 {
		return false
	}
	wanted := make(map[string]struct{}, len(desired))
	for _, ip := range desired {
		wanted[ip] = struct{}{}
	}
	for _, ip := range actual {
		if _, ok := wanted[ip]; !ok {
			return false
		}
	}
	return true
}

func getQuartermasterGRPCClient(ctx context.Context) (*quartermaster.GRPCClient, func(), error) {
	ctxConfig, err := activeContextWithAuth(ctx)
	if err != nil {
		return nil, nil, err
	}

	ep, err := controlplane.ResolveGRPC(ctx, ctxConfig, "quartermaster")
	if err != nil {
		return nil, nil, err
	}

	client, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      ep.Address,
		Logger:        logging.NewLogger(),
		ServiceToken:  ctxConfig.Auth.ServiceToken,
		AllowInsecure: ep.AllowInsecure,
		CACertFile:    ep.CACertFile,
		CACertPEM:     ep.CACertPEM,
		ServerName:    ep.ServerName,
	})
	if err != nil {
		ep.Cleanup()
		return nil, nil, err
	}
	return client, ep.Cleanup, nil
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

func uniqueExternalIPs(nodes []*quartermasterpb.InfrastructureNode) []string {
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

func clusterExternalIPs(nodes []*quartermasterpb.InfrastructureNode) map[string][]string {
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

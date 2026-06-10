package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// fakeDNSQM is a hand-written stand-in for the Quartermaster surface used by
// `dns doctor`. It records the per-service request params and returns canned
// responses keyed by service type.
type fakeDNSQM struct {
	clustersResp *quartermasterpb.ListClustersResponse
	clustersErr  error

	nodesByType map[string]*quartermasterpb.ListHealthyNodesForDNSResponse
	nodesErr    error

	gotStale       int
	gotServiceReqs []string
}

func (f *fakeDNSQM) ListClusters(_ context.Context, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
	return f.clustersResp, f.clustersErr
}

func (f *fakeDNSQM) ListHealthyNodesForDNS(_ context.Context, staleThresholdSeconds int, serviceType string) (*quartermasterpb.ListHealthyNodesForDNSResponse, error) {
	f.gotStale = staleThresholdSeconds
	f.gotServiceReqs = append(f.gotServiceReqs, serviceType)
	if f.nodesErr != nil {
		return nil, f.nodesErr
	}
	if resp, ok := f.nodesByType[serviceType]; ok {
		return resp, nil
	}
	return &quartermasterpb.ListHealthyNodesForDNSResponse{}, nil
}

// dnsCloudflareServiceType returns a managed service type that resolves to a
// root (Cloudflare) FQDN, so the test exercises the expectedIPs path without
// hard-coding a brittle service-name string.
func dnsCloudflareServiceType(t *testing.T) (svcType, fqdn string) {
	t.Helper()
	for _, st := range pkgdns.ManagedServiceTypes() {
		if pkgdns.ProviderForServiceType(st) != pkgdns.ProviderCloudflare {
			continue
		}
		f, ok := pkgdns.RootServiceFQDN(st, "frameworks.network")
		if ok {
			return st, f
		}
	}
	t.Skip("no Cloudflare-provider managed service type available in this build")
	return "", ""
}

func dnsNodes(ips ...string) *quartermasterpb.ListHealthyNodesForDNSResponse {
	nodes := make([]*quartermasterpb.InfrastructureNode, 0, len(ips))
	for _, ip := range ips {
		nodes = append(nodes, &quartermasterpb.InfrastructureNode{ExternalIp: strptr(ip)})
	}
	return &quartermasterpb.ListHealthyNodesForDNSResponse{Nodes: nodes}
}

func TestRunDNSDoctor(t *testing.T) {
	t.Run("happy_ok_render", func(t *testing.T) {
		svcType, fqdn := dnsCloudflareServiceType(t)
		f := &fakeDNSQM{
			clustersResp: &quartermasterpb.ListClustersResponse{},
			nodesByType: map[string]*quartermasterpb.ListHealthyNodesForDNSResponse{
				svcType: dnsNodes("203.0.113.10", "203.0.113.11"),
			},
		}
		// lookupHost returns exactly the expected IPs -> OK, no error.
		lookup := func(host string) ([]string, error) {
			if host == fqdn {
				return []string{"203.0.113.11", "203.0.113.10"}, nil
			}
			return nil, errors.New("unexpected host: " + host)
		}

		var buf bytes.Buffer
		err := runDNSDoctor(context.Background(), &buf, f, "frameworks.network", lookup, false)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, fqdn) {
			t.Errorf("expected output to contain fqdn %q, got:\n%s", fqdn, out)
		}
		if !strings.Contains(out, "OK") {
			t.Errorf("expected OK status in output, got:\n%s", out)
		}
		if strings.Contains(out, "MISMATCH") || strings.Contains(out, "NXDOMAIN") {
			t.Errorf("unexpected failure status in output:\n%s", out)
		}
		// Request forwarding: stale threshold and that every managed type was queried.
		if f.gotStale != 300 {
			t.Errorf("expected stale threshold 300, got %d", f.gotStale)
		}
		if len(f.gotServiceReqs) != len(pkgdns.ManagedServiceTypes()) {
			t.Errorf("expected one ListHealthyNodesForDNS per managed type (%d), got %d",
				len(pkgdns.ManagedServiceTypes()), len(f.gotServiceReqs))
		}
	})

	t.Run("json_render", func(t *testing.T) {
		svcType, fqdn := dnsCloudflareServiceType(t)
		f := &fakeDNSQM{
			clustersResp: &quartermasterpb.ListClustersResponse{},
			nodesByType: map[string]*quartermasterpb.ListHealthyNodesForDNSResponse{
				svcType: dnsNodes("203.0.113.20"),
			},
		}
		lookup := func(string) ([]string, error) { return []string{"203.0.113.20"}, nil }

		var buf bytes.Buffer
		if err := runDNSDoctor(context.Background(), &buf, f, "frameworks.network", lookup, true); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		var results []struct {
			Domain      string   `json:"domain"`
			ExpectedIPs []string `json:"expected_ips"`
			ActualIPs   []string `json:"actual_ips"`
			OK          bool     `json:"ok"`
			Status      string   `json:"status"`
		}
		if err := json.Unmarshal(buf.Bytes(), &results); err != nil {
			t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].Domain != fqdn || !results[0].OK || results[0].Status != "OK" {
			t.Errorf("unexpected result: %+v", results[0])
		}
	})

	t.Run("nxdomain_returns_error", func(t *testing.T) {
		svcType, _ := dnsCloudflareServiceType(t)
		f := &fakeDNSQM{
			clustersResp: &quartermasterpb.ListClustersResponse{},
			nodesByType: map[string]*quartermasterpb.ListHealthyNodesForDNSResponse{
				svcType: dnsNodes("203.0.113.30"),
			},
		}
		lookup := func(string) ([]string, error) { return nil, errors.New("no such host") }

		var buf bytes.Buffer
		err := runDNSDoctor(context.Background(), &buf, f, "frameworks.network", lookup, false)
		if err == nil {
			t.Fatal("expected DNS mismatch error for NXDOMAIN")
		}
		if !strings.Contains(buf.String(), "NXDOMAIN") {
			t.Errorf("expected NXDOMAIN status in output, got:\n%s", buf.String())
		}
	})

	t.Run("mismatch_returns_error", func(t *testing.T) {
		svcType, _ := dnsCloudflareServiceType(t)
		f := &fakeDNSQM{
			clustersResp: &quartermasterpb.ListClustersResponse{},
			nodesByType: map[string]*quartermasterpb.ListHealthyNodesForDNSResponse{
				svcType: dnsNodes("203.0.113.40"),
			},
		}
		// resolves, but to a different IP set.
		lookup := func(string) ([]string, error) { return []string{"198.51.100.99"}, nil }

		var buf bytes.Buffer
		err := runDNSDoctor(context.Background(), &buf, f, "frameworks.network", lookup, false)
		if err == nil {
			t.Fatal("expected DNS mismatch error")
		}
		if !strings.Contains(buf.String(), "MISMATCH") {
			t.Errorf("expected MISMATCH status in output, got:\n%s", buf.String())
		}
	})

	t.Run("list_clusters_error", func(t *testing.T) {
		f := &fakeDNSQM{clustersErr: errors.New("boom")}
		lookup := func(string) ([]string, error) { return nil, nil }

		var buf bytes.Buffer
		err := runDNSDoctor(context.Background(), &buf, f, "frameworks.network", lookup, false)
		if err == nil || !strings.Contains(err.Error(), "failed to list clusters") {
			t.Fatalf("expected list clusters error, got %v", err)
		}
		// ListHealthyNodesForDNS must not have been called after the cluster failure.
		if len(f.gotServiceReqs) != 0 {
			t.Errorf("expected no node queries after cluster failure, got %v", f.gotServiceReqs)
		}
	})

	t.Run("list_nodes_error", func(t *testing.T) {
		f := &fakeDNSQM{
			clustersResp: &quartermasterpb.ListClustersResponse{},
			nodesErr:     errors.New("nodes down"),
		}
		lookup := func(string) ([]string, error) { return nil, nil }

		var buf bytes.Buffer
		err := runDNSDoctor(context.Background(), &buf, f, "frameworks.network", lookup, false)
		if err == nil || !strings.Contains(err.Error(), "failed to get healthy nodes") {
			t.Fatalf("expected healthy-nodes error, got %v", err)
		}
	})

	t.Run("empty_inventory_no_records", func(t *testing.T) {
		// No clusters and no nodes -> no expected records -> healthy, no error.
		f := &fakeDNSQM{
			clustersResp: &quartermasterpb.ListClustersResponse{},
			nodesByType:  map[string]*quartermasterpb.ListHealthyNodesForDNSResponse{},
		}
		lookupCalled := false
		lookup := func(string) ([]string, error) {
			lookupCalled = true
			return nil, nil
		}

		var buf bytes.Buffer
		if err := runDNSDoctor(context.Background(), &buf, f, "frameworks.network", lookup, false); err != nil {
			t.Fatalf("expected no error for empty inventory, got %v", err)
		}
		if lookupCalled {
			t.Error("lookupHost should not be called when there are no expected records")
		}
	})
}

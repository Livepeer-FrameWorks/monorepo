package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

type scopedAliasClient struct {
	calls []string
	fail  string
}

func (f *scopedAliasClient) ListAliasedTenantsForCluster(_ context.Context, cluster string) (*quartermasterpb.ListAliasedTenantsForClusterResponse, error) {
	f.calls = append(f.calls, cluster)
	if cluster == f.fail {
		return nil, errors.New("inventory unavailable")
	}
	if cluster == "media-us-1" || cluster == "private-a" {
		return &quartermasterpb.ListAliasedTenantsForClusterResponse{Tenants: []*quartermasterpb.AliasedTenantRef{{TenantId: "t-acme", Subdomain: "acme"}}}, nil
	}
	return &quartermasterpb.ListAliasedTenantsForClusterResponse{}, nil
}

type aliasAssignmentRegistry struct{ *fakeServiceRegistryClient }

func (*aliasAssignmentRegistry) ListServiceClusterAssignments(_ context.Context, instanceID, serviceType string) (*quartermasterpb.ListServiceClusterAssignmentsResponse, error) {
	return &quartermasterpb.ListServiceClusterAssignmentsResponse{ClusterIds: []string{"media-us-1", "private-a", "private-a"}}, nil
}
func TestTenantAliasCertificatesUseServedMediaClusters(t *testing.T) {
	a, root, _ := newAliasTestAgent(t, foghornRegistry(), &fakeAliasedTenantsClient{}, &fakeCertificateClient{tlsBundles: map[string]*dnspb.GetTLSBundleResponse{"tenant:t-acme": aliasTestBundle(t, "t-acme", "acme", "v1")}})
	a.clusterID = "regional-us-primary"
	a.registryClient = &aliasAssignmentRegistry{&fakeServiceRegistryClient{responses: []*quartermasterpb.ListServiceInstancesResponse{{Instances: []*quartermasterpb.ServiceInstance{{ServiceId: "foghorn", InstanceId: "foghorn-us-1", ClusterId: "media-us-1", Status: "running"}}}}}}
	aliases := &scopedAliasClient{}
	a.aliasClient = aliases
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	if !reflect.DeepEqual(aliases.calls, []string{"media-us-1", "private-a"}) {
		t.Fatalf("tenant eligibility used host scope instead of served scopes: %v", aliases.calls)
	}
	inventory, err := a.loadAliasInventory()
	if err != nil {
		t.Fatal(err)
	}
	if _, callErr := os.Stat(filepath.Join(root, "tenant-alias", inventory["t-acme"].Path, "tls.crt")); callErr != nil {
		t.Fatalf("served tenant's certificate not installed: %v", callErr)
	}
}

type paginatedAliasRegistry struct {
	*fakeServiceRegistryClient
	assignments     map[string][]string
	assignmentCalls []string
}

func (r *paginatedAliasRegistry) ListServiceClusterAssignments(_ context.Context, id, _ string) (*quartermasterpb.ListServiceClusterAssignmentsResponse, error) {
	r.assignmentCalls = append(r.assignmentCalls, id)
	return &quartermasterpb.ListServiceClusterAssignmentsResponse{ClusterIds: r.assignments[id]}, nil
}
func TestTenantAliasCertificatesUnionAllLocalInstancesAndPages(t *testing.T) {
	cursor := "next-page"
	registry := &paginatedAliasRegistry{fakeServiceRegistryClient: &fakeServiceRegistryClient{responses: []*quartermasterpb.ListServiceInstancesResponse{
		{Instances: []*quartermasterpb.ServiceInstance{{ServiceId: "foghorn", InstanceId: "fh-us", ClusterId: "media-us-1", Status: "running"}}, Pagination: &commonpb.CursorPaginationResponse{HasNextPage: true, EndCursor: &cursor}},
		{Instances: []*quartermasterpb.ServiceInstance{{ServiceId: "foghorn", InstanceId: "fh-eu", ClusterId: "media-eu-1", Status: "running"}, {ServiceId: "foghorn", InstanceId: "removed", ClusterId: "foreign", Status: "stopped"}}},
	}}, assignments: map[string][]string{"fh-us": {"private-a", "private-a"}, "fh-eu": {"private-b"}}}
	a, _, _ := newAliasTestAgent(t, foghornRegistry(), &fakeAliasedTenantsClient{}, &fakeCertificateClient{})
	a.registryClient = registry
	scopes, err := a.localAliasClusters(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(scopes, []string{"media-eu-1", "media-us-1", "private-a", "private-b"}) {
		t.Fatalf("served media scopes: %v", scopes)
	}
	if !reflect.DeepEqual(registry.assignmentCalls, []string{"fh-eu", "fh-us"}) {
		t.Fatalf("assignment discovery included an inactive instance: %v", registry.assignmentCalls)
	}
	if len(registry.requests) != 2 || registry.requests[1].GetAfter() != cursor {
		t.Fatalf("local instance pagination was not followed: %v", registry.requests)
	}
}

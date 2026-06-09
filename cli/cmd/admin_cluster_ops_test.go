package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func int32Ptr(v int32) *int32 { return &v }

type fakeAdminClusterOpsClient struct {
	accessResp *quartermasterpb.ClustersAccessResponse
	accessErr  error

	grantReq   *quartermasterpb.GrantClusterAccessRequest
	grantCalls int
	grantErr   error

	subsResp *quartermasterpb.ListClustersResponse
	subsErr  error

	getClusterResp *quartermasterpb.ClusterResponse
	getClusterErr  error
	discoverResp   *quartermasterpb.ServiceDiscoveryResponse
	discoverErr    error

	enableResp *quartermasterpb.EnableSelfHostingResponse
	enableErr  error

	enrollResp *quartermasterpb.CreateBootstrapTokenResponse
	enrollErr  error
}

func (f *fakeAdminClusterOpsClient) ListClustersForTenant(_ context.Context, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ClustersAccessResponse, error) {
	return f.accessResp, f.accessErr
}

func (f *fakeAdminClusterOpsClient) GrantClusterAccess(_ context.Context, req *quartermasterpb.GrantClusterAccessRequest) error {
	f.grantCalls++
	f.grantReq = req
	return f.grantErr
}

func (f *fakeAdminClusterOpsClient) ListMySubscriptions(_ context.Context, _ *quartermasterpb.ListMySubscriptionsRequest) (*quartermasterpb.ListClustersResponse, error) {
	return f.subsResp, f.subsErr
}

func (f *fakeAdminClusterOpsClient) GetCluster(_ context.Context, _ string) (*quartermasterpb.ClusterResponse, error) {
	return f.getClusterResp, f.getClusterErr
}

func (f *fakeAdminClusterOpsClient) DiscoverServices(_ context.Context, _, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	return f.discoverResp, f.discoverErr
}

func (f *fakeAdminClusterOpsClient) EnableSelfHosting(_ context.Context, _ *quartermasterpb.EnableSelfHostingRequest) (*quartermasterpb.EnableSelfHostingResponse, error) {
	return f.enableResp, f.enableErr
}

func (f *fakeAdminClusterOpsClient) CreateEnrollmentToken(_ context.Context, _ *quartermasterpb.CreateEnrollmentTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error) {
	return f.enrollResp, f.enrollErr
}

func TestRunClusterAccessList(t *testing.T) {
	fake := &fakeAdminClusterOpsClient{accessResp: &quartermasterpb.ClustersAccessResponse{
		Clusters: []*quartermasterpb.ClusterAccessEntry{
			{ClusterId: "c-1", ClusterName: "eu-1", AccessLevel: "read"},
		},
	}}
	var buf bytes.Buffer
	if err := runClusterAccessList(context.Background(), &buf, fake, "", "tenant-1", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Accessible clusters (1)", "eu-1", "c-1", "access=read"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	var jbuf bytes.Buffer
	if err := runClusterAccessList(context.Background(), &jbuf, fake, "", "tenant-1", true); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !json.Valid(jbuf.Bytes()) {
		t.Errorf("not valid JSON: %s", jbuf.String())
	}

	errFake := &fakeAdminClusterOpsClient{accessErr: errors.New("rpc down")}
	if err := runClusterAccessList(context.Background(), &bytes.Buffer{}, errFake, "", "t", false); err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestRunClusterAccessGrant(t *testing.T) {
	fake := &fakeAdminClusterOpsClient{}
	req := &quartermasterpb.GrantClusterAccessRequest{TenantId: "tenant-1", ClusterId: "c-1"}
	var buf bytes.Buffer
	if err := runClusterAccessGrant(context.Background(), &buf, fake, "", req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.grantCalls != 1 || fake.grantReq != req {
		t.Errorf("grant calls=%d forwarded=%v", fake.grantCalls, fake.grantReq == req)
	}
	if !strings.Contains(buf.String(), "Granted access to cluster c-1 for tenant tenant-1") {
		t.Errorf("missing success line: %q", buf.String())
	}

	errFake := &fakeAdminClusterOpsClient{grantErr: errors.New("denied")}
	if err := runClusterAccessGrant(context.Background(), &bytes.Buffer{}, errFake, "", req); err == nil {
		t.Fatal("expected grant error to propagate")
	}
}

func TestRunClusterSubscriptionsList(t *testing.T) {
	fake := &fakeAdminClusterOpsClient{subsResp: &quartermasterpb.ListClustersResponse{
		Clusters: []*quartermasterpb.InfrastructureCluster{
			{ClusterId: "c-1", ClusterName: "eu-1", ClusterType: "media"},
		},
	}}
	var buf bytes.Buffer
	if err := runClusterSubscriptionsList(context.Background(), &buf, fake, "", "tenant-1", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Subscriptions (1)", "eu-1", "c-1", "media"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	var jbuf bytes.Buffer
	if err := runClusterSubscriptionsList(context.Background(), &jbuf, fake, "", "t", true); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !json.Valid(jbuf.Bytes()) {
		t.Errorf("not valid JSON: %s", jbuf.String())
	}

	errFake := &fakeAdminClusterOpsClient{subsErr: errors.New("rpc down")}
	if err := runClusterSubscriptionsList(context.Background(), &bytes.Buffer{}, errFake, "", "t", false); err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestRunClusterCertStatus_ActiveAndPending(t *testing.T) {
	mk := func(health string) *fakeAdminClusterOpsClient {
		return &fakeAdminClusterOpsClient{
			getClusterResp: &quartermasterpb.ClusterResponse{Cluster: &quartermasterpb.InfrastructureCluster{
				ClusterId: "c-1", ClusterName: "eu-1", ClusterType: "media", BaseUrl: "https://eu-1", HealthStatus: health, IsActive: health == "active",
			}},
			discoverResp: &quartermasterpb.ServiceDiscoveryResponse{
				Instances: []*quartermasterpb.ServiceInstance{{Host: bootstrapStrPtr("fh-1"), Port: int32Ptr(18008), Status: "healthy"}},
			},
		}
	}

	var active bytes.Buffer
	if err := runClusterCertStatus(context.Background(), &active, mk("active"), "", "c-1"); err != nil {
		t.Fatalf("active: %v", err)
	}
	for _, want := range []string{"Cluster: eu-1 (c-1)", "fh-1:18008", "status=healthy", "cert:       ready (cluster active)"} {
		if !strings.Contains(active.String(), want) {
			t.Errorf("active output missing %q:\n%s", want, active.String())
		}
	}

	var pending bytes.Buffer
	if err := runClusterCertStatus(context.Background(), &pending, mk("provisioning"), "", "c-1"); err != nil {
		t.Fatalf("pending: %v", err)
	}
	if !strings.Contains(pending.String(), "cert:       pending (cluster provisioning)") {
		t.Errorf("pending output missing cert-pending line:\n%s", pending.String())
	}
}

func TestRunClusterCertStatus_Errors(t *testing.T) {
	getErr := &fakeAdminClusterOpsClient{getClusterErr: errors.New("nope")}
	if err := runClusterCertStatus(context.Background(), &bytes.Buffer{}, getErr, "", "c-1"); err == nil {
		t.Fatal("expected get-cluster error to propagate")
	}
	discErr := &fakeAdminClusterOpsClient{
		getClusterResp: &quartermasterpb.ClusterResponse{Cluster: &quartermasterpb.InfrastructureCluster{ClusterId: "c-1"}},
		discoverErr:    errors.New("nope"),
	}
	if err := runClusterCertStatus(context.Background(), &bytes.Buffer{}, discErr, "", "c-1"); err == nil {
		t.Fatal("expected discover error to propagate")
	}
}

func TestRunClusterCreateEdge(t *testing.T) {
	fake := &fakeAdminClusterOpsClient{enableResp: &quartermasterpb.EnableSelfHostingResponse{
		Cluster:        &quartermasterpb.InfrastructureCluster{ClusterId: "c-1", ClusterName: "edge-eu", BaseUrl: "https://edge-eu"},
		BootstrapToken: &quartermasterpb.BootstrapToken{Token: "tok-xyz", ExpiresAt: timestamppb.New(refTime())},
		FoghornAddr:    "foghorn.eu:18008",
	}}
	var buf bytes.Buffer
	if err := runClusterCreateEdge(context.Background(), &buf, fake, "", &quartermasterpb.EnableSelfHostingRequest{}, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"edge-eu", "c-1", "foghorn.eu:18008", "tok-xyz", "edge deploy"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	var jbuf bytes.Buffer
	if err := runClusterCreateEdge(context.Background(), &jbuf, fake, "", &quartermasterpb.EnableSelfHostingRequest{}, true); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !json.Valid(jbuf.Bytes()) {
		t.Errorf("not valid JSON: %s", jbuf.String())
	}

	errFake := &fakeAdminClusterOpsClient{enableErr: errors.New("boom")}
	if err := runClusterCreateEdge(context.Background(), &bytes.Buffer{}, errFake, "", &quartermasterpb.EnableSelfHostingRequest{}, false); err == nil {
		t.Fatal("expected enable error to propagate")
	}
}

func TestRunClusterEnrollmentToken(t *testing.T) {
	fake := &fakeAdminClusterOpsClient{enrollResp: &quartermasterpb.CreateBootstrapTokenResponse{
		Token: &quartermasterpb.BootstrapToken{Token: "tok-abc", ExpiresAt: timestamppb.New(refTime())},
	}}
	var buf bytes.Buffer
	if err := runClusterEnrollmentToken(context.Background(), &buf, fake, "", "c-1", &quartermasterpb.CreateEnrollmentTokenRequest{}, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	// "cluster" field is the passed clusterID, not a response field.
	for _, want := range []string{"tok-abc", "c-1", "edge deploy"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	var jbuf bytes.Buffer
	if err := runClusterEnrollmentToken(context.Background(), &jbuf, fake, "", "c-1", &quartermasterpb.CreateEnrollmentTokenRequest{}, true); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !json.Valid(jbuf.Bytes()) {
		t.Errorf("not valid JSON: %s", jbuf.String())
	}

	errFake := &fakeAdminClusterOpsClient{enrollErr: errors.New("boom")}
	if err := runClusterEnrollmentToken(context.Background(), &bytes.Buffer{}, errFake, "", "c-1", &quartermasterpb.CreateEnrollmentTokenRequest{}, false); err == nil {
		t.Fatal("expected enroll error to propagate")
	}
}

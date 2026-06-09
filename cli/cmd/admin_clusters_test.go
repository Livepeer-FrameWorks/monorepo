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
)

type fakeAdminClustersClient struct {
	listResp *quartermasterpb.ListClustersResponse
	listErr  error

	createReq  *quartermasterpb.CreateClusterRequest
	createResp *quartermasterpb.ClusterResponse
	createErr  error

	updateReq  *quartermasterpb.UpdateClusterRequest
	updateResp *quartermasterpb.ClusterResponse
	updateErr  error
}

func (f *fakeAdminClustersClient) ListClusters(_ context.Context, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
	return f.listResp, f.listErr
}

func (f *fakeAdminClustersClient) CreateCluster(_ context.Context, req *quartermasterpb.CreateClusterRequest) (*quartermasterpb.ClusterResponse, error) {
	f.createReq = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.createResp, nil
}

func (f *fakeAdminClustersClient) UpdateCluster(_ context.Context, req *quartermasterpb.UpdateClusterRequest) (*quartermasterpb.ClusterResponse, error) {
	f.updateReq = req
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return f.updateResp, nil
}

func clusterResp() *quartermasterpb.ClusterResponse {
	return &quartermasterpb.ClusterResponse{
		Cluster: &quartermasterpb.InfrastructureCluster{ClusterId: "c-1", ClusterName: "eu-1"},
	}
}

func TestRunClustersList_Renders(t *testing.T) {
	fake := &fakeAdminClustersClient{listResp: &quartermasterpb.ListClustersResponse{
		Clusters: []*quartermasterpb.InfrastructureCluster{
			{ClusterId: "c-1", ClusterName: "eu-1", ClusterType: "media", BaseUrl: "https://eu-1.example.com"},
			{ClusterId: "c-2", ClusterName: "us-1", ClusterType: "edge", BaseUrl: "https://us-1.example.com"},
		},
	}}
	var buf bytes.Buffer
	if err := runClustersList(context.Background(), &buf, fake, "", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Clusters (2)", "eu-1", "c-1", "media", "https://eu-1.example.com", "us-1", "edge"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunClustersList_JSON(t *testing.T) {
	fake := &fakeAdminClustersClient{listResp: &quartermasterpb.ListClustersResponse{
		Clusters: []*quartermasterpb.InfrastructureCluster{{ClusterId: "c-1", ClusterName: "eu-1"}},
	}}
	var buf bytes.Buffer
	if err := runClustersList(context.Background(), &buf, fake, "", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if _, ok := decoded["clusters"]; !ok {
		t.Errorf("json missing clusters field: %s", buf.String())
	}
}

func TestRunClustersList_Error(t *testing.T) {
	fake := &fakeAdminClustersClient{listErr: errors.New("rpc down")}
	var buf bytes.Buffer
	if err := runClustersList(context.Background(), &buf, fake, "", false); err == nil {
		t.Fatal("expected list error to propagate")
	}
}

func TestRunClusterCreate_ForwardsAndRenders(t *testing.T) {
	fake := &fakeAdminClustersClient{createResp: clusterResp()}
	req := &quartermasterpb.CreateClusterRequest{ClusterId: "c-1", ClusterName: "eu-1"}
	var buf bytes.Buffer
	if err := runClusterCreate(context.Background(), &buf, fake, "jwt", req, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.createReq != req {
		t.Error("request was not forwarded unchanged")
	}
	if !strings.Contains(buf.String(), "Created cluster eu-1 (c-1)") {
		t.Errorf("missing success line: %q", buf.String())
	}
}

func TestRunClusterCreate_JSONAndError(t *testing.T) {
	fake := &fakeAdminClustersClient{createResp: clusterResp()}
	var buf bytes.Buffer
	if err := runClusterCreate(context.Background(), &buf, fake, "", &quartermasterpb.CreateClusterRequest{}, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}

	errFake := &fakeAdminClustersClient{createErr: errors.New("boom")}
	var errBuf bytes.Buffer
	if err := runClusterCreate(context.Background(), &errBuf, errFake, "", &quartermasterpb.CreateClusterRequest{}, false); err == nil {
		t.Fatal("expected create error to propagate")
	}
}

func TestRunClusterUpdate_ForwardsAndRenders(t *testing.T) {
	fake := &fakeAdminClustersClient{updateResp: clusterResp()}
	req := &quartermasterpb.UpdateClusterRequest{ClusterId: "c-1"}
	var buf bytes.Buffer
	if err := runClusterUpdate(context.Background(), &buf, fake, "jwt", req, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.updateReq != req {
		t.Error("request was not forwarded unchanged")
	}
	if !strings.Contains(buf.String(), "Updated cluster eu-1 (c-1)") {
		t.Errorf("missing success line: %q", buf.String())
	}
}

func TestRunClusterUpdate_JSONAndError(t *testing.T) {
	fake := &fakeAdminClustersClient{updateResp: clusterResp()}
	var buf bytes.Buffer
	if err := runClusterUpdate(context.Background(), &buf, fake, "", &quartermasterpb.UpdateClusterRequest{}, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}

	errFake := &fakeAdminClustersClient{updateErr: errors.New("boom")}
	var errBuf bytes.Buffer
	if err := runClusterUpdate(context.Background(), &errBuf, errFake, "", &quartermasterpb.UpdateClusterRequest{}, false); err == nil {
		t.Fatal("expected update error to propagate")
	}
}

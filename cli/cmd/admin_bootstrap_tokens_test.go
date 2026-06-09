package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"time"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func refTime() time.Time { return time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC) }

type fakeAdminBootstrapTokensClient struct {
	createReq  *quartermasterpb.CreateBootstrapTokenRequest
	createResp *quartermasterpb.CreateBootstrapTokenResponse
	createErr  error

	listResp  *quartermasterpb.ListBootstrapTokensResponse
	listErr   error
	listCalls int

	revokedID   string
	revokeCalls int
	revokeErr   error
}

func (f *fakeAdminBootstrapTokensClient) CreateBootstrapToken(_ context.Context, req *quartermasterpb.CreateBootstrapTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error) {
	f.createReq = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.createResp, nil
}

func (f *fakeAdminBootstrapTokensClient) ListBootstrapTokens(_ context.Context, _, _ string, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListBootstrapTokensResponse, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listResp, nil
}

func (f *fakeAdminBootstrapTokensClient) RevokeBootstrapToken(_ context.Context, tokenID string) error {
	f.revokeCalls++
	f.revokedID = tokenID
	return f.revokeErr
}

func bootstrapStrPtr(s string) *string { return &s }

func bootstrapCreateResp(kind string) *quartermasterpb.CreateBootstrapTokenResponse {
	return &quartermasterpb.CreateBootstrapTokenResponse{
		Token: &quartermasterpb.BootstrapToken{
			Id:        "id-1",
			Token:     "tok-secret",
			Kind:      kind,
			ExpiresAt: timestamppb.New(refTime()),
		},
	}
}

func TestRunBootstrapTokenCreate_NonEdgeNoNextSteps(t *testing.T) {
	fake := &fakeAdminBootstrapTokensClient{createResp: bootstrapCreateResp("service")}
	req := &quartermasterpb.CreateBootstrapTokenRequest{Name: "svc-token"}
	var buf bytes.Buffer
	if err := runBootstrapTokenCreate(context.Background(), &buf, fake, "jwt-abc", req, "service", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.createReq != req {
		t.Error("request was not forwarded unchanged to the client")
	}
	out := buf.String()
	if !strings.Contains(out, "tok-secret") || !strings.Contains(out, "service") {
		t.Errorf("output missing token/kind: %q", out)
	}
	if strings.Contains(out, "frameworks edge deploy") {
		t.Errorf("non-edge token must not print enrollment next-step: %q", out)
	}
}

func TestRunBootstrapTokenCreate_EdgeNodePrintsNextStep(t *testing.T) {
	fake := &fakeAdminBootstrapTokensClient{createResp: bootstrapCreateResp("edge_node")}
	var buf bytes.Buffer
	if err := runBootstrapTokenCreate(context.Background(), &buf, fake, "", &quartermasterpb.CreateBootstrapTokenRequest{}, "edge_node", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "frameworks edge deploy") || !strings.Contains(out, "tok-secret") {
		t.Errorf("edge_node token must print enrollment next-step with the token: %q", out)
	}
}

func TestRunBootstrapTokenCreate_JSON(t *testing.T) {
	fake := &fakeAdminBootstrapTokensClient{createResp: bootstrapCreateResp("service")}
	var buf bytes.Buffer
	if err := runBootstrapTokenCreate(context.Background(), &buf, fake, "", &quartermasterpb.CreateBootstrapTokenRequest{}, "service", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if _, ok := decoded["token"]; !ok {
		t.Errorf("json missing token field: %s", buf.String())
	}
}

func TestRunBootstrapTokenCreate_Error(t *testing.T) {
	fake := &fakeAdminBootstrapTokensClient{createErr: errors.New("boom")}
	var buf bytes.Buffer
	if err := runBootstrapTokenCreate(context.Background(), &buf, fake, "", &quartermasterpb.CreateBootstrapTokenRequest{}, "service", false); err == nil {
		t.Fatal("expected create error to propagate")
	}
}

func TestRunBootstrapTokensList_RendersNullableFields(t *testing.T) {
	fake := &fakeAdminBootstrapTokensClient{listResp: &quartermasterpb.ListBootstrapTokensResponse{
		Tokens: []*quartermasterpb.BootstrapToken{
			{Name: "anon", Id: "a", Kind: "edge_node", ExpiresAt: timestamppb.New(refTime())},
			{
				Name: "bound", Id: "b", Kind: "service",
				TenantId:  bootstrapStrPtr("tenant-1"),
				ClusterId: bootstrapStrPtr("cluster-1"),
				ExpiresAt: timestamppb.New(refTime()),
				UsedAt:    timestamppb.New(refTime()),
			},
		},
	}}
	var buf bytes.Buffer
	if err := runBootstrapTokensList(context.Background(), &buf, fake, "", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Bootstrap tokens (2)", "anon", "<any>", "bound", "tenant-1", "cluster-1", "used"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunBootstrapTokensList_JSON(t *testing.T) {
	fake := &fakeAdminBootstrapTokensClient{listResp: &quartermasterpb.ListBootstrapTokensResponse{
		Tokens: []*quartermasterpb.BootstrapToken{{Name: "x", Id: "a", Kind: "service", ExpiresAt: timestamppb.New(refTime())}},
	}}
	var buf bytes.Buffer
	if err := runBootstrapTokensList(context.Background(), &buf, fake, "", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if _, ok := decoded["tokens"]; !ok {
		t.Errorf("json missing tokens field: %s", buf.String())
	}
}

func TestRunBootstrapTokensList_Error(t *testing.T) {
	fake := &fakeAdminBootstrapTokensClient{listErr: errors.New("rpc down")}
	var buf bytes.Buffer
	if err := runBootstrapTokensList(context.Background(), &buf, fake, "", false); err == nil {
		t.Fatal("expected list error to propagate")
	}
}

func TestRunBootstrapTokensRevoke_ByID(t *testing.T) {
	fake := &fakeAdminBootstrapTokensClient{}
	var buf bytes.Buffer
	if err := runBootstrapTokensRevoke(context.Background(), &buf, fake, "", validTokenUUID, "", confirmYes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.revokeCalls != 1 || fake.revokedID != validTokenUUID {
		t.Errorf("revoke calls=%d id=%q, want 1 / %s", fake.revokeCalls, fake.revokedID, validTokenUUID)
	}
	if fake.listCalls != 0 {
		t.Errorf("list should not be called when ID given, got %d", fake.listCalls)
	}
}

func TestRunBootstrapTokensRevoke_ByID_InvalidUUID(t *testing.T) {
	fake := &fakeAdminBootstrapTokensClient{}
	var buf bytes.Buffer
	if err := runBootstrapTokensRevoke(context.Background(), &buf, fake, "", "nope", "", confirmYes); err == nil {
		t.Fatal("expected error for malformed token ID")
	}
	if fake.revokeCalls != 0 {
		t.Error("must not revoke on invalid ID")
	}
}

func TestRunBootstrapTokensRevoke_ByName(t *testing.T) {
	fake := &fakeAdminBootstrapTokensClient{listResp: &quartermasterpb.ListBootstrapTokensResponse{
		Tokens: []*quartermasterpb.BootstrapToken{
			{Name: "other", Id: "x"},
			{Name: "ci", Id: "target"},
		},
	}}
	var buf bytes.Buffer
	if err := runBootstrapTokensRevoke(context.Background(), &buf, fake, "", "", "ci", confirmYes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.revokedID != "target" {
		t.Errorf("revoked id = %q, want target", fake.revokedID)
	}
}

func TestRunBootstrapTokensRevoke_ByName_NotFound(t *testing.T) {
	fake := &fakeAdminBootstrapTokensClient{listResp: &quartermasterpb.ListBootstrapTokensResponse{
		Tokens: []*quartermasterpb.BootstrapToken{{Name: "other", Id: "x"}},
	}}
	var buf bytes.Buffer
	if err := runBootstrapTokensRevoke(context.Background(), &buf, fake, "", "", "ci", confirmYes); err == nil {
		t.Fatal("expected not-found error")
	}
	if fake.revokeCalls != 0 {
		t.Error("must not revoke when name unresolved")
	}
}

func TestRunBootstrapTokensRevoke_NoArgs(t *testing.T) {
	fake := &fakeAdminBootstrapTokensClient{}
	var buf bytes.Buffer
	if err := runBootstrapTokensRevoke(context.Background(), &buf, fake, "", "", "", confirmYes); err == nil {
		t.Fatal("expected error when neither ID nor --name supplied")
	}
}

func TestRunBootstrapTokensRevoke_ConfirmFalse(t *testing.T) {
	fake := &fakeAdminBootstrapTokensClient{}
	var buf bytes.Buffer
	if err := runBootstrapTokensRevoke(context.Background(), &buf, fake, "", validTokenUUID, "", confirmNo); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.revokeCalls != 0 {
		t.Error("declining confirmation must not revoke")
	}
	if !strings.Contains(buf.String(), "Cancelled") {
		t.Errorf("expected Cancelled message: %q", buf.String())
	}
}

func TestRunBootstrapTokensRevoke_RevokeError(t *testing.T) {
	fake := &fakeAdminBootstrapTokensClient{revokeErr: errors.New("rpc rejected")}
	var buf bytes.Buffer
	if err := runBootstrapTokensRevoke(context.Background(), &buf, fake, "", validTokenUUID, "", confirmYes); err == nil {
		t.Fatal("expected revoke error to propagate")
	}
}

// --- tenants ---

type fakeAdminTenantsClient struct {
	resp *quartermasterpb.ListTenantsResponse
	err  error
}

func (f *fakeAdminTenantsClient) ListTenants(_ context.Context, _ *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error) {
	return f.resp, f.err
}

func TestRunTenantsList_Renders(t *testing.T) {
	fake := &fakeAdminTenantsClient{resp: &quartermasterpb.ListTenantsResponse{
		Tenants: []*quartermasterpb.Tenant{
			{Id: "t1", Name: "acme", DeploymentTier: "pro"},
		},
	}}
	var buf bytes.Buffer
	if err := runTenantsList(context.Background(), &buf, fake, "", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Tenants (1)", "acme", "t1", "pro"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
}

func TestRunTenantsList_JSON(t *testing.T) {
	fake := &fakeAdminTenantsClient{resp: &quartermasterpb.ListTenantsResponse{
		Tenants: []*quartermasterpb.Tenant{{Id: "t1", Name: "acme", DeploymentTier: "pro"}},
	}}
	var buf bytes.Buffer
	if err := runTenantsList(context.Background(), &buf, fake, "", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if _, ok := decoded["tenants"]; !ok {
		t.Errorf("json missing tenants field: %s", buf.String())
	}
}

func TestRunTenantsList_Error(t *testing.T) {
	fake := &fakeAdminTenantsClient{err: errors.New("rpc down")}
	var buf bytes.Buffer
	if err := runTenantsList(context.Background(), &buf, fake, "", false); err == nil {
		t.Fatal("expected tenants list error to propagate")
	}
}

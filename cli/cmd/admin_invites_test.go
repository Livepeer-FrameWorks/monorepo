package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeAdminInvitesClient struct {
	createReq  *quartermasterpb.CreateClusterInviteRequest
	createResp *quartermasterpb.ClusterInvite
	createErr  error

	listResp *quartermasterpb.ListClusterInvitesResponse
	listErr  error

	revokeReq   *quartermasterpb.RevokeClusterInviteRequest
	revokeCalls int
	revokeErr   error

	mineResp *quartermasterpb.ListClusterInvitesResponse
	mineErr  error

	acceptResp *quartermasterpb.ClusterSubscription
	acceptErr  error
}

func (f *fakeAdminInvitesClient) CreateClusterInvite(_ context.Context, req *quartermasterpb.CreateClusterInviteRequest) (*quartermasterpb.ClusterInvite, error) {
	f.createReq = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.createResp, nil
}

func (f *fakeAdminInvitesClient) ListClusterInvites(_ context.Context, _ *quartermasterpb.ListClusterInvitesRequest) (*quartermasterpb.ListClusterInvitesResponse, error) {
	return f.listResp, f.listErr
}

func (f *fakeAdminInvitesClient) RevokeClusterInvite(_ context.Context, req *quartermasterpb.RevokeClusterInviteRequest) error {
	f.revokeCalls++
	f.revokeReq = req
	return f.revokeErr
}

func (f *fakeAdminInvitesClient) ListMyClusterInvites(_ context.Context, _ *quartermasterpb.ListMyClusterInvitesRequest) (*quartermasterpb.ListClusterInvitesResponse, error) {
	return f.mineResp, f.mineErr
}

func (f *fakeAdminInvitesClient) AcceptClusterInvite(_ context.Context, _ *quartermasterpb.AcceptClusterInviteRequest) (*quartermasterpb.ClusterSubscription, error) {
	return f.acceptResp, f.acceptErr
}

func TestRunInviteCreate_ForwardsAndRenders(t *testing.T) {
	fake := &fakeAdminInvitesClient{createResp: &quartermasterpb.ClusterInvite{Id: "inv-1", InvitedTenantId: "tenant-9", InviteToken: "tok-xyz"}}
	req := &quartermasterpb.CreateClusterInviteRequest{ClusterId: "c-1"}
	var buf bytes.Buffer
	if err := runInviteCreate(context.Background(), &buf, fake, "jwt", req, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.createReq != req {
		t.Error("request not forwarded unchanged")
	}
	if !strings.Contains(buf.String(), "Created invite inv-1 for tenant tenant-9 (token=tok-xyz)") {
		t.Errorf("missing success line: %q", buf.String())
	}
}

func TestRunInviteCreate_JSONAndError(t *testing.T) {
	fake := &fakeAdminInvitesClient{createResp: &quartermasterpb.ClusterInvite{Id: "inv-1"}}
	var buf bytes.Buffer
	if err := runInviteCreate(context.Background(), &buf, fake, "", &quartermasterpb.CreateClusterInviteRequest{}, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !json.Valid(buf.Bytes()) {
		t.Errorf("output is not valid JSON: %s", buf.String())
	}
	errFake := &fakeAdminInvitesClient{createErr: errors.New("boom")}
	if err := runInviteCreate(context.Background(), &bytes.Buffer{}, errFake, "", &quartermasterpb.CreateClusterInviteRequest{}, false); err == nil {
		t.Fatal("expected create error to propagate")
	}
}

func TestRunInvitesList_RendersTenantAndExpiry(t *testing.T) {
	fake := &fakeAdminInvitesClient{listResp: &quartermasterpb.ListClusterInvitesResponse{
		Invites: []*quartermasterpb.ClusterInvite{
			{Id: "inv-1", InvitedTenantId: "tenant-9", Status: "pending", ExpiresAt: timestamppb.New(refTime())},
			{Id: "inv-2", InvitedTenantId: "tenant-8", Status: "accepted"}, // nil ExpiresAt → "-"
		},
	}}
	var buf bytes.Buffer
	if err := runInvitesList(context.Background(), &buf, fake, "", &quartermasterpb.ListClusterInvitesRequest{}, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Invites (2)", "inv-1", "tenant=tenant-9", "pending", "inv-2", "expires=-"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunInvitesList_Error(t *testing.T) {
	fake := &fakeAdminInvitesClient{listErr: errors.New("rpc down")}
	if err := runInvitesList(context.Background(), &bytes.Buffer{}, fake, "", &quartermasterpb.ListClusterInvitesRequest{}, false); err == nil {
		t.Fatal("expected list error to propagate")
	}
}

func TestRunInvitesList_JSON(t *testing.T) {
	fake := &fakeAdminInvitesClient{listResp: &quartermasterpb.ListClusterInvitesResponse{
		Invites: []*quartermasterpb.ClusterInvite{{Id: "inv-1"}},
	}}
	var buf bytes.Buffer
	if err := runInvitesList(context.Background(), &buf, fake, "", &quartermasterpb.ListClusterInvitesRequest{}, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !json.Valid(buf.Bytes()) {
		t.Errorf("output is not valid JSON: %s", buf.String())
	}
}

func TestRunInvitesListMine_JSON(t *testing.T) {
	fake := &fakeAdminInvitesClient{mineResp: &quartermasterpb.ListClusterInvitesResponse{
		Invites: []*quartermasterpb.ClusterInvite{{Id: "inv-1"}},
	}}
	var buf bytes.Buffer
	if err := runInvitesListMine(context.Background(), &buf, fake, "", &quartermasterpb.ListMyClusterInvitesRequest{}, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !json.Valid(buf.Bytes()) {
		t.Errorf("output is not valid JSON: %s", buf.String())
	}
}

func TestRunInviteRevoke(t *testing.T) {
	fake := &fakeAdminInvitesClient{}
	req := &quartermasterpb.RevokeClusterInviteRequest{InviteId: "inv-7"}
	var buf bytes.Buffer
	if err := runInviteRevoke(context.Background(), &buf, fake, "jwt", req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.revokeCalls != 1 || fake.revokeReq != req {
		t.Errorf("revoke calls=%d req forwarded=%v", fake.revokeCalls, fake.revokeReq == req)
	}
	if !strings.Contains(buf.String(), "Revoked invite inv-7") {
		t.Errorf("missing success line: %q", buf.String())
	}
}

func TestRunInviteRevoke_Error(t *testing.T) {
	fake := &fakeAdminInvitesClient{revokeErr: errors.New("rpc rejected")}
	if err := runInviteRevoke(context.Background(), &bytes.Buffer{}, fake, "", &quartermasterpb.RevokeClusterInviteRequest{InviteId: "x"}); err == nil {
		t.Fatal("expected revoke error to propagate")
	}
}

func TestRunInvitesListMine_RendersClusterColumn(t *testing.T) {
	fake := &fakeAdminInvitesClient{mineResp: &quartermasterpb.ListClusterInvitesResponse{
		Invites: []*quartermasterpb.ClusterInvite{
			{Id: "inv-1", ClusterId: "c-eu", Status: "pending", ExpiresAt: timestamppb.New(refTime())},
		},
	}}
	var buf bytes.Buffer
	if err := runInvitesListMine(context.Background(), &buf, fake, "", &quartermasterpb.ListMyClusterInvitesRequest{}, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	// list-mine shows cluster (not invited-tenant) — the column differs from runInvitesList.
	for _, want := range []string{"Invites (1)", "cluster=c-eu", "pending"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunInvitesListMine_Error(t *testing.T) {
	fake := &fakeAdminInvitesClient{mineErr: errors.New("rpc down")}
	if err := runInvitesListMine(context.Background(), &bytes.Buffer{}, fake, "", &quartermasterpb.ListMyClusterInvitesRequest{}, false); err == nil {
		t.Fatal("expected list-mine error to propagate")
	}
}

func TestRunInviteAccept_RendersResultAndNextStep(t *testing.T) {
	fake := &fakeAdminInvitesClient{acceptResp: &quartermasterpb.ClusterSubscription{
		ClusterId: "c-eu", TenantId: "tenant-9", AccessLevel: "read",
	}}
	var buf bytes.Buffer
	if err := runInviteAccept(context.Background(), &buf, fake, "jwt", &quartermasterpb.AcceptClusterInviteRequest{}, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"c-eu", "tenant-9", "read", "subscriptions list --tenant-id tenant-9"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunInviteAccept_JSONAndError(t *testing.T) {
	fake := &fakeAdminInvitesClient{acceptResp: &quartermasterpb.ClusterSubscription{TenantId: "t"}}
	var buf bytes.Buffer
	if err := runInviteAccept(context.Background(), &buf, fake, "", &quartermasterpb.AcceptClusterInviteRequest{}, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !json.Valid(buf.Bytes()) {
		t.Errorf("output is not valid JSON: %s", buf.String())
	}
	errFake := &fakeAdminInvitesClient{acceptErr: errors.New("boom")}
	if err := runInviteAccept(context.Background(), &bytes.Buffer{}, errFake, "", &quartermasterpb.AcceptClusterInviteRequest{}, false); err == nil {
		t.Fatal("expected accept error to propagate")
	}
}

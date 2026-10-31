package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

type fakeGrantList struct {
	request *purserpb.AdminListBillingGrantsRequest
}

func (f *fakeGrantList) AdminListBillingGrants(_ context.Context, req *purserpb.AdminListBillingGrantsRequest) (*purserpb.AdminListBillingGrantsResponse, error) {
	f.request = req
	return &purserpb.AdminListBillingGrantsResponse{Grants: []*purserpb.AdminBillingGrantEntry{{TenantId: "tenant-1", TierName: "enterprise", Grant: &purserpb.OperatorBillingGrant{FallbackTierName: "payg", Reason: "evaluation", Active: false, ExpiryError: "retired fallback", ExpiryFailures: 3, WaiveUsage: true, BasePrice: proto.String("0.00")}}}}, nil
}

func TestOperatorGrantListDisplaysFallbackAndHistory(t *testing.T) {
	client := &fakeGrantList{}
	var out bytes.Buffer
	req := &purserpb.AdminListBillingGrantsRequest{IncludeExpired: true, Limit: 10, Offset: 20}
	if err := runBillingGrantList(context.Background(), &out, client, "jwt", req, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tenant-1", "enterprise", "payg", "ended", "evaluation", "BASE FEE", "USAGE WAIVED", "EUR 0.00", "retired fallback"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("listing lacks %q: %s", want, out.String())
		}
	}
	if client.request != req {
		t.Fatal("listing lost pagination/history filters")
	}
}

func TestGrantFallbackFlagReachesRPC(t *testing.T) {
	req, err := (billingGrantRequest{BaseFee: "0", WaiveUsage: true, Reason: "trial", FallbackTier: "payg"}).toProto("tenant", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if req.GetFallbackTierName() != "payg" {
		t.Fatalf("fallback=%q", req.GetFallbackTierName())
	}
}

func TestTenantClosureNeedsExactName(t *testing.T) {
	for _, tc := range []struct {
		name string
		yes  bool
		ok   bool
	}{{"", true, false}, {"wrong", true, false}, {"Trial Tenant", true, true}, {"Trial Tenant", false, true}} {
		if err := confirmTenantClosure("Trial Tenant", tc.name, tc.yes); (err == nil) != tc.ok {
			t.Fatalf("confirmation %q yes=%v err=%v", tc.name, tc.yes, err)
		}
	}
}

type accountLifecycleFake struct {
	tenant  string
	active  bool
	lookups int
	closure *commodorepb.AdminDeactivateTenantRequest
}

func (f *accountLifecycleFake) AdminLookupUserByEmail(context.Context, string) (*commodorepb.AdminLookupUserByEmailResponse, error) {
	f.lookups++
	return &commodorepb.AdminLookupUserByEmailResponse{TenantId: f.tenant}, nil
}

func (f *accountLifecycleFake) AdminSetUserActive(_ context.Context, r *commodorepb.AdminSetUserActiveRequest) (*commodorepb.AdminSetUserActiveResponse, error) {
	f.tenant = r.TenantId
	f.active = r.Active
	return &commodorepb.AdminSetUserActiveResponse{TenantId: r.TenantId, Email: r.Email, IsActive: r.Active}, nil
}

func (f *accountLifecycleFake) AdminDeactivateTenant(_ context.Context, r *commodorepb.AdminDeactivateTenantRequest) error {
	f.closure = r
	return nil
}

func TestUserActivationResolvesEmailAndReportsJSON(t *testing.T) {
	for _, active := range []bool{false, true} {
		client := &accountLifecycleFake{tenant: "10000000-0000-4000-8000-000000000001"}
		var out bytes.Buffer
		if err := runAccountUserActive(context.Background(), &out, client, "jwt", &commodorepb.AdminSetUserActiveRequest{Email: "trial@example.test", Reason: "operator action", Active: active}, true); err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if client.lookups != 1 || client.active != active || result["tenant_id"] != client.tenant {
			t.Fatalf("active=%v result=%s client=%+v", active, out.String(), client)
		}
	}
	command := newAdminUsersActiveCmd(false)
	command.SetArgs([]string{"--user-id", "10000000-0000-4000-8000-000000000001", "--reason", "test"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "--tenant-id is required") {
		t.Fatalf("missing tenant validation=%v", err)
	}
}

func TestTenantClosurePromptJSONAndUUIDSpellings(t *testing.T) {
	const canonical = "abcdef01-abcd-4abc-8abc-abcdefabcdef"
	for _, spelling := range []string{strings.ToUpper(canonical), strings.ReplaceAll(canonical, "-", "")} {
		command := &cobra.Command{}
		command.SetContext(context.Background())
		var out, stderr bytes.Buffer
		command.SetOut(&out)
		command.SetErr(&stderr)
		command.SetIn(strings.NewReader("Exact Name\n"))
		client := &accountLifecycleFake{}
		if err := runTenantClosure(command, client, "jwt", spelling, "Exact Name", "", "operator closure", false, true); err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("stdout is not JSON: %s", out.String())
		}
		if !strings.Contains(stderr.String(), "Type the tenant name") || client.closure.GetTenantId() != canonical || result["active"] != false {
			t.Fatalf("result=%s stderr=%s request=%v", out.String(), stderr.String(), client.closure)
		}
		if confirmationErr := runTenantClosure(command, client, "jwt", spelling, "Exact Name", "Exact Name", "operator closure", false, true); confirmationErr == nil || !strings.Contains(confirmationErr.Error(), "--tenant-name requires --yes") {
			t.Fatalf("unattended confirmation without --yes=%v", confirmationErr)
		}

		stderr.Reset()
		out.Reset()
		if err := runTenantClosure(command, client, "jwt", spelling, "Exact Name", "Exact Name", "operator closure", true, true); err != nil {
			t.Fatal(err)
		}
		if stderr.Len() != 0 {
			t.Fatalf("explicit name prompted: %s", stderr.String())
		}
	}
}

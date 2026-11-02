package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type tierDowngradeRecoveryFake struct {
	requests []*purserpb.AdminRecoverTierDowngradeRequest
}

func (f *tierDowngradeRecoveryFake) AdminRecoverTierDowngrade(_ context.Context, r *purserpb.AdminRecoverTierDowngradeRequest) (*purserpb.AdminRecoverTierDowngradeResponse, error) {
	f.requests = append(f.requests, proto.Clone(r).(*purserpb.AdminRecoverTierDowngradeRequest))
	held := &purserpb.HeldTierDowngrade{TenantId: r.GetTenantId(), TargetTierId: "target-tier", TargetTierName: "developer",
		EffectiveAt: timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)), InvoiceId: "invoice-1", InvoiceStatus: "manual_review"}
	if !r.GetConfirmed() {
		return &purserpb.AdminRecoverTierDowngradeResponse{Status: "invoice_held", Downgrade: held, Detail: "Invoice invoice-1 is in manual review"}, nil
	}
	return &purserpb.AdminRecoverTierDowngradeResponse{Status: "applied", Downgrade: held, Detail: "The tenant is on developer"}, nil
}

func downgradeRecoveryCommand(t *testing.T, stdin string) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var out, errOut bytes.Buffer
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	return cmd, &out, &errOut
}

func TestTierDowngradeRecoveryValidatesBeforeRPC(t *testing.T) {
	for _, r := range []*purserpb.AdminRecoverTierDowngradeRequest{
		{TenantId: "bad", Reason: "pricing fixed"},
		{TenantId: "00000000-0000-4000-8000-000000000001"},
		{TenantId: "00000000-0000-4000-8000-000000000001", Reason: "   "},
	} {
		fake := &tierDowngradeRecoveryFake{}
		cmd, _, _ := downgradeRecoveryCommand(t, "")
		if err := runTierDowngradeRecovery(cmd, fake, "jwt", r, true, false); err == nil || len(fake.requests) != 0 {
			t.Fatalf("invalid request %v called the RPC %d times, err=%v", r, len(fake.requests), err)
		}
	}
}

// Confirmation carries the previewed target, so a downgrade re-staged after the
// preview is refused by Purser instead of released unseen.
func TestTierDowngradeRecoveryConfirmsThePreviewedTarget(t *testing.T) {
	fake := &tierDowngradeRecoveryFake{}
	cmd, out, errOut := downgradeRecoveryCommand(t, "yes\n")
	req := &purserpb.AdminRecoverTierDowngradeRequest{TenantId: "00000000-0000-4000-8000-000000000001", Reason: "pricing fixed", TargetTierId: "stale"}
	if err := runTierDowngradeRecovery(cmd, fake, "jwt", req, false, false); err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("RPC calls = %d, want preview and confirmation", len(fake.requests))
	}
	if preview := fake.requests[0]; preview.GetConfirmed() || preview.GetTargetTierId() != "" {
		t.Fatalf("preview request = %v, want unconfirmed without a target", preview)
	}
	if confirm := fake.requests[1]; !confirm.GetConfirmed() || confirm.GetTargetTierId() != "target-tier" || confirm.GetReason() != "pricing fixed" {
		t.Fatalf("confirmation request = %v, want the previewed target and the reason", confirm)
	}
	for _, want := range []string{"developer", "invoice-1", "manual_review", "2026-11-01T00:00:00Z", ": invoice_held.", "your identity and reason are recorded"} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("preview output misses %q: %s", want, errOut.String())
		}
	}
	if !strings.Contains(out.String(), "Downgrade recovery: applied") {
		t.Fatalf("result output = %s", out.String())
	}
}

func TestTierDowngradeRecoveryDeclinedSendsNoConfirmation(t *testing.T) {
	fake := &tierDowngradeRecoveryFake{}
	cmd, _, _ := downgradeRecoveryCommand(t, "no\n")
	req := &purserpb.AdminRecoverTierDowngradeRequest{TenantId: "00000000-0000-4000-8000-000000000001", Reason: "pricing fixed"}
	if err := runTierDowngradeRecovery(cmd, fake, "jwt", req, false, false); err == nil {
		t.Fatal("declined confirmation succeeded")
	}
	if len(fake.requests) != 1 || fake.requests[0].GetConfirmed() {
		t.Fatalf("requests after decline = %v, want only the preview", fake.requests)
	}
}

func TestRecoveryListNamesHeldDowngrades(t *testing.T) {
	client := &recoveryValidationClient{response: &purserpb.AdminListCheckoutRecoveryResponse{HeldDowngrades: []*purserpb.HeldTierDowngrade{{
		TenantId: "00000000-0000-4000-8000-000000000007", TargetTierName: "developer",
		EffectiveAt: timestamppb.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)), InvoiceId: "invoice-7", InvoiceStatus: "manual_review",
	}, {TenantId: "00000000-0000-4000-8000-000000000008", TargetTierName: "free"}}, HeldDowngradesHasMore: true}}
	var out bytes.Buffer
	if err := runCheckoutRecoveryList(t.Context(), &out, client, "jwt", &purserpb.AdminListCheckoutRecoveryRequest{Limit: 10}, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"No provider recovery work found.", "Held downgrades (PurserTierDowngradeHeld):",
		"00000000-0000-4000-8000-000000000007  developer  2026-11-01T00:00:00Z  invoice-7  manual_review",
		"00000000-0000-4000-8000-000000000008  free  -  -  -", "More held downgrades exist than --limit lists",
		"admin billing recovery downgrade --tenant-id <tenant> --reason <reason>"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
}

// Held downgrades are listed on the first page only, so later pages do not
// claim there are none.
func TestRecoveryListLaterPagesOmitHeldDowngrades(t *testing.T) {
	client := &recoveryValidationClient{response: &purserpb.AdminListCheckoutRecoveryResponse{}}
	var out bytes.Buffer
	if err := runCheckoutRecoveryList(t.Context(), &out, client, "jwt", &purserpb.AdminListCheckoutRecoveryRequest{Limit: 10, Offset: 10}, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "held downgrade") || strings.Contains(out.String(), "Held downgrades") {
		t.Fatalf("later page reported held downgrades: %s", out.String())
	}
	out.Reset()
	if err := runCheckoutRecoveryList(t.Context(), &out, client, "jwt", &purserpb.AdminListCheckoutRecoveryRequest{Limit: 10}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No held downgrades found.") {
		t.Fatalf("first page misses the empty held-downgrade section: %s", out.String())
	}
}

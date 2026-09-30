package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

type fakeAdminBillingClient struct {
	tiersResp *purserpb.GetBillingTiersResponse
	tiersErr  error

	initResp *purserpb.InitializePostpaidAccountResponse
	initErr  error

	promoteResp *purserpb.PromoteToPaidResponse
	promoteErr  error

	pricingReq  *purserpb.SetClusterPricingRequest
	pricingResp *purserpb.ClusterPricing
	pricingErr  error

	adjustCalls []adjustBalanceCall
	adjustResp  *purserpb.BalanceTransaction

	assignCalls []assignTierCall
	assignResp  *purserpb.AdminAssignTierResponse

	grantReqs   []*purserpb.AdminSetBillingGrantRequest
	grantResp   *purserpb.AdminBillingGrantResponse
	revokeCalls []string
	paymentReqs []*purserpb.AdminRecordInvoicePaymentRequest
	paymentResp *purserpb.AdminRecordInvoicePaymentResponse
}

func (f *fakeAdminBillingClient) AdminSetBillingGrant(_ context.Context, req *purserpb.AdminSetBillingGrantRequest) (*purserpb.AdminBillingGrantResponse, error) {
	f.grantReqs = append(f.grantReqs, req)
	return f.grantResp, nil
}

func (f *fakeAdminBillingClient) AdminGetBillingGrant(context.Context, string) (*purserpb.AdminBillingGrantResponse, error) {
	return f.grantResp, nil
}

func (f *fakeAdminBillingClient) AdminRevokeBillingGrant(_ context.Context, tenantID, reason string) (*purserpb.AdminBillingGrantResponse, error) {
	f.revokeCalls = append(f.revokeCalls, tenantID+"|"+reason)
	return f.grantResp, nil
}

func (f *fakeAdminBillingClient) AdminRecordInvoicePayment(_ context.Context, req *purserpb.AdminRecordInvoicePaymentRequest) (*purserpb.AdminRecordInvoicePaymentResponse, error) {
	f.paymentReqs = append(f.paymentReqs, req)
	return f.paymentResp, nil
}

func (f *fakeAdminBillingClient) AdjustBalance(_ context.Context, tenantID string, amountCents int64, description string, _, _ *string) (*purserpb.BalanceTransaction, error) {
	f.adjustCalls = append(f.adjustCalls, adjustBalanceCall{tenantID: tenantID, amountCents: amountCents, description: description})
	return f.adjustResp, nil
}

func (f *fakeAdminBillingClient) AdminAssignTier(_ context.Context, req *purserpb.AdminAssignTierRequest) (*purserpb.AdminAssignTierResponse, error) {
	f.assignCalls = append(f.assignCalls, assignTierCall{req: req})
	return f.assignResp, nil
}

func (f *fakeAdminBillingClient) GetBillingTiers(_ context.Context, _ bool, _ *commonpb.CursorPaginationRequest) (*purserpb.GetBillingTiersResponse, error) {
	return f.tiersResp, f.tiersErr
}

func (f *fakeAdminBillingClient) InitializePostpaidAccount(_ context.Context, _ string) (*purserpb.InitializePostpaidAccountResponse, error) {
	return f.initResp, f.initErr
}

func (f *fakeAdminBillingClient) PromoteToPaid(_ context.Context, _, _ string) (*purserpb.PromoteToPaidResponse, error) {
	return f.promoteResp, f.promoteErr
}

func (f *fakeAdminBillingClient) SetClusterPricing(_ context.Context, req *purserpb.SetClusterPricingRequest) (*purserpb.ClusterPricing, error) {
	f.pricingReq = req
	if f.pricingErr != nil {
		return nil, f.pricingErr
	}
	return f.pricingResp, nil
}

func TestRunBillingTiers(t *testing.T) {
	fake := &fakeAdminBillingClient{tiersResp: &purserpb.GetBillingTiersResponse{
		Tiers: []*purserpb.BillingTier{
			{TierName: "free", TierLevel: 0, DisplayName: "Free", Currency: "$", BasePrice: 0, IsDefaultPrepaid: true},
			{TierName: "pro", TierLevel: 3, DisplayName: "Pro", Currency: "$", BasePrice: 49.0, IsDefaultPostpaid: true},
		},
	}}
	var buf bytes.Buffer
	if err := runBillingTiers(context.Background(), &buf, fake, "", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Billing tiers (2)", "free", "[default-prepaid]", "pro", "$49.00/mo", "[default-postpaid]"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	var jbuf bytes.Buffer
	if err := runBillingTiers(context.Background(), &jbuf, fake, "", true); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !json.Valid(jbuf.Bytes()) {
		t.Errorf("not valid JSON: %s", jbuf.String())
	}

	errFake := &fakeAdminBillingClient{tiersErr: errors.New("rpc down")}
	if err := runBillingTiers(context.Background(), &bytes.Buffer{}, errFake, "", false); err == nil {
		t.Fatal("expected tiers error to propagate")
	}
}

func TestRunBillingInitPostpaid(t *testing.T) {
	fake := &fakeAdminBillingClient{initResp: &purserpb.InitializePostpaidAccountResponse{
		SubscriptionId: "sub-1", TierLevel: 2, PrimaryClusterId: "c-1", EligibleClusterIds: []string{"c-1", "c-2"},
	}}
	var buf bytes.Buffer
	if err := runBillingInitPostpaid(context.Background(), &buf, fake, "", "tenant-1", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Initialized postpaid account", "Subscription: sub-1", "Tier level:   2", "Primary cluster: c-1", "Eligible clusters: c-1, c-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	var jbuf bytes.Buffer
	if err := runBillingInitPostpaid(context.Background(), &jbuf, fake, "", "tenant-1", true); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !json.Valid(jbuf.Bytes()) {
		t.Errorf("not valid JSON: %s", jbuf.String())
	}

	errFake := &fakeAdminBillingClient{initErr: errors.New("boom")}
	if err := runBillingInitPostpaid(context.Background(), &bytes.Buffer{}, errFake, "", "tenant-1", false); err == nil {
		t.Fatal("expected init error to propagate")
	}
}

func TestRunBillingPromote(t *testing.T) {
	// No eligible clusters → that optional line is omitted.
	fake := &fakeAdminBillingClient{promoteResp: &purserpb.PromoteToPaidResponse{
		SubscriptionId: "sub-9", NewBillingModel: "postpaid", CreditBalanceCents: 1500, TierLevel: 3, PrimaryClusterId: "c-1",
	}}
	var buf bytes.Buffer
	if err := runBillingPromote(context.Background(), &buf, fake, "", "tenant-1", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"Promoted to postpaid billing", "Subscription: sub-9", "Billing model: postpaid", "Credit balance: 1500 cents", "Tier level: 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Eligible clusters:") {
		t.Errorf("eligible-clusters line should be omitted when empty:\n%s", out)
	}

	// With eligible clusters the optional line is printed.
	withClusters := &fakeAdminBillingClient{promoteResp: &purserpb.PromoteToPaidResponse{
		SubscriptionId: "sub-9", NewBillingModel: "postpaid", EligibleClusterIds: []string{"c-1", "c-2"},
	}}
	var cbuf bytes.Buffer
	if err := runBillingPromote(context.Background(), &cbuf, withClusters, "", "tenant-1", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(cbuf.String(), "Eligible clusters: c-1, c-2") {
		t.Errorf("expected eligible-clusters line:\n%s", cbuf.String())
	}

	var jbuf bytes.Buffer
	if err := runBillingPromote(context.Background(), &jbuf, fake, "", "tenant-1", true); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !json.Valid(jbuf.Bytes()) {
		t.Errorf("not valid JSON: %s", jbuf.String())
	}

	errFake := &fakeAdminBillingClient{promoteErr: errors.New("boom")}
	if err := runBillingPromote(context.Background(), &bytes.Buffer{}, errFake, "", "tenant-1", false); err == nil {
		t.Fatal("expected promote error to propagate")
	}
}

func TestRunBillingSetClusterPricing(t *testing.T) {
	fake := &fakeAdminBillingClient{pricingResp: &purserpb.ClusterPricing{PricingModel: "metered"}}
	req := &purserpb.SetClusterPricingRequest{ClusterId: "c-1", PricingModel: "metered"}
	var buf bytes.Buffer
	if err := runBillingSetClusterPricing(context.Background(), &buf, fake, "", "c-1", req, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.pricingReq != req {
		t.Error("request not forwarded unchanged")
	}
	if !strings.Contains(buf.String(), "Set cluster pricing for c-1 (model=metered)") {
		t.Errorf("missing success line: %q", buf.String())
	}

	var jbuf bytes.Buffer
	if err := runBillingSetClusterPricing(context.Background(), &jbuf, fake, "", "c-1", req, true); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !json.Valid(jbuf.Bytes()) {
		t.Errorf("not valid JSON: %s", jbuf.String())
	}

	errFake := &fakeAdminBillingClient{pricingErr: errors.New("boom")}
	if err := runBillingSetClusterPricing(context.Background(), &bytes.Buffer{}, errFake, "", "c-1", req, false); err == nil {
		t.Fatal("expected set-pricing error to propagate")
	}
}

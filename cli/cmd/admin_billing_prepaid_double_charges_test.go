package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakePrepaidDoubleChargeClient struct {
	reqs []*purserpb.AdminListPrepaidDoubleChargesRequest
	resp *purserpb.AdminListPrepaidDoubleChargesResponse
}

func (f *fakePrepaidDoubleChargeClient) AdminListPrepaidDoubleCharges(_ context.Context, req *purserpb.AdminListPrepaidDoubleChargesRequest) (*purserpb.AdminListPrepaidDoubleChargesResponse, error) {
	f.reqs = append(f.reqs, req)
	return f.resp, nil
}

func TestRunBillingPrepaidDoubleChargesPrintsWhatToRefund(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	fake := &fakePrepaidDoubleChargeClient{resp: &purserpb.AdminListPrepaidDoubleChargesResponse{
		TotalDoubleChargedCents: 200,
		Truncated:               true,
		Charges: []*purserpb.PrepaidDoubleCharge{{
			TenantId: "11111111-1111-4111-8111-111111111111", InvoiceId: "invoice-1", InvoiceNumber: "INV-0000000007",
			Status: "paid", PeriodStart: timestamppb.New(start), PeriodEnd: timestamppb.New(start.AddDate(0, 1, 0)),
			InvoiceUsageCents: 200, PrepaidUsagePaidCents: 200, InvoiceCreditCents: 200, DoubleChargedCents: 200,
		}},
	}}
	var out bytes.Buffer
	if err := runBillingPrepaidDoubleCharges(context.Background(), &out, fake, "", "11111111-1111-4111-8111-111111111111", 10, false); err != nil {
		t.Fatalf("runBillingPrepaidDoubleCharges: %v", err)
	}
	if len(fake.reqs) != 1 || fake.reqs[0].GetTenantId() != "11111111-1111-4111-8111-111111111111" || fake.reqs[0].GetLimit() != 10 {
		t.Fatalf("requests = %+v", fake.reqs)
	}
	text := out.String()
	for _, want := range []string{"EUR 2.00 to refund", "invoice=INV-0000000007", "period=2026-08-01..2026-09-01", "refund=EUR 2.00", "invoice_credit=EUR 2.00", "raise it"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}

	if err := runBillingPrepaidDoubleCharges(context.Background(), &out, fake, "", "not-a-uuid", 0, false); err == nil {
		t.Fatal("an invalid --tenant-id was sent")
	}
	fake.resp = &purserpb.AdminListPrepaidDoubleChargesResponse{}
	out.Reset()
	if err := runBillingPrepaidDoubleCharges(context.Background(), &out, fake, "", "", 0, false); err != nil || !strings.Contains(out.String(), "No invoice charged") {
		t.Fatalf("empty report = %q, %v", out.String(), err)
	}
}

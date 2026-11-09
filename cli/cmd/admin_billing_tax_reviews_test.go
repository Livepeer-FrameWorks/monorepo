package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

type fakeTaxReviewClient struct {
	calls  []*purserpb.AdminListBillingTaxReviewsRequest
	repeat bool
}

func (f *fakeTaxReviewClient) AdminListBillingTaxReviews(_ context.Context, r *purserpb.AdminListBillingTaxReviewsRequest) (*purserpb.AdminListBillingTaxReviewsResponse, error) {
	f.calls = append(f.calls, r)
	if len(f.calls) == 1 || f.repeat {
		return &purserpb.AdminListBillingTaxReviewsResponse{RecordsJson: [][]byte{[]byte(`{"id":"review","evidence_sha256":"retained"}`)}, NextCursor: "next"}, nil
	}
	return &purserpb.AdminListBillingTaxReviewsResponse{}, nil
}

func TestFinancialArchiveReportsOpenTaxReviews(t *testing.T) {
	row := map[string]json.RawMessage{"id": json.RawMessage(`"review"`), "status": json.RawMessage(`"open"`)}
	if got := auditTaxException("billing_tax_reviews", "tenant", row); got == nil || got.Code != "OPEN_BILLING_TAX_REVIEW" {
		t.Fatalf("open review missing from exceptions: %+v", got)
	}
	row["status"] = json.RawMessage(`"resolved"`)
	if got := auditTaxException("billing_tax_reviews", "tenant", row); got != nil {
		t.Fatalf("acknowledged review incorrectly remains an open exception: %+v", got)
	}
}
func (*fakeTaxReviewClient) AdminAcknowledgeBillingTaxReview(context.Context, *purserpb.AdminAcknowledgeBillingTaxReviewRequest) (*purserpb.AdminAcknowledgeBillingTaxReviewResponse, error) {
	panic("list must never acknowledge")
}
func TestBillingTaxReviewListingPreservesScopeAndEvidence(t *testing.T) {
	c := &fakeTaxReviewClient{}
	var out bytes.Buffer
	if err := listBillingTaxReviews(t.Context(), &out, c, "tenant", "stripe", "provider_tax_document_review", "all"); err != nil {
		t.Fatal(err)
	}
	if len(c.calls) != 2 || c.calls[1].GetAfterId() != "next" || !strings.Contains(out.String(), "evidence_sha256") {
		t.Fatalf("lost evidence/page: %s %+v", out.String(), c.calls)
	}
	for _, r := range c.calls {
		if r.GetTenantId() != "tenant" || r.GetProvider() != "stripe" || r.GetCategory() != "provider_tax_document_review" || r.GetStatus() != "all" {
			t.Fatalf("scope changed: %+v", r)
		}
	}
	if err := listBillingTaxReviews(t.Context(), &out, &fakeTaxReviewClient{repeat: true}, "tenant", "stripe", "", "open"); err == nil {
		t.Fatal("repeated cursor must not loop forever")
	}
}

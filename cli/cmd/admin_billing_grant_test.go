package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func TestParseGrantUntil(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	until, err := parseGrantUntil("2026-12-31", now)
	if err != nil {
		t.Fatalf("date: %v", err)
	}
	if got := until.AsTime(); !got.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("a date runs through that day: got %s", got)
	}
	if _, err := parseGrantUntil("2026-09-29", now); err == nil || !strings.Contains(err.Error(), "not in the future") {
		t.Fatalf("a past date must be refused: %v", err)
	}
	if _, err := parseGrantUntil("next week", now); err == nil {
		t.Fatal("an unparseable --until must be refused")
	}
	if until, err := parseGrantUntil("", now); err != nil || until != nil {
		t.Fatalf("no --until means until revoked: %v %v", until, err)
	}
}

func TestBillingGrantRequestValidation(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name    string
		req     billingGrantRequest
		wantErr string
	}{
		{"reason required", billingGrantRequest{BaseFee: "0"}, "--reason"},
		{"changes nothing", billingGrantRequest{Reason: "x"}, "changes nothing"},
		{"bad collection", billingGrantRequest{Reason: "x", Collection: "wire"}, "--collection"},
		{"bad base fee", billingGrantRequest{Reason: "x", BaseFee: "1.234"}, "--base-fee"},
		{"negative base fee", billingGrantRequest{Reason: "x", BaseFee: "-1"}, "--base-fee"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.req.toProto(billingOperatorTenant, now); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
	req, err := billingGrantRequest{Tier: "supporter", BaseFee: "0", Collection: "Invoice", Reason: "house account"}.toProto(billingOperatorTenant, now)
	if err != nil {
		t.Fatalf("valid grant: %v", err)
	}
	if req.GetBasePrice() != "0" || req.BasePrice == nil || req.GetCollection() != "invoice" || req.GetTierName() != "supporter" || req.GetWaiveUsage() {
		t.Fatalf("unexpected request: %+v", req)
	}
}

func TestRunBillingGrantSetSendsTheArrangement(t *testing.T) {
	base := "0.00"
	fake := &fakeAdminBillingClient{grantResp: &purserpb.AdminBillingGrantResponse{
		Grant: &purserpb.OperatorBillingGrant{BasePrice: &base, Collection: "provider", Active: true, Reason: "beta supporter"},
	}}
	var out bytes.Buffer
	err := runBillingGrantSet(context.Background(), &out, fake, nil, "", billingGrantRequest{
		Target: billingTenantTarget{TenantID: billingOperatorTenant}, Tier: "supporter", BaseFee: "0", Reason: "beta supporter",
	}, false)
	if err != nil {
		t.Fatalf("grant set: %v", err)
	}
	if len(fake.grantReqs) != 1 || fake.grantReqs[0].GetTenantId() != billingOperatorTenant || fake.grantReqs[0].GetBasePrice() != "0" {
		t.Fatalf("unexpected grant requests: %+v", fake.grantReqs)
	}
	// The tenant still owes usage and has no card: the output must say so.
	if !strings.Contains(out.String(), "not ready") {
		t.Fatalf("output does not report that usage cannot be collected:\n%s", out.String())
	}
}

func TestRunBillingRecordPaymentRequiresReference(t *testing.T) {
	fake := &fakeAdminBillingClient{}
	err := runBillingRecordPayment(context.Background(), &bytes.Buffer{}, fake, nil, "", billingRecordPaymentRequest{
		Target: billingTenantTarget{TenantID: billingOperatorTenant}, InvoiceID: billingOperatorTenant, Reason: "paid",
	}, false)
	if err == nil || !strings.Contains(err.Error(), "--reference") {
		t.Fatalf("err = %v, want --reference required", err)
	}
	if len(fake.paymentReqs) != 0 {
		t.Fatal("no payment may be recorded without a reference")
	}
}

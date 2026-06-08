package handlers

import (
	"context"
	"net/http"
	"testing"

	pkgx402 "github.com/Livepeer-FrameWorks/monorepo/pkg/x402"
)

// mapSettlementErrorToHTTPDecision turns an x402 settlement error code into the
// HTTP 402 body the viewer endpoint returns. Every code resolves to a specific
// response shape (payment_failed / billing_details_required / insufficient_balance),
// and any unrecognised code falls through to the generic payment_failed body —
// never an empty or 200 decision. All arms always return 402.
func TestMapSettlementErrorToHTTPDecision(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		code     string
		wantBody string // value of the "error" field in the response body
	}{
		{pkgx402.ErrInvalidPayment, "payment_failed"},
		{pkgx402.ErrBillingDetailsRequired, "billing_details_required"},
		{pkgx402.ErrAuthOnly, "insufficient_balance"},
		{pkgx402.ErrVerificationFailed, "payment_failed"},
		{pkgx402.ErrSettlementFailed, "payment_failed"},
		{"some_unknown_code", "payment_failed"}, // default arm
	}
	for _, c := range cases {
		dec := mapSettlementErrorToHTTPDecision(ctx, "tenant-1", "stream-1", &pkgx402.SettlementError{Code: c.code, Message: "msg"})
		if dec == nil {
			t.Fatalf("code %q: nil decision", c.code)
		}
		if dec.Status != http.StatusPaymentRequired {
			t.Errorf("code %q: status = %d, want 402", c.code, dec.Status)
		}
		if got := dec.Body["error"]; got != c.wantBody {
			t.Errorf("code %q: body error = %v, want %q", c.code, got, c.wantBody)
		}
	}
}

// The response builders default a blank message to a sensible fallback and stamp
// a stable machine-readable code clients switch on.
func TestX402ResponseBuilders(t *testing.T) {
	t.Run("payment failed defaults blank message", func(t *testing.T) {
		got := buildPaymentFailedResponse("")
		if got["code"] != "PAYMENT_FAILED" || got["message"] == "" {
			t.Fatalf("blank message not defaulted: %v", got)
		}
		if buildPaymentFailedResponse("nope")["message"] != "nope" {
			t.Fatal("explicit message should be preserved")
		}
	})
	t.Run("billing details defaults blank message", func(t *testing.T) {
		got := buildBillingDetailsRequiredResponse("   ")
		if got["code"] != "BILLING_DETAILS_REQUIRED" || got["message"] == "" {
			t.Fatalf("blank message not defaulted: %v", got)
		}
		if buildBillingDetailsRequiredResponse("need card")["message"] != "need card" {
			t.Fatal("explicit message should be preserved")
		}
	})
}

// buildInsufficientBalanceResponse returns the base body (no payment block) when
// no purser client is wired — it must not panic dereferencing a nil client.
func TestBuildInsufficientBalanceResponse_NoPurserClient(t *testing.T) {
	prev := purserClient
	purserClient = nil
	t.Cleanup(func() { purserClient = prev })

	got := buildInsufficientBalanceResponse(context.Background(), "tenant-1", "viewer://stream-1", "broke")
	if got["code"] != "INSUFFICIENT_BALANCE" || got["error"] != "insufficient_balance" {
		t.Fatalf("unexpected base body: %v", got)
	}
	if _, hasPayment := got["payment"]; hasPayment {
		t.Fatal("no purser client => no payment requirements block expected")
	}
	if got["topup_url"] != "/account/billing" {
		t.Fatalf("topup_url = %v, want /account/billing", got["topup_url"])
	}
}

package tools

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/mcp/preflight"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	x402pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/x402"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func v2PaymentSignature() string {
	document := []byte(`{"x402Version":2,"accepted":{"scheme":"exact","network":"eip155:8453","asset":"0xAsset","amount":"5000000","payTo":"0xToAddress","maxTimeoutSeconds":60,"extra":{"frameworks":{"quoteId":"quote-123"}}},"payload":{"signature":"0xabc123","authorization":{"from":"0xFromAddress","to":"0xToAddress","value":"5000000","validAfter":"0","validBefore":"9999999999","nonce":"0x01"}},"resource":{"url":"https://api.example.com/graphql"}}`)
	return base64.StdEncoding.EncodeToString(document)
}

func toolsCtx(tenant string) context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyTenantID, tenant)
}

func purserTools(p *clientstest.FakePurser) (*clientstest.FakePurser, *preflight.Checker) {
	sc := clientstest.Clients(clientstest.WithPurser(p))
	return p, preflight.NewChecker(sc, clientstest.DiscardLogger())
}

// ---- topup_balance ----

// Missing auth surfaces as a Go (jsonrpc) error, NOT a tool-level IsError result.
func TestHandleTopupBalance_AuthRequired(t *testing.T) {
	p, checker := purserTools(&clientstest.FakePurser{})
	sc := clientstest.Clients(clientstest.WithPurser(p))
	_, _, err := handleTopupBalance(context.Background(), TopupBalanceInput{AmountCents: 100}, sc, checker, clientstest.DiscardLogger())
	if err == nil {
		t.Fatal("missing tenant must return a jsonrpc auth error")
	}
	if p.Calls != 0 {
		t.Fatalf("auth gate must short-circuit before Purser, got %d calls", p.Calls)
	}
}

// Input validation rejects before any backend call: these are tool errors (IsError).
func TestHandleTopupBalance_Validation(t *testing.T) {
	cases := []struct {
		name string
		in   TopupBalanceInput
		want string
	}{
		{"non_positive", TopupBalanceInput{AmountCents: 0}, "amount must be positive"},
		{"lpt_rejected", TopupBalanceInput{AmountCents: 100, Asset: "LPT"}, "LPT"},
		{"unknown_asset", TopupBalanceInput{AmountCents: 100, Asset: "DOGE"}, "Invalid asset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &clientstest.FakePurser{} // CreateCryptoTopup unstubbed → must not be reached
			sc := clientstest.Clients(clientstest.WithPurser(p))
			checker := preflight.NewChecker(sc, clientstest.DiscardLogger())
			res, _, err := handleTopupBalance(toolsCtx("t1"), tc.in, sc, checker, clientstest.DiscardLogger())
			if err != nil {
				t.Fatalf("validation failure should be a tool error, not a Go error: %v", err)
			}
			if res == nil || !res.IsError {
				t.Fatalf("expected IsError result for %s", tc.name)
			}
			if !strings.Contains(extractToolText(res), tc.want) {
				t.Errorf("message %q should mention %q", extractToolText(res), tc.want)
			}
			if p.Calls != 0 {
				t.Fatalf("validation must not reach Purser, got %d calls", p.Calls)
			}
		})
	}
}

// Happy path: blank asset defaults to USDC and the proto enum + amount are forwarded.
func TestHandleTopupBalance_DefaultsToUSDC(t *testing.T) {
	var gotReq *purserpb.CreateCryptoTopupRequest
	p := &clientstest.FakePurser{
		CreateCryptoTopupFn: func(_ context.Context, req *purserpb.CreateCryptoTopupRequest) (*purserpb.CreateCryptoTopupResponse, error) {
			gotReq = req
			return &purserpb.CreateCryptoTopupResponse{
				TopupId: "tp1", DepositAddress: "0xabc", AssetSymbol: "USDC",
				ExpectedAmountCents: req.ExpectedAmountCents, ExpiresAt: timestamppb.New(time.Unix(1000, 0)),
			}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithPurser(p))
	checker := preflight.NewChecker(sc, clientstest.DiscardLogger())
	res, out, err := handleTopupBalance(toolsCtx("t1"), TopupBalanceInput{AmountCents: 2500}, sc, checker, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("expected success, got err=%v isErr=%v (%s)", err, res.IsError, extractToolText(res))
	}
	if gotReq.Asset != purserpb.CryptoAsset_CRYPTO_ASSET_USDC {
		t.Errorf("blank asset should default to USDC enum, got %v", gotReq.Asset)
	}
	if gotReq.ExpectedAmountCents != 2500 || gotReq.TenantId != "t1" {
		t.Errorf("amount/tenant not forwarded: %+v", gotReq)
	}
	if r, ok := out.(TopupResult); !ok || r.TopupID != "tp1" {
		t.Errorf("unexpected result: %T %+v", out, out)
	}
}

func TestHandleTopupBalance_ETHEnum(t *testing.T) {
	var gotReq *purserpb.CreateCryptoTopupRequest
	p := &clientstest.FakePurser{
		CreateCryptoTopupFn: func(_ context.Context, req *purserpb.CreateCryptoTopupRequest) (*purserpb.CreateCryptoTopupResponse, error) {
			gotReq = req
			return &purserpb.CreateCryptoTopupResponse{TopupId: "tp2", ExpiresAt: timestamppb.New(time.Unix(1000, 0))}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithPurser(p))
	checker := preflight.NewChecker(sc, clientstest.DiscardLogger())
	if _, _, err := handleTopupBalance(toolsCtx("t1"), TopupBalanceInput{AmountCents: 100, Asset: "eth"}, sc, checker, clientstest.DiscardLogger()); err != nil {
		t.Fatal(err)
	}
	if gotReq.Asset != purserpb.CryptoAsset_CRYPTO_ASSET_ETH {
		t.Errorf("asset 'eth' should map to ETH enum (case-insensitive), got %v", gotReq.Asset)
	}
}

func TestHandleStartPostpaidSetupStripe(t *testing.T) {
	var gotTenant, gotTier string
	p := &clientstest.FakePurser{
		GetBillingDetailsFn: func(context.Context, string) (*purserpb.BillingDetails, error) {
			return &purserpb.BillingDetails{IsComplete: true}, nil
		},
		GetBillingStatusFn: func(context.Context, string) (*purserpb.BillingStatusResponse, error) {
			return &purserpb.BillingStatusResponse{SetupProviders: []string{"stripe"}}, nil
		},
		CreateStripeCheckoutSessionFn: func(_ context.Context, tenantID, tierID, period, successURL, cancelURL string) (*purserpb.CreateStripeCheckoutResponse, error) {
			gotTenant, gotTier = tenantID, tierID
			if period != "monthly" || successURL == "" || cancelURL == "" {
				t.Fatalf("unexpected Stripe args: %q %q %q", period, successURL, cancelURL)
			}
			return &purserpb.CreateStripeCheckoutResponse{SessionId: "cs_1", CheckoutUrl: "https://checkout.stripe.test/cs_1"}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithPurser(p))
	checker := preflight.NewChecker(sc, clientstest.DiscardLogger())
	res, out, err := handleStartPostpaidSetup(toolsCtx("tenant-1"), StartPostpaidSetupInput{
		Provider: "stripe", TierID: "tier-1", SuccessURL: "https://app.test/ok", CancelURL: "https://app.test/no",
	}, sc, checker, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("expected success, got err=%v result=%s", err, extractToolText(res))
	}
	result, ok := out.(PostpaidSetupResult)
	if !ok || result.SetupID != "cs_1" || result.Status != "action_required" {
		t.Fatalf("result = %T %+v", out, out)
	}
	if gotTenant != "tenant-1" || gotTier != "tier-1" {
		t.Fatalf("tenant/tier = %s/%s", gotTenant, gotTier)
	}
}

func TestHandleCompleteMolliePostpaidSetup(t *testing.T) {
	p := &clientstest.FakePurser{
		GetBillingDetailsFn: func(context.Context, string) (*purserpb.BillingDetails, error) {
			return &purserpb.BillingDetails{IsComplete: true}, nil
		},
		CreateMollieSubscriptionFn: func(_ context.Context, tenantID, tierID, mandateID, description string) (*purserpb.CreateMollieSubscriptionResponse, error) {
			if tenantID != "tenant-1" || tierID != "tier-1" || mandateID != "" || description == "" {
				t.Fatalf("unexpected Mollie args: %s %s %s %s", tenantID, tierID, mandateID, description)
			}
			return &purserpb.CreateMollieSubscriptionResponse{SubscriptionId: "sub_1", Status: "active"}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithPurser(p))
	checker := preflight.NewChecker(sc, clientstest.DiscardLogger())
	res, out, err := handleCompleteMolliePostpaidSetup(toolsCtx("tenant-1"), CompleteMolliePostpaidSetupInput{TierID: "tier-1"}, sc, checker, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("expected success, got err=%v result=%s", err, extractToolText(res))
	}
	result, ok := out.(PostpaidSetupResult)
	if !ok || result.SetupID != "sub_1" || result.Status != "confirmed" {
		t.Fatalf("result = %T %+v", out, out)
	}
}

// ---- check_topup ----

func TestHandleCheckTopup_StatusMessages(t *testing.T) {
	cases := []struct {
		status        string
		wantConfirmed bool
		wantInMsg     string
	}{
		{"completed", true, "confirmed"},
		{"confirming", false, "confirmations"},
		{"pending", false, "not yet received"},
		{"expired", false, "expired"},
		{"weird", false, "weird"},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			sc := clientstest.Clients(clientstest.WithPurser(&clientstest.FakePurser{
				GetCryptoTopupFn: func(_ context.Context, id string) (*purserpb.CryptoTopup, error) {
					return &purserpb.CryptoTopup{Id: id, Status: tc.status, CreditedAmountCents: 500, TxHash: "0xtx"}, nil
				},
			}))
			res, out, err := handleCheckTopup(toolsCtx("t1"), CheckTopupInput{TopupID: "tp1"}, sc, clientstest.DiscardLogger())
			if err != nil || res.IsError {
				t.Fatalf("status %s: err=%v isErr=%v", tc.status, err, res.IsError)
			}
			r, ok := out.(CheckTopupResult)
			if !ok {
				t.Fatalf("unexpected result type %T", out)
			}
			if r.Confirmed != tc.wantConfirmed {
				t.Errorf("status %s: Confirmed=%v want %v", tc.status, r.Confirmed, tc.wantConfirmed)
			}
			if !strings.Contains(strings.ToLower(r.Message), strings.ToLower(tc.wantInMsg)) {
				t.Errorf("status %s: message %q should mention %q", tc.status, r.Message, tc.wantInMsg)
			}
		})
	}
}

func TestHandleCheckTopup_Guards(t *testing.T) {
	// No auth → jsonrpc error.
	sc := clientstest.Clients(clientstest.WithPurser(&clientstest.FakePurser{}))
	if _, _, err := handleCheckTopup(context.Background(), CheckTopupInput{TopupID: "x"}, sc, clientstest.DiscardLogger()); err == nil {
		t.Fatal("missing tenant should error")
	}
	// Empty topup_id → tool error, no backend call.
	p := &clientstest.FakePurser{}
	sc = clientstest.Clients(clientstest.WithPurser(p))
	res, _, err := handleCheckTopup(toolsCtx("t1"), CheckTopupInput{TopupID: ""}, sc, clientstest.DiscardLogger())
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("empty topup_id should be a tool error, got err=%v res=%v", err, res)
	}
	if p.Calls != 0 {
		t.Fatalf("must not reach Purser without a topup_id, got %d calls", p.Calls)
	}
}

// ---- get_payment_options (tenant-bound; auth required) ----

func TestHandleGetPaymentOptions(t *testing.T) {
	sc := clientstest.Clients(clientstest.WithPurser(&clientstest.FakePurser{
		GetPaymentRequirementsFn: func(_ context.Context, tenantID, resource string) (*purserpb.PaymentRequirements, error) {
			if tenantID != "t1" {
				t.Errorf("payment options tenant = %q, want t1", tenantID)
			}
			return &purserpb.PaymentRequirements{
				X402Version: 1,
				Accepts: []*purserpb.PaymentRequirement{
					{Network: "base", Asset: "0xusdc", PayTo: "0xpay"},
				},
			}, nil
		},
	}))
	res, out, err := handleGetPaymentOptions(toolsCtx("t1"), GetPaymentOptionsInput{}, sc, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("expected success, got err=%v isErr=%v", err, res.IsError)
	}
	r, ok := out.(GetPaymentOptionsResult)
	if !ok || len(r.Options) != 1 {
		t.Fatalf("unexpected result: %T %+v", out, out)
	}
	if r.Options[0].DisplayName != "Base (Coinbase L2)" {
		t.Errorf("network display name not humanized: %q", r.Options[0].DisplayName)
	}
}

func TestHandleSubmitPayment_PreservesStableTerminalCode(t *testing.T) {
	p := &clientstest.FakePurser{
		VerifyX402PaymentFn: func(_ context.Context, tenantID string, _ *x402pb.X402PaymentPayload, _ string) (*purserpb.VerifyX402PaymentResponse, error) {
			return &purserpb.VerifyX402PaymentResponse{Valid: tenantID == "t1"}, nil
		},
		SettleX402PaymentFn: func(_ context.Context, _ string, _ *x402pb.X402PaymentPayload, _ string) (*purserpb.SettleX402PaymentResponse, error) {
			return &purserpb.SettleX402PaymentResponse{
				Success: false, Error: "authorization already used", ErrorCode: "AUTHORIZATION_USED",
			}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithPurser(p))
	res, out, err := handleSubmitPayment(toolsCtx("t1"), SubmitPaymentInput{Payment: v2PaymentSignature()}, sc, clientstest.DiscardLogger())
	if err != nil || res == nil || res.IsError {
		t.Fatalf("terminal settlement must be a machine-readable tool result: err=%v result=%v", err, res)
	}
	result, ok := out.(SubmitPaymentResult)
	if !ok {
		t.Fatalf("result type = %T, want SubmitPaymentResult", out)
	}
	if result.Success || result.SettlementStatus != "failed" || result.ErrorCode != "AUTHORIZATION_USED" {
		t.Fatalf("unexpected terminal settlement result: %+v", result)
	}
	if result.Message != "authorization already used" {
		t.Fatalf("message = %q", result.Message)
	}
}

func TestHandleSubmitPayment_ReturnsStructuredSettlementPending(t *testing.T) {
	p := &clientstest.FakePurser{
		VerifyX402PaymentFn: func(_ context.Context, _ string, _ *x402pb.X402PaymentPayload, _ string) (*purserpb.VerifyX402PaymentResponse, error) {
			return &purserpb.VerifyX402PaymentResponse{Valid: true}, nil
		},
		SettleX402PaymentFn: func(_ context.Context, _ string, _ *x402pb.X402PaymentPayload, _ string) (*purserpb.SettleX402PaymentResponse, error) {
			return &purserpb.SettleX402PaymentResponse{
				Success: false, Error: "awaiting canonical receipt", ErrorCode: "SETTLEMENT_PENDING",
				TxHash: "0xabc", Network: "eip155:8453",
			}, nil
		},
	}
	sc := clientstest.Clients(clientstest.WithPurser(p))
	res, out, err := handleSubmitPayment(toolsCtx("t1"), SubmitPaymentInput{Payment: v2PaymentSignature()}, sc, clientstest.DiscardLogger())
	if err != nil || res == nil || res.IsError {
		t.Fatalf("pending settlement must be a machine-readable tool result: err=%v result=%v", err, res)
	}
	result, ok := out.(SubmitPaymentResult)
	if !ok || result.Success || result.SettlementStatus != "pending" || result.ErrorCode != "SETTLEMENT_PENDING" ||
		result.TxHash != "0xabc" || result.Network != "eip155:8453" {
		t.Fatalf("unexpected pending settlement result: %T %+v", out, out)
	}
}

func TestHandleGetPaymentOptions_AuthRequired(t *testing.T) {
	p := &clientstest.FakePurser{}
	sc := clientstest.Clients(clientstest.WithPurser(p))
	res, _, err := handleGetPaymentOptions(context.Background(), GetPaymentOptionsInput{}, sc, clientstest.DiscardLogger())
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("missing auth should be a tool error, got err=%v res=%v", err, res)
	}
	if !strings.Contains(extractToolText(res), "authentication required") {
		t.Fatalf("unexpected auth error: %q", extractToolText(res))
	}
	if p.Calls != 0 {
		t.Fatalf("auth gate must short-circuit before Purser, got %d calls", p.Calls)
	}
}

// An in-band Purser error string is surfaced as a tool error.
func TestHandleGetPaymentOptions_InBandError(t *testing.T) {
	sc := clientstest.Clients(clientstest.WithPurser(&clientstest.FakePurser{
		GetPaymentRequirementsFn: func(context.Context, string, string) (*purserpb.PaymentRequirements, error) {
			return &purserpb.PaymentRequirements{Error: "x402 disabled"}, nil
		},
	}))
	res, _, err := handleGetPaymentOptions(toolsCtx("t1"), GetPaymentOptionsInput{}, sc, clientstest.DiscardLogger())
	if err != nil || res == nil || !res.IsError {
		t.Fatalf("in-band error should be a tool error, got err=%v res=%v", err, res)
	}
	if !strings.Contains(extractToolText(res), "x402 disabled") {
		t.Errorf("error text not surfaced: %q", extractToolText(res))
	}
}

// networkDisplayName humanizes known chains and passes through unknowns.
func TestNetworkDisplayName(t *testing.T) {
	cases := map[string]string{
		"base":         "Base (Coinbase L2)",
		"BASE-SEPOLIA": "Base Sepolia (Testnet)",
		"arbitrum":     "Arbitrum One",
		"ethereum":     "Ethereum Mainnet",
		"solana":       "solana", // unknown → passthrough
	}
	for in, want := range cases {
		if got := networkDisplayName(in); got != want {
			t.Errorf("networkDisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- update_billing_details ----

func TestHandleUpdateBillingDetails(t *testing.T) {
	// No auth.
	scNoAuth := clientstest.Clients(clientstest.WithPurser(&clientstest.FakePurser{}))
	if _, _, err := handleUpdateBillingDetails(context.Background(), UpdateBillingDetailsInput{}, scNoAuth, clientstest.DiscardLogger()); err == nil {
		t.Fatal("missing tenant should error")
	}

	// Missing required address fields → tool error before backend.
	p := &clientstest.FakePurser{}
	sc := clientstest.Clients(clientstest.WithPurser(p))
	res, _, err := handleUpdateBillingDetails(toolsCtx("t1"), UpdateBillingDetailsInput{Line1: "x"}, sc, clientstest.DiscardLogger())
	if err != nil || !res.IsError {
		t.Fatalf("incomplete address should be a tool error, got err=%v res=%v", err, res)
	}
	if p.Calls != 0 {
		t.Fatalf("must not reach Purser with incomplete address, got %d calls", p.Calls)
	}

	// Invalid country code → tool error.
	res, _, err = handleUpdateBillingDetails(toolsCtx("t1"), UpdateBillingDetailsInput{
		Line1: "1 Main", City: "Berlin", PostalCode: "10115", Country: "ZZ",
	}, sc, clientstest.DiscardLogger())
	if err != nil || !res.IsError {
		t.Fatalf("invalid country should be a tool error, got err=%v res=%v", err, res)
	}

	// Happy path: country normalized and Line2 folded into a multi-line street.
	var gotReq *purserpb.UpdateBillingDetailsRequest
	pOK := &clientstest.FakePurser{
		UpdateBillingDetailsFn: func(_ context.Context, req *purserpb.UpdateBillingDetailsRequest) (*purserpb.BillingDetails, error) {
			gotReq = req
			return &purserpb.BillingDetails{TenantId: req.TenantId, IsComplete: true}, nil
		},
	}
	scOK := clientstest.Clients(clientstest.WithPurser(pOK))
	res, _, err = handleUpdateBillingDetails(toolsCtx("tenant-9"), UpdateBillingDetailsInput{
		Name: "Example GmbH", Email: "billing@example.com", Line1: "1 Main", Line2: "Apt 4", City: "Berlin", PostalCode: "10115", Country: "de",
	}, scOK, clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("expected success, got err=%v isErr=%v (%s)", err, res.IsError, extractToolText(res))
	}
	if gotReq.Address == nil || gotReq.Address.Country != "DE" {
		t.Errorf("country not normalized to ISO upper, got %+v", gotReq.Address)
	}
	if gotReq.Address.Street != "1 Main\nApt 4" {
		t.Errorf("Line2 should fold into street, got %q", gotReq.Address.Street)
	}
}

// ---- toolErrorWithResolution helper ----

func TestToolErrorWithResolution(t *testing.T) {
	res, meta, err := toolErrorWithResolution(preflight.Blocker{
		Message:    "Top up required",
		Resolution: "Call topup_balance",
		Tool:       "topup_balance",
	})
	if err != nil || !res.IsError {
		t.Fatalf("expected IsError result, got err=%v res=%v", err, res)
	}
	text := extractToolText(res)
	if !strings.Contains(text, "Top up required") || !strings.Contains(text, "Call topup_balance") || !strings.Contains(text, "topup_balance") {
		t.Errorf("resolution text incomplete: %q", text)
	}
	if _, ok := meta.(preflight.Blocker); !ok {
		t.Errorf("blocker should be returned as structured meta, got %T", meta)
	}
}

package handlers

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	x402sdk "github.com/x402-foundation/x402/go/v2"
)

func TestCreatePaymentQuoteCoversDeficitAndBuffer(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	t.Setenv("X402_PREPAID_BUFFER_EUR_CENTS", "500")

	ecbRateCache.Lock()
	ecbRateCache.rate = 1
	ecbRateCache.fetchedAt = time.Now()
	ecbRateCache.Unlock()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(balance_cents, 0)")).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(-250)))
	mock.ExpectQuery("SELECT billing_email, billing_name, billing_company, billing_address, tax_id").
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id"}))
	mock.ExpectExec("INSERT INTO purser.x402_payment_quotes").
		WithArgs(
			sqlmock.AnyArg(), "tenant-1", "graphql://createStream", "graphql",
			"eip155:8453", "0xAsset", "0xpayto", "7500000", int64(750),
			float64(1), sqlmock.AnyArg(), "simplified", sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	h := &X402Handler{db: db, topupUSDCents: 500}
	quote, err := h.CreatePaymentQuote(context.Background(), "tenant-1", "graphql://createStream", "0xPayTo", NetworkConfig{
		Name:           "base",
		ChainID:        8453,
		USDCContract:   "0xAsset",
		USDCDomainName: "USD Coin",
	})
	if err != nil {
		t.Fatalf("CreatePaymentQuote() error = %v", err)
	}
	if quote.AmountAtomic != "7500000" || quote.CreditAmountCents != 750 {
		t.Fatalf("quote does not cover deficit + buffer: %+v", quote)
	}
	var extra map[string]interface{}
	if err := json.Unmarshal(quote.ExtraJSON, &extra); err != nil {
		t.Fatal(err)
	}
	frameworks := extra["frameworks"].(map[string]interface{})
	if frameworks["quoteId"] != quote.ID || frameworks["resourceClass"] != "graphql" {
		t.Fatalf("quote binding missing from accepted.extra: %+v", frameworks)
	}
	if extra["name"] != "USD Coin" || extra["version"] != "2" || extra["assetTransferMethod"] != "eip3009" {
		t.Fatalf("quote does not advertise the exact EIP-3009 asset contract domain: %+v", extra)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimPaymentQuoteIsCompareAndSwap(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &X402Handler{db: db}

	mock.ExpectExec("UPDATE purser.x402_payment_quotes").
		WithArgs("quote-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE purser.x402_payment_quotes").
		WithArgs("quote-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))

	claimed, err := h.claimPaymentQuote(context.Background(), "quote-1")
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	claimed, err = h.claimPaymentQuote(context.Background(), "quote-1")
	if err != nil || claimed {
		t.Fatalf("second claim = %v, %v", claimed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimPaymentQuoteOnlyReclaimsLeaseWithoutDurableIntent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &X402Handler{db: db}

	mock.ExpectExec(`(?s)status = 'claiming'.*claim_expires_at < NOW\(\).*NOT EXISTS.*x402_nonces`).
		WithArgs("quote-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	claimed, err := h.claimPaymentQuote(context.Background(), "quote-1")
	if err != nil || !claimed {
		t.Fatalf("stale safe claim = %v, %v", claimed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateV2QuoteRejectsAlteredResourceExtension(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expected := x402sdk.PaymentRequirements{
		Scheme: "exact", Network: "eip155:8453",
		Asset: Networks["base"].USDCContract, Amount: "5000000",
		PayTo: "0x1111111111111111111111111111111111111111", MaxTimeoutSeconds: 60,
		Extra: map[string]interface{}{
			"name": "USD Coin", "version": "2", "assetTransferMethod": "eip3009",
			"frameworks": map[string]interface{}{"quoteId": "quote-1", "resourceClass": "graphql"},
		},
	}
	requirements, _ := json.Marshal(expected)
	mock.ExpectQuery("SELECT id::text, tenant_id::text, resource, resource_class").
		WithArgs("quote-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "resource", "resource_class", "network", "asset", "pay_to",
			"amount_atomic", "credit_amount_cents", "eur_per_usd_rate", "requirements_json",
			"tax_document_kind", "tax_profile_snapshot", "expires_at", "status",
		}).AddRow("quote-1", "tenant-1", "graphql://createStream", "graphql", "eip155:8453",
			Networks["base"].USDCContract, expected.PayTo, "5000000", int64(500), "0.9",
			string(requirements), "simplified", `{}`, time.Now().Add(time.Minute), "offered"))

	h := &X402Handler{db: db}
	_, _, err = h.validateV2Quote(context.Background(), "tenant-1", &X402PaymentPayload{
		QuoteID: "quote-1",
		Accepted: &X402AcceptedRequirements{
			Scheme: "exact", Network: "eip155:8453", Asset: expected.Asset,
			Amount: "5000000", PayTo: expected.PayTo, MaxTimeoutSeconds: 60,
			ExtraJSON: []byte(`{"name":"USD Coin","version":"2","assetTransferMethod":"eip3009","frameworks":{"quoteId":"quote-1","resourceClass":"clip"}}`),
		},
		Payload: &X402ExactPayload{Authorization: &X402Authorization{Value: "5000000", To: expected.PayTo}},
	})
	if err == nil {
		t.Fatal("altered resource extension unexpectedly validated")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateV2QuoteRejectsAlteredTransferMethod(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	expected := x402sdk.PaymentRequirements{
		Scheme: "exact", Network: "eip155:8453",
		Asset: Networks["base"].USDCContract, Amount: "5000000",
		PayTo: "0x1111111111111111111111111111111111111111", MaxTimeoutSeconds: 60,
		Extra: map[string]interface{}{
			"name": "USD Coin", "version": "2", "assetTransferMethod": "eip3009",
			"frameworks": map[string]interface{}{"quoteId": "quote-1", "resourceClass": "graphql"},
		},
	}
	requirements, _ := json.Marshal(expected)
	mock.ExpectQuery("SELECT id::text, tenant_id::text, resource, resource_class").
		WithArgs("quote-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "resource", "resource_class", "network", "asset", "pay_to",
			"amount_atomic", "credit_amount_cents", "eur_per_usd_rate", "requirements_json",
			"tax_document_kind", "tax_profile_snapshot", "expires_at", "status",
		}).AddRow("quote-1", "tenant-1", "graphql://createStream", "graphql", "eip155:8453",
			Networks["base"].USDCContract, expected.PayTo, "5000000", int64(500), "0.9",
			string(requirements), "simplified", `{}`, time.Now().Add(time.Minute), "offered"))

	h := &X402Handler{db: db}
	_, _, err = h.validateV2Quote(context.Background(), "tenant-1", &X402PaymentPayload{
		QuoteID: "quote-1",
		Accepted: &X402AcceptedRequirements{
			Scheme: "exact", Network: "eip155:8453", Asset: expected.Asset,
			Amount: "5000000", PayTo: expected.PayTo, MaxTimeoutSeconds: 60,
			ExtraJSON: []byte(`{"name":"USD Coin","version":"2","assetTransferMethod":"permit2","frameworks":{"quoteId":"quote-1","resourceClass":"graphql"}}`),
		},
		Payload: &X402ExactPayload{Authorization: &X402Authorization{Value: "5000000", To: expected.PayTo}},
	})
	if err == nil || !strings.Contains(err.Error(), "immutable quote") {
		t.Fatalf("altered transfer method error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type fakeX402Facilitator struct {
	supported x402sdk.SupportedResponse
}

func (f *fakeX402Facilitator) Verify(context.Context, []byte, []byte) (*x402sdk.VerifyResponse, error) {
	return &x402sdk.VerifyResponse{IsValid: true}, nil
}

func (f *fakeX402Facilitator) Settle(context.Context, []byte, []byte) (*x402sdk.SettleResponse, error) {
	return &x402sdk.SettleResponse{Success: true}, nil
}

func (f *fakeX402Facilitator) GetSupported(context.Context) (x402sdk.SupportedResponse, error) {
	return f.supported, nil
}

func TestAdvertisableNetworksAreFacilitatorIntersection(t *testing.T) {
	t.Setenv("X402_INCLUDE_TESTNETS", "false")
	h := &X402Handler{
		facilitatorProvider: "hosted",
		facilitator: &fakeX402Facilitator{supported: x402sdk.SupportedResponse{Kinds: []x402sdk.SupportedKind{{
			X402Version: 2,
			Scheme:      "exact",
			Network:     "eip155:8453",
		}}}},
	}
	networks, err := h.GetAdvertisableX402Networks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(networks) != 1 || networks[0].Name != "base" {
		t.Fatalf("advertisable networks = %+v, want Base only", networks)
	}
}

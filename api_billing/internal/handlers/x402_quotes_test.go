package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/DATA-DOG/go-sqlmock"
	x402sdk "github.com/x402-foundation/x402/go/v2"
)

func TestCreatePaymentQuotesAssessOnceAndCoverDeficitAndBuffer(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	appconfigtest.Set(t, "X402_PREPAID_BUFFER_EUR_CENTS", "500")

	today := time.Now().UTC().Truncate(24 * time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta("FROM purser.fx_rates")).
		WithArgs("USD", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"currency", "reference_date", "units_per_eur", "source", "fetched_at"}).
			AddRow("USD", today, "1.0000000000", "ecb", time.Now()))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT balance_cents")).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(-250)))
	mock.ExpectQuery("SELECT billing_email, billing_name, billing_company").
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow("buyer@example.test", "Original Buyer", nil, []byte(`{"country":"NL"}`), nil, "consumer"))
	mock.ExpectQuery("SELECT rate_bps, source, source_checked_on::text AS source_checked_on").WillReturnRows(sqlmock.NewRows([]string{"rate_bps", "source", "source_checked_on", "effective_from"}).AddRow(2100, "Dutch VAT", "2026-01-01", "2026-01-01"))
	for _, chain := range []string{"eip155:8453", "eip155:42161"} {
		mock.ExpectExec("INSERT INTO purser.x402_payment_quotes").
			WithArgs(
				sqlmock.AnyArg(), "tenant-1", "graphql://createStream", "graphql",
				chain, "0xAsset", "0xpayto", "9080000", int64(750),
				sqlmock.AnyArg(), "simplified", sqlmock.AnyArg(), sqlmock.AnyArg(),
				int64(908), "USD", int64(908), "1", "ecb", sqlmock.AnyArg(),
			).
			WillReturnResult(sqlmock.NewResult(1, 1))

	}
	h := &X402Handler{db: db, topupUSDCents: 500, supplierCountry: "NL", supplierName: "DDVTech B.V.", supplierAddress: "Leiden, NL", supplierVAT: "NL123456789B01", supplierRegistration: "12345678"}
	quotes, requirement, err := h.CreatePaymentQuotes(context.Background(), "tenant-1", "graphql://createStream", "0xPayTo", []NetworkConfig{{Name: "base", ChainID: 8453, USDCContract: "0xAsset", USDCDomainName: "USD Coin"}, {Name: "arbitrum", ChainID: 42161, USDCContract: "0xAsset", USDCDomainName: "USD Coin"}}, "")
	if err != nil {
		t.Fatalf("CreatePaymentQuote() error = %v", err)
	}
	if len(quotes) != 2 || requirement.RequiresCompleteProfile {
		t.Fatalf("quotes=%d requirement=%+v", len(quotes), requirement)
	}
	quote := quotes[0]
	if string(quote.TaxProfileSnapshot) != string(quotes[1].TaxProfileSnapshot) || !quote.ExpiresAt.Equal(quotes[1].ExpiresAt) || quote.FX.EURMinor != quotes[1].FX.EURMinor {
		t.Fatal("network options received different tax/financial assessments")
	}
	if quote.AmountAtomic != "9080000" || quote.CreditAmountCents != 750 {
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
		WithArgs(sqlmock.AnyArg(), "quote-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE purser.x402_payment_quotes").
		WithArgs(sqlmock.AnyArg(), "quote-1").
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
		WithArgs(sqlmock.AnyArg(), "quote-1").
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
	mock.ExpectQuery("SELECT id::text AS id, tenant_id::text AS tenant_id, resource, resource_class").
		WithArgs("quote-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "resource", "resource_class", "network", "asset", "pay_to",
			"amount_atomic", "credit_amount_cents", "requirements_json",
			"tax_document_kind", "tax_profile_snapshot", "expires_at", "status",
			"original_amount_cents", "original_currency", "eur_amount_cents",
			"fx_units_per_eur", "fx_source", "fx_reference_date",
		}).AddRow("quote-1", "tenant-1", "graphql://createStream", "graphql", "eip155:8453",
			Networks["base"].USDCContract, expected.PayTo, "5000000", int64(500),
			requirements, "simplified", []byte(`{}`), time.Now().Add(time.Minute), "offered",
			int64(556), "USD", int64(500), "1.1111111111", "ecb", time.Now()))

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
	mock.ExpectQuery("SELECT id::text AS id, tenant_id::text AS tenant_id, resource, resource_class").
		WithArgs("quote-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "resource", "resource_class", "network", "asset", "pay_to",
			"amount_atomic", "credit_amount_cents", "requirements_json",
			"tax_document_kind", "tax_profile_snapshot", "expires_at", "status",
			"original_amount_cents", "original_currency", "eur_amount_cents",
			"fx_units_per_eur", "fx_source", "fx_reference_date",
		}).AddRow("quote-1", "tenant-1", "graphql://createStream", "graphql", "eip155:8453",
			Networks["base"].USDCContract, expected.PayTo, "5000000", int64(500),
			requirements, "simplified", []byte(`{}`), time.Now().Add(time.Minute), "offered",
			int64(556), "USD", int64(500), "1.1111111111", "ecb", time.Now()))

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
	appconfigtest.Set(t, "X402_INCLUDE_TESTNETS", "false")
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

func TestPaymentQuotesDoNotOfferUnverifiedVATDuringVIESOutage(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("valid_cached_evidence_%v", cached), func(t *testing.T) {
			var calls atomic.Int32
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer endpoint.Close()
			appconfigtest.Set(t, "VIES_ENDPOINT", endpoint.URL)
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			today := time.Now().UTC().Truncate(24 * time.Hour)
			mock.ExpectQuery("FROM purser.fx_rates").WillReturnRows(sqlmock.NewRows([]string{"currency", "reference_date", "units_per_eur", "source", "fetched_at"}).AddRow("USD", today, "1", "ecb", time.Now()))
			mock.ExpectQuery("SELECT balance_cents").WithArgs("tenant-1").WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(0))
			mock.ExpectQuery("SELECT billing_email, billing_name, billing_company").WithArgs("tenant-1").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow("buyer@example.test", "German Buyer", nil, []byte(`{"street":"Main 1","city":"Berlin","postal_code":"10115","country":"DE"}`), "DE123456789", "business"))
			expectation := mock.ExpectQuery("-- name: GetCachedVATValidation").WithArgs(viesOutageGraceHours, "tenant-1", sqlmock.AnyArg())
			if cached {
				expectation.WillReturnRows(sqlmock.NewRows([]string{"valid", "validation_snapshot", "fresh", "within_outage_grace"}).AddRow(true, []byte(`{"valid":true,"expires_at":"2027-01-01T00:00:00Z"}`), true, false))
				for range 2 {
					mock.ExpectExec("INSERT INTO purser.x402_payment_quotes").WillReturnResult(sqlmock.NewResult(1, 1))
				}
			} else {
				expectation.WillReturnError(sql.ErrNoRows)
			}
			h := &X402Handler{db: db, supplierCountry: "NL", topupUSDCents: 500, supplierName: "DDVTech B.V.", supplierAddress: "Leiden, NL", supplierVAT: "NL123456789B01", supplierRegistration: "12345678"}
			quotes, requirement, err := h.CreatePaymentQuotes(context.Background(), "tenant-1", "mcp://test", "0xPayTo", []NetworkConfig{Networks["base"], Networks["arbitrum"]}, "")
			if cached {
				if err != nil || len(quotes) != 2 || calls.Load() != 0 || len(requirement.Profile.TaxAssessment.VATValidation) == 0 {
					t.Fatalf("valid cached proof was not usable: quotes=%d calls=%d err=%v", len(quotes), calls.Load(), err)
				}
			} else {
				var review *billingtax.ReviewError
				if !errors.As(err, &review) || review.Code != "vat_validation_unavailable" || len(quotes) != 0 || calls.Load() != 1 {
					t.Fatalf("unverified VAT was advertised or repeatedly retried: quotes=%d calls=%d err=%v", len(quotes), calls.Load(), err)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

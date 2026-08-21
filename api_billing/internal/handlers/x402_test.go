package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
)

func TestDeriveAddressFromPrivKey(t *testing.T) {
	address, err := deriveAddressFromPrivKey("4f3edf983ac636a65a842ce7c78d9aa706d3b113b37f1f6f0f6a16c3b7f1f941")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if address != "0xfa99341c1e9bf760dfec7e938943792f1cc73e16" {
		t.Fatalf("unexpected address: %s", address)
	}
}

func TestBaseSepoliaUsesLiveUSDCDomainName(t *testing.T) {
	if got := Networks["base-sepolia"].USDCDomainName; got != "USDC" {
		t.Fatalf("Base Sepolia USDC EIP-712 domain = %q, want %q", got, "USDC")
	}
	if got := Networks["base"].USDCDomainName; got != "" {
		t.Fatalf("Base mainnet should use default USD Coin domain, got override %q", got)
	}
}

func TestExtractVATInclusive(t *testing.T) {
	tests := []struct {
		name    string
		gross   int64
		rateBps int
		wantNet int64
		wantVAT int64
	}{
		{name: "Dutch standard rate", gross: 500, rateBps: 2100, wantNet: 413, wantVAT: 87},
		{name: "zero rate", gross: 500, rateBps: 0, wantNet: 500, wantVAT: 0},
		{name: "zero gross", gross: 0, rateBps: 2100, wantNet: 0, wantVAT: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			net, vat := extractVATInclusive(tc.gross, tc.rateBps)
			if net != tc.wantNet || vat != tc.wantVAT {
				t.Fatalf("extractVATInclusive(%d, %d) = (%d, %d), want (%d, %d)", tc.gross, tc.rateBps, net, vat, tc.wantNet, tc.wantVAT)
			}
		})
	}
}

func TestGetNetworkConfigRespectsX402Settings(t *testing.T) {
	handler := &X402Handler{logger: logrus.New(), includeTestnets: false}

	if _, err := handler.getNetworkConfig("ethereum"); err == nil {
		t.Fatal("expected error for x402-disabled network")
	}

	if _, err := handler.getNetworkConfig("base-sepolia"); err == nil {
		t.Fatal("expected error for testnet when disabled")
	}

	if _, err := handler.getNetworkConfig("base"); err != nil {
		t.Fatalf("expected base network to be allowed, got %v", err)
	}
}

func TestGetNetworkConfigAllowsTestnetsWhenEnabled(t *testing.T) {
	t.Setenv("BUILD_ENV", "development")
	handler := &X402Handler{logger: logrus.New(), includeTestnets: true}

	if _, err := handler.getNetworkConfig("base-sepolia"); err != nil {
		t.Fatalf("expected testnet to be allowed, got %v", err)
	}
}

func TestGetNetworkConfigRejectsTestnetsInProduction(t *testing.T) {
	t.Setenv("BUILD_ENV", "production")
	handler := &X402Handler{logger: logrus.New(), includeTestnets: true}

	if _, err := handler.getNetworkConfig("base-sepolia"); err == nil {
		t.Fatal("expected production to reject testnet settlement")
	}
	for _, network := range handler.GetSupportedNetworks() {
		if network.IsTestnet {
			t.Fatalf("production advertised testnet %q", network.Name)
		}
	}
}

func TestRequiredTopupAmount(t *testing.T) {
	t.Run("configured exact amount", func(t *testing.T) {
		handler := &X402Handler{topupUSDCents: 725}
		if got := handler.RequiredTopupUSDCents(); got != 725 {
			t.Fatalf("RequiredTopupUSDCents() = %d, want 725", got)
		}
		if got := handler.RequiredTopupBaseUnits(); got != "7250000" {
			t.Fatalf("RequiredTopupBaseUnits() = %q, want 7250000", got)
		}
	})

	t.Run("invalid values fail to safe default", func(t *testing.T) {
		for _, configured := range []int64{-1, 0, maximumX402TopupUSDCents + 1} {
			handler := &X402Handler{topupUSDCents: configured}
			if got := handler.RequiredTopupUSDCents(); got != defaultX402TopupUSDCents {
				t.Fatalf("configured %d: got %d, want %d", configured, got, defaultX402TopupUSDCents)
			}
		}
	})
}

func TestVerifyPaymentRejectsProtocolDowngradeBeforeDatabaseAccess(t *testing.T) {
	handler := &X402Handler{logger: logrus.New()}
	base := &X402PaymentPayload{
		X402Version: 3,
		Scheme:      "exact",
		Network:     "base",
		Payload: &X402ExactPayload{Authorization: &X402Authorization{
			Value: "5000000",
		}},
	}
	result, err := handler.VerifyPayment(context.Background(), "tenant-1", base, "")
	if err != nil || result.Valid || result.Error != "unsupported x402 version" {
		t.Fatalf("unexpected version validation result: result=%#v err=%v", result, err)
	}

	base.X402Version = 1
	base.Scheme = "upto"
	result, err = handler.VerifyPayment(context.Background(), "tenant-1", base, "")
	if err != nil || result.Valid || result.Error != "unsupported x402 scheme" {
		t.Fatalf("unexpected scheme validation result: result=%#v err=%v", result, err)
	}
}

func TestSettlePaymentReturnsExistingSettlementBeforeVerification(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	payload := &X402PaymentPayload{
		X402Version: 1,
		Scheme:      "exact",
		Network:     "base",
		Payload: &X402ExactPayload{
			Signature: "0xsig",
			Authorization: &X402Authorization{
				From:        "0x1111111111111111111111111111111111111111",
				To:          "0x2222222222222222222222222222222222222222",
				Value:       "25000000",
				ValidAfter:  "1",
				ValidBefore: "9999999999",
				Nonce:       "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		},
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	handler := &X402Handler{db: mockDB, logger: logrus.New()}
	mock.ExpectQuery("SELECT id::text AS id, network, tx_hash, tenant_id::text AS tenant_id").
		WithArgs("base", "0x1111111111111111111111111111111111111111", payload.Payload.Authorization.Nonce).
		WillReturnRows(sqlmock.NewRows([]string{"id", "network", "tx_hash", "tenant_id", "amount_cents", "status", "auth_payload", "client_ip"}).
			AddRow("nonce-1", "base", "0xabc", "tenant-1", int64(2300), "confirmed", string(payloadJSON), "127.0.0.1"))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("tenant-1", "nonce-1", "x402_payment", "topup").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery("SELECT balance_cents FROM purser.prepaid_balances").
		WithArgs("tenant-1", "EUR").
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(7300)))

	result, err := handler.SettlePayment(context.Background(), "tenant-1", payload, "127.0.0.1")
	if err != nil {
		t.Fatalf("SettlePayment returned error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected successful idempotent result, got %#v", result)
	}
	if result.TxHash != "0xabc" || result.CreditedCents != 2300 || result.NewBalanceCents != 7300 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestIdempotentPendingSettlementDoesNotCreditBeforeConfirmation(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	handler := &X402Handler{db: mockDB, logger: logrus.New()}
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("tenant-1", "nonce-1", "x402_payment", "topup").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	result, err := handler.buildIdempotentSettleResult(context.Background(), "tenant-1", &SettlementRow{
		ID:          "nonce-1",
		TxHash:      "0xabc",
		AmountCents: 500,
		Status:      "pending",
	}, "")
	if err != nil {
		t.Fatalf("buildIdempotentSettleResult returned error: %v", err)
	}
	if result == nil || result.Success || result.ErrorCode != "SETTLEMENT_PENDING" || result.TxHash != "0xabc" {
		t.Fatalf("expected safe pending result, got %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestReverseChargeRequiresValidatedCrossBorderVATID(t *testing.T) {
	tests := []struct {
		name             string
		supplierCountry  string
		customerCountry  string
		validated        bool
		reverseChargeVAT bool
	}{
		{name: "cross-border validated", supplierCountry: "NL", customerCountry: "DE", validated: true, reverseChargeVAT: true},
		{name: "domestic validated", supplierCountry: "NL", customerCountry: "NL", validated: true, reverseChargeVAT: false},
		{name: "cross-border unvalidated", supplierCountry: "NL", customerCountry: "DE", validated: false, reverseChargeVAT: false},
		{name: "non-EU supplier", supplierCountry: "US", customerCountry: "DE", validated: true, reverseChargeVAT: false},
		{name: "missing supplier country", customerCountry: "DE", validated: true, reverseChargeVAT: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := reverseChargeEligible(test.supplierCountry, test.customerCountry, test.validated); got != test.reverseChargeVAT {
				t.Fatalf("reverseChargeEligible() = %v, want %v", got, test.reverseChargeVAT)
			}
		})
	}
}

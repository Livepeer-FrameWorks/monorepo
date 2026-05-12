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
	handler := &X402Handler{logger: logrus.New(), includeTestnets: true}

	if _, err := handler.getNetworkConfig("base-sepolia"); err != nil {
		t.Fatalf("expected testnet to be allowed, got %v", err)
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
	mock.ExpectQuery("SELECT id, tx_hash, tenant_id, amount_cents, status").
		WithArgs("base", "0x1111111111111111111111111111111111111111", payload.Payload.Authorization.Nonce).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tx_hash", "tenant_id", "amount_cents", "status", "auth_payload"}).
			AddRow("nonce-1", "0xabc", "tenant-1", int64(2300), "confirmed", string(payloadJSON)))
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

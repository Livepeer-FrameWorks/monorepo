package grpc

import (
	"context"
	"testing"
	"time"

	"frameworks/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestLoadInvoiceCryptoPaymentQuote(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	quotedAt := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	expiresAt := quotedAt.Add(30 * time.Minute)
	mock.ExpectQuery("SELECT wallet_address, expected_amount_base_units::text, quoted_price_usd::text").
		WithArgs("invoice-1", "tenant-1", "ETH").
		WillReturnRows(sqlmock.NewRows([]string{
			"wallet_address",
			"expected_amount_base_units",
			"quoted_price_usd",
			"quote_source",
			"asset",
			"network",
			"quoted_at",
			"expires_at",
		}).AddRow(
			"0xabc",
			"1000000000000000000",
			"3000.00",
			"chainlink",
			"ETH",
			"arbitrum",
			quotedAt,
			expiresAt,
		))

	server := &PurserServer{db: db, logger: logging.NewLogger()}
	got, err := server.loadInvoiceCryptoPaymentQuote(context.Background(), "invoice-1", "tenant-1", "ETH")
	if err != nil {
		t.Fatalf("loadInvoiceCryptoPaymentQuote returned error: %v", err)
	}
	if got.WalletAddress != "0xabc" {
		t.Fatalf("wallet address = %q, want 0xabc", got.WalletAddress)
	}
	if got.ExpectedAmountToken != "1.000000000000000000" {
		t.Fatalf("expected token = %q, want 1.000000000000000000", got.ExpectedAmountToken)
	}
	if got.Network != "arbitrum" || got.AssetSymbol != "ETH" {
		t.Fatalf("asset/network = %s/%s, want ETH/arbitrum", got.AssetSymbol, got.Network)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

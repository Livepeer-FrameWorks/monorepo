package grpc

import (
	"context"
	"testing"

	"frameworks/api_billing/internal/handlers"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestLoadInvoiceBalanceTxSubtractsConfirmedNetPayments(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`SELECT invoice.tenant_id::text AS tenant_id,[\s\S]*FOR UPDATE`).
		WithArgs("invoice-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "total_amount", "currency", "net_paid"}).
			AddRow("tenant-1", "100.00", "EUR", "30.25"))

	balance, err := loadInvoiceBalanceTx(context.Background(), tx, "invoice-1", "tenant-1")
	if err != nil {
		t.Fatalf("loadInvoiceBalanceTx: %v", err)
	}
	if got := balance.AmountDue.StringFixed(2); got != "69.75" {
		t.Fatalf("amount due = %s, want 69.75", got)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredInvoiceCardProviderRequiresCompleteExplicitConfiguration(t *testing.T) {
	for _, key := range []string{
		"PAYMENT_CARD_PROVIDER", "WEBAPP_PUBLIC_URL", "GATEWAY_PUBLIC_URL",
		"STRIPE_SECRET_KEY", "STRIPE_WEBHOOK_SECRET", "MOLLIE_API_KEY",
	} {
		t.Setenv(key, "")
	}
	if _, err := configuredInvoiceCardProvider(); err == nil {
		t.Fatal("unconfigured provider must not be advertised")
	}

	t.Setenv("WEBAPP_PUBLIC_URL", "https://app.example.com")
	t.Setenv("STRIPE_SECRET_KEY", "sk_test")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
	provider, err := configuredInvoiceCardProvider()
	if err != nil || provider != handlers.ProviderStripe {
		t.Fatalf("Stripe provider = %q, %v", provider, err)
	}

	t.Setenv("GATEWAY_PUBLIC_URL", "https://api.example.com")
	t.Setenv("MOLLIE_API_KEY", "test_mollie")
	if _, providerErr := configuredInvoiceCardProvider(); providerErr == nil {
		t.Fatal("two providers without PAYMENT_CARD_PROVIDER must be rejected")
	}
	t.Setenv("PAYMENT_CARD_PROVIDER", "mollie")
	provider, err = configuredInvoiceCardProvider()
	if err != nil || provider != handlers.ProviderMollie {
		t.Fatalf("Mollie provider = %q, %v", provider, err)
	}
}

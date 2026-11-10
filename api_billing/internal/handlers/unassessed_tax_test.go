package handlers

import (
	"testing"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	api "github.com/stripe/stripe-go/v85"
)

// expectAssessedTaxHandling answers the tax handling read for a relationship
// that already uses assessed tax handling.
func expectAssessedTaxHandling(mock sqlmock.Sqlmock, tenant string) {
	mock.ExpectQuery("-- name: TenantKeepsUnassessedTax").WithArgs(tenant).WillReturnRows(sqlmock.NewRows([]string{"keeps_unassessed"}).AddRow(false))
}

// recordingStripeBackend answers like the recurring fallback backend for a
// Canadian Stripe customer and records every provider call.
type recordingStripeBackend struct {
	recurringFallbackBackend
	calls []string
}

func (b *recordingStripeBackend) Call(method, path, key string, params api.ParamsContainer, out api.LastResponseSetter) error {
	b.calls = append(b.calls, method+" "+path)
	if err := b.recurringFallbackBackend.Call(method, path, key, params, out); err != nil {
		return err
	}
	if customer, ok := out.(*api.Customer); ok {
		customer.Address = &api.Address{Line1: "1 King St W", City: "Toronto", PostalCode: "M5H 1A1", Country: "CA"}
	}
	return nil
}

func TestExistingStripeSubscriptionKeepsItsProviderTaxSetup(t *testing.T) {
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	for _, tc := range []struct {
		name    string
		profile *sqlmock.Rows
	}{
		{"no saved profile", sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"})},
		{"incomplete saved profile", sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow("buyer@example.ca", nil, nil, []byte(`{"city":"Toronto","country":"CA"}`), nil, "unspecified")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			// Expectations are unordered: the reads and writes a domestic
			// fallback would make are declared so that a reconcile which ignores
			// the existing relationship reaches the provider instead of failing
			// on an unexpected query.
			mock.MatchExpectationsInOrder(false)
			mock.ExpectQuery("-- name: ListTenantStripeTaxSubscriptions").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{"customer_id", "subscription_id"}).AddRow("customer", "sub"))
			mock.ExpectQuery("-- name: TenantKeepsUnassessedTax").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{"keeps_unassessed"}).AddRow(true))
			mock.ExpectQuery("-- name: GetCryptoDocumentBillingProfile").WithArgs("tenant").WillReturnRows(tc.profile)
			mock.ExpectQuery("-- name: GetEffectiveVATRate").WillReturnRows(sqlmock.NewRows([]string{"rate_bps", "source", "source_checked_on", "effective_from"}).AddRow(2100, "Dutch effective rate", "2026-11-01", "2026-01-01"))
			stored := false
			mock.ExpectExec("-- name: StoreProviderTaxProfile").WithArgs("tenant", sqlmock.AnyArg(), retainedUpgradeTaxArg{profile: &billingstripe.TaxProfile{}, recorded: &stored}).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("-- name: RetainProviderTaxExpectation").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("-- name: RecordBillingTaxReview").WillReturnResult(sqlmock.NewResult(0, 1))
			backend := &recordingStripeBackend{}
			previous := api.GetBackend(api.APIBackend)
			api.SetBackend(api.APIBackend, backend)
			t.Cleanup(func() { api.SetBackend(api.APIBackend, previous) })
			svc := &Service{db: db, logger: logging.NewLogger(), stripeClient: billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test", Logger: logging.NewLogger()})}
			jm := &JobManager{db: db, billing: svc, logger: logging.NewLogger()}
			err = jm.reconcileStripeTaxTenant(t.Context(), "tenant")
			if err != nil || len(backend.calls) != 0 || stored {
				t.Fatalf("existing subscription was synchronized to assessed tax: err=%v stripe calls=%v stored NL profile=%v", err, backend.calls, stored)
			}
		})
	}
}

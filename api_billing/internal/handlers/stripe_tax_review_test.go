package handlers

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"testing"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	billingstripe "frameworks/api_billing/internal/stripe"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	api "github.com/stripe/stripe-go/v85"
)

type taxReviewBackend struct {
	stripeUpgradeTaxBackend
	called bool
}

type multipleTaxSubscriptionsBackend struct {
	stripeUpgradeTaxBackend
	failed  error
	reads   []string
	current *api.Subscription
}

func (b *multipleTaxSubscriptionsBackend) Call(method, path, key string, params api.ParamsContainer, out api.LastResponseSetter) error {
	if result, ok := out.(*api.Subscription); ok {
		if method == http.MethodGet {
			b.reads = append(b.reads, path)
			if path == "/v1/subscriptions/broken" {
				return b.failed
			}
			if b.current != nil {
				*result = *b.current
				return nil
			}
		}
		if err := b.stripeUpgradeTaxBackend.Call(method, path, key, params, out); err != nil {
			return err
		}
		if method == http.MethodGet {
			copy := *result
			b.current = &copy
		} else {
			for key, value := range params.(*api.SubscriptionParams).Metadata {
				b.current.Metadata[key] = value
			}
		}
		return nil
	}
	return b.stripeUpgradeTaxBackend.Call(method, path, key, params, out)
}

func TestStripeTaxReconciliationContinuesOtherTenantSubscriptions(t *testing.T) {
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("-- name: ListTenantStripeTaxSubscriptions").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{"customer_id", "subscription_id"}).AddRow("customer", "broken").AddRow("customer", "sub"))
	mock.ExpectQuery("-- name: GetCryptoDocumentBillingProfile").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow("buyer@example.test", "Buyer", "Buyer Ltd", []byte(`{"street":"Main1","city":"Toronto","postal_code":"M5V1A1","country":"CA"}`), nil, "business"))
	retained := &billingstripe.TaxProfile{}
	mock.ExpectExec("-- name: StoreProviderTaxProfile").WithArgs("tenant", sqlmock.AnyArg(), retainedUpgradeTaxArg{profile: retained}).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("-- name: RetainProviderTaxExpectation").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("-- name: ResolveProviderTaxReview").WithArgs(sqlmock.AnyArg(), "tenant", "stripe_subscription", "sub").WillReturnResult(sqlmock.NewResult(0, 1))
	failure := errors.New("first provider subscription unavailable")
	b := &multipleTaxSubscriptionsBackend{stripeUpgradeTaxBackend: stripeUpgradeTaxBackend{profile: retained}, failed: failure}
	previous := api.GetBackend(api.APIBackend)
	api.SetBackend(api.APIBackend, b)
	t.Cleanup(func() { api.SetBackend(api.APIBackend, previous) })
	svc := &Service{db: db, logger: logging.NewLogger(), stripeClient: billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test", Logger: logging.NewLogger()})}
	jm := &JobManager{db: db, billing: svc, logger: logging.NewLogger()}
	if err = jm.reconcileStripeTaxTenant(t.Context(), "tenant"); !errors.Is(err, failure) {
		t.Fatalf("first failure was lost: %v", err)
	}
	if len(b.reads) < 2 || b.reads[0] != "/v1/subscriptions/broken" || b.reads[1] != "/v1/subscriptions/sub" || b.taxUpdates != 1 {
		t.Fatalf("one failed subscription skipped the remaining tenant: reads%v updates%d", b.reads, b.taxUpdates)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStripeTaxAssessmentFailuresNeverPauseCollection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		hold     bool
		report   bool
		fallback bool
	}{
		{"missing billing profile", sql.ErrNoRows, false, true, true},
		{"database unavailable", errors.New("database unavailable"), false, false, false},
		{"deadline", context.DeadlineExceeded, false, false, false},
		{"cancelled", context.Canceled, false, false, false},
		{"VIES temporarily unavailable", billingtax.Review("vat_validation_unavailable", "VAT validation is temporarily unavailable"), false, false, false},
		{"business evidence store unavailable", billingtax.Review("business_evidence_unavailable", "Business verification is temporarily unavailable; retry before paying"), false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
				appconfigtest.Set(t, key, value)
			}
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectQuery("-- name: ListTenantStripeTaxSubscriptions").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{"customer_id", "subscription_id"}).AddRow("customer", "sub"))
			mock.ExpectQuery("-- name: GetCryptoDocumentBillingProfile").WithArgs("tenant").WillReturnError(tc.err)
			if tc.fallback {
				mock.ExpectQuery("-- name: GetEffectiveVATRate").WillReturnRows(sqlmock.NewRows([]string{"rate_bps", "source", "source_checked_on", "effective_from"}).AddRow(2100, "Dutch effective rate", "2026-11-01", "2026-01-01"))
				mock.ExpectExec("-- name: StoreProviderTaxProfile").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("-- name: RetainProviderTaxExpectation").WillReturnResult(sqlmock.NewResult(0, 1))
			}
			if tc.report {
				mock.ExpectExec("-- name: RecordBillingTaxReview").WillReturnResult(sqlmock.NewResult(0, 1))
			}
			b := &taxReviewBackend{}
			fallback := &recurringFallbackBackend{}
			previous := api.GetBackend(api.APIBackend)
			if tc.fallback {
				api.SetBackend(api.APIBackend, fallback)
			} else {
				api.SetBackend(api.APIBackend, b)
			}
			t.Cleanup(func() { api.SetBackend(api.APIBackend, previous) })
			svc := &Service{db: db, logger: logging.NewLogger(), stripeClient: billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test", Logger: logging.NewLogger()})}
			jm := &JobManager{db: db, billing: svc, logger: logging.NewLogger()}
			err = jm.reconcileStripeTaxTenant(t.Context(), "tenant")
			if tc.fallback {
				if err != nil || fallback.taxUpdates != 1 {
					t.Fatalf("missing recurring profile did not sync Dutch VAT: %v updates%d", err, fallback.taxUpdates)
				}
			} else if err == nil {
				t.Fatal("assessment failure disappeared")
			}
			if b.called {
				t.Fatal("assessment failure reached the provider")
			}

			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLegacyWhitespaceOnlyBillingIdentityReportsWithoutPausingCollection(t *testing.T) {
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("-- name: ListTenantStripeTaxSubscriptions").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{"customer_id", "subscription_id"}).AddRow("customer", "sub"))
	mock.ExpectQuery("-- name: GetCryptoDocumentBillingProfile").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow(" buyer@example.test ", " Buyer ", " Buyer Ltd ", []byte(`{"street":" \t ","city":" Toronto ","postal_code":" M5V1A1 ","country":" CA "}`), nil, "business"))
	mock.ExpectQuery("-- name: GetEffectiveVATRate").WillReturnRows(sqlmock.NewRows([]string{"rate_bps", "source", "source_checked_on", "effective_from"}).AddRow(2100, "Dutch effective rate", "2026-11-01", "2026-01-01"))
	mock.ExpectExec("-- name: StoreProviderTaxProfile").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("-- name: RetainProviderTaxExpectation").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("-- name: RecordBillingTaxReview").WillReturnResult(sqlmock.NewResult(0, 1))
	b := &recurringFallbackBackend{}
	previous := api.GetBackend(api.APIBackend)
	api.SetBackend(api.APIBackend, b)
	t.Cleanup(func() { api.SetBackend(api.APIBackend, previous) })
	svc := &Service{db: db, logger: logging.NewLogger(), stripeClient: billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test", Logger: logging.NewLogger()})}
	jm := &JobManager{db: db, billing: svc, logger: logging.NewLogger()}
	err = jm.reconcileStripeTaxTenant(t.Context(), "tenant")
	if err != nil || b.taxUpdates != 1 {
		t.Fatalf("legacy whitespace-only details did not sync Dutch VAT without pausing collection: %v updates%d", err, b.taxUpdates)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func (b *taxReviewBackend) Call(method, path, key string, params api.ParamsContainer, out api.LastResponseSetter) error {
	b.called = true
	return errors.New("tax assessment failure must not mutate provider state")
}

func TestStripeTaxConfigurationFailureDoesNotMutateProvider(t *testing.T) {
	appconfigtest.Set(t, "SUPPLIER_NAME", "")
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("-- name: ListTenantStripeTaxSubscriptions").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{"customer_id", "subscription_id"}).AddRow("customer", "sub"))
	b := &taxReviewBackend{}
	previous := api.GetBackend(api.APIBackend)
	api.SetBackend(api.APIBackend, b)
	t.Cleanup(func() { api.SetBackend(api.APIBackend, previous) })
	svc := &Service{db: db, logger: logging.NewLogger(), stripeClient: billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test", Logger: logging.NewLogger()})}
	jm := &JobManager{db: db, billing: svc, logger: logging.NewLogger()}
	if err = jm.reconcileStripeTaxTenant(context.Background(), "tenant"); err == nil {
		t.Fatal("invalid tax policy unexpectedly accepted")
	}
	if b.called {
		t.Fatal("configuration failure changed Stripe tax or collection state")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

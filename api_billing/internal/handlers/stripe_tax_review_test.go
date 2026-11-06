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
	paused          bool
	clearedCustomer bool
}

type multipleTaxSubscriptionsBackend struct {
	stripeUpgradeTaxBackend
	failed error
	reads  []string
}

func (b *multipleTaxSubscriptionsBackend) Call(method, path, key string, params api.ParamsContainer, out api.LastResponseSetter) error {
	if _, ok := out.(*api.Subscription); ok && method == http.MethodGet {
		b.reads = append(b.reads, path)
		if path == "/v1/subscriptions/broken" {
			return b.failed
		}
	}
	return b.stripeUpgradeTaxBackend.Call(method, path, key, params, out)
}

func TestStripeTaxReconciliationContinuesOtherTenantSubscriptions(t *testing.T) {
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL", "CRYPTO_TAX_POLICY_JSON": ""} {
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
	if len(b.reads) != 2 || b.taxUpdates != 1 {
		t.Fatalf("one failed subscription skipped the remaining tenant: reads%v updates%d", b.reads, b.taxUpdates)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStripeTaxOnlyBillingReviewHoldsCollection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		hold   bool
		report bool
	}{
		{"missing billing profile", sql.ErrNoRows, false, true},
		{"invalid VAT claim", billingtax.Review("vat_number_invalid", "Business VAT number is invalid"), true, true},
		{"database unavailable", errors.New("database unavailable"), false, false},
		{"deadline", context.DeadlineExceeded, false, false},
		{"cancelled", context.Canceled, false, false},
		{"VIES temporarily unavailable", billingtax.Review("vat_validation_unavailable", "VAT validation is temporarily unavailable"), false, false},
		{"business evidence store unavailable", billingtax.Review("business_evidence_unavailable", "Business verification is temporarily unavailable; retry before paying"), false, false},
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
			if tc.report {
				mock.ExpectExec("-- name: RecordBillingTaxReview").WillReturnResult(sqlmock.NewResult(0, 1))
			}
			b := &taxReviewBackend{}
			previous := api.GetBackend(api.APIBackend)
			api.SetBackend(api.APIBackend, b)
			t.Cleanup(func() { api.SetBackend(api.APIBackend, previous) })
			svc := &Service{db: db, logger: logging.NewLogger(), stripeClient: billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test", Logger: logging.NewLogger()})}
			jm := &JobManager{db: db, billing: svc, logger: logging.NewLogger()}
			if err = jm.reconcileStripeTaxTenant(t.Context(), "tenant"); err == nil {
				t.Fatal("assessment failure disappeared")
			}
			if b.paused != tc.hold || b.clearedCustomer != tc.hold {
				t.Fatalf("collection changed=%v/%v, want %v", b.paused, b.clearedCustomer, tc.hold)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLegacyWhitespaceOnlyBillingIdentityReportsWithoutPausingCollection(t *testing.T) {
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL", "CRYPTO_TAX_POLICY_JSON": ""} {
		appconfigtest.Set(t, key, value)
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("-- name: ListTenantStripeTaxSubscriptions").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{"customer_id", "subscription_id"}).AddRow("customer", "sub"))
	mock.ExpectQuery("-- name: GetCryptoDocumentBillingProfile").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow(" buyer@example.test ", " Buyer ", " Buyer Ltd ", []byte(`{"street":" \t ","city":" Toronto ","postal_code":" M5V1A1 ","country":" CA "}`), nil, "business"))
	mock.ExpectExec("-- name: RecordBillingTaxReview").WillReturnResult(sqlmock.NewResult(0, 1))
	b := &taxReviewBackend{}
	previous := api.GetBackend(api.APIBackend)
	api.SetBackend(api.APIBackend, b)
	t.Cleanup(func() { api.SetBackend(api.APIBackend, previous) })
	svc := &Service{db: db, logger: logging.NewLogger(), stripeClient: billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test", Logger: logging.NewLogger()})}
	jm := &JobManager{db: db, billing: svc, logger: logging.NewLogger()}
	err = jm.reconcileStripeTaxTenant(t.Context(), "tenant")
	var review *billingtax.ReviewError
	if !errors.As(err, &review) || review.Code != "billing_profile_required" {
		t.Fatalf("missing normalized legacy identity did not produce actionable review: %v", err)
	}
	if b.paused || b.clearedCustomer {
		t.Fatal("legacy whitespace-only fields paused a working provider subscription")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func (b *taxReviewBackend) Call(method, path, key string, params api.ParamsContainer, out api.LastResponseSetter) error {
	switch v := out.(type) {
	case *api.Subscription:
		if method == http.MethodGet {
			*v = api.Subscription{ID: "sub", Customer: &api.Customer{ID: "customer"}, Metadata: map[string]string{"tenant_id": "tenant"}, DefaultTaxRates: []*api.TaxRate{{ID: "stale0"}}, Items: &api.SubscriptionItemList{Data: []*api.SubscriptionItem{{ID: "item", TaxRates: []*api.TaxRate{{ID: "stale0"}}}}}}
			return nil
		}
		p := params.(*api.SubscriptionParams)
		if p.PauseCollection == nil || api.StringValue(p.PauseCollection.Behavior) != "keep_as_draft" || api.StringValue(p.ProrationBehavior) != "none" || len(p.Items) != 1 || len(p.Items[0].UnsetFields) == 0 || len(p.UnsetFields) == 0 || p.Metadata["frameworks_tax_review"] != "true" {
			return errors.New("failed tax check did not hold collection and clear stale rates")
		}
		b.paused = true
		return nil
	case *api.Customer:
		if method == http.MethodGet {
			*v = api.Customer{ID: "customer", Metadata: map[string]string{"tenant_id": "tenant"}, TaxExempt: api.CustomerTaxExemptReverse}
			return nil
		}
		p := params.(*api.CustomerParams)
		if !b.paused || api.StringValue(p.TaxExempt) != "none" {
			return errors.New("customer exemption cleared without holding collection first")
		}
		b.clearedCustomer = true
		return nil
	}
	return errors.New("unexpected tax-review provider request")
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
	if b.paused || b.clearedCustomer {
		t.Fatal("configuration failure changed Stripe tax or collection state")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

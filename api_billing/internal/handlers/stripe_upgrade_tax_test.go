package handlers

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	api "github.com/stripe/stripe-go/v85"
)

type retainedUpgradeTaxArg struct {
	profile  *billingstripe.TaxProfile
	recorded *bool
}

func (a retainedUpgradeTaxArg) Match(value driver.Value) bool {
	raw, ok := value.([]byte)
	if !ok || json.Unmarshal(raw, a.profile) != nil {
		return false
	}
	if a.recorded != nil {
		*a.recorded = true
	}
	return true
}

type stripeUpgradeTaxBackend struct {
	profile                            *billingstripe.TaxProfile
	syncError                          error
	subReads, taxUpdates, upgradeReads int
	synced                             bool
	allowUnretainedRead                bool
	expectationRecorded                *bool
}

var errUpgradeTaxOrder = errors.New("upgrade reached after retained tax preflight")

func (b *stripeUpgradeTaxBackend) Call(method, path, key string, params api.ParamsContainer, out api.LastResponseSetter) error {
	if b.profile.ProfileSHA == "" && !b.allowUnretainedRead {
		return errors.New("provider called before tax assessment was retained")
	}
	if b.expectationRecorded != nil && !*b.expectationRecorded {
		return errors.New("provider called before intended tax history was retained")
	}
	switch result := out.(type) {
	case *api.Subscription:
		if method == http.MethodGet {
			b.subReads++
			if b.synced {
				b.upgradeReads++
				return errUpgradeTaxOrder
			}
			if b.syncError != nil {
				return b.syncError
			}
			*result = api.Subscription{ID: "sub", Customer: &api.Customer{ID: "customer"}, Metadata: map[string]string{"tenant_id": "tenant"}, DefaultTaxRates: []*api.TaxRate{{ID: "old21"}}, Items: &api.SubscriptionItemList{Data: []*api.SubscriptionItem{{ID: "item", TaxRates: []*api.TaxRate{{ID: "old21"}}}}}}
			return nil
		}
		update, ok := params.(*api.SubscriptionParams)
		if !ok || api.StringValue(update.ProrationBehavior) != "none" || len(update.DefaultTaxRates) != 1 || api.StringValue(update.DefaultTaxRates[0]) != "rate0" || len(update.Items) != 1 || len(update.Items[0].TaxRates) != 1 || api.StringValue(update.Items[0].TaxRates[0]) != "rate0" || update.Metadata["frameworks_tax_profile"] != b.profile.ProfileSHA {
			return errors.New("upgrade tax preflight retained old rate or mutated paid terms")
		}
		b.synced = true
		b.taxUpdates++
		return nil
	case *api.Customer:
		*result = api.Customer{ID: "customer", Metadata: map[string]string{"tenant_id": "tenant"}}
		return nil
	case *api.TaxRate:
		update, ok := params.(*api.TaxRateParams)
		if !ok || api.Float64Value(update.Percentage) != 0 {
			return errors.New("current zero-tax profile was not applied")
		}
		*result = api.TaxRate{ID: "rate0", Country: "NL", Percentage: 0, Inclusive: false}
		return nil
	}
	return fmt.Errorf("unexpected provider operation %s %s", method, path)
}

func TestLocateStripeUpgradeDoesNotAssessOrMutateTax(t *testing.T) {
	backend := &stripeUpgradeTaxBackend{profile: &billingstripe.TaxProfile{}, allowUnretainedRead: true}
	previous := api.GetBackend(api.APIBackend)
	api.SetBackend(api.APIBackend, backend)
	t.Cleanup(func() { api.SetBackend(api.APIBackend, previous) })
	service := &Service{stripeClient: billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test", Logger: logging.NewLogger()})}
	_, err := service.providerTierUpgrade(context.Background(), purserdb.GetTierUpgradeRow{Provider: "stripe", TenantID: "tenant"}, billing.TierUpgradeRequest{ID: "upgrade", TenantID: "tenant", CustomerID: "customer", SubscriptionID: "sub", LocateOnly: true})
	if !errors.Is(err, billing.ErrTierUpgradeRejected) || backend.subReads != 1 || backend.taxUpdates != 0 || backend.upgradeReads != 0 {
		t.Fatalf("locate-only recovery assessed or changed tax: %v %+v", err, backend)
	}
}
func (*stripeUpgradeTaxBackend) CallRaw(string, string, string, []byte, *api.Params, api.LastResponseSetter) error {
	return nil
}
func (*stripeUpgradeTaxBackend) CallStreaming(string, string, string, api.ParamsContainer, api.StreamingLastResponseSetter) error {
	return errors.New("unexpected streaming provider request")
}
func (*stripeUpgradeTaxBackend) CallMultipart(string, string, string, string, *bytes.Buffer, *api.Params, api.LastResponseSetter) error {
	return errors.New("unexpected multipart provider request")
}
func (*stripeUpgradeTaxBackend) SetMaxNetworkRetries(int64) {}

func TestPaidStripeUpgradeRetainsAndSyncsCurrentTaxBeforeProviderPurchase(t *testing.T) {
	for _, tc := range []struct {
		name                                     string
		retainError, expectationError, syncError error
		wantUpgrade                              bool
	}{
		{"profile0 replaces provider21 before upgrade", nil, nil, nil, true},
		{"unretained proof forbids provider call", errors.New("assessment store unavailable"), nil, nil, false},
		{"unretained intended history forbids provider call", nil, errors.New("expectation store unavailable"), nil, false},
		{"failed tax sync forbids upgrade", nil, nil, errors.New("subscription tax unavailable"), false},
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
			retained := &billingstripe.TaxProfile{}
			expectationRecorded := false
			mock.ExpectQuery("-- name: GetCryptoDocumentBillingProfile").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow("buyer@example.test", "Buyer", "Buyer Ltd", []byte(`{"street":"Main1","city":"Toronto","postal_code":"M5V1A1","country":"CA"}`), nil, "business"))
			store := mock.ExpectExec("-- name: StoreProviderTaxProfile").WithArgs("tenant", sqlmock.AnyArg(), retainedUpgradeTaxArg{profile: retained})
			if tc.retainError != nil {
				store.WillReturnError(tc.retainError)
			} else {
				store.WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("-- name: StartAssessedTaxHandling").WithArgs("tenant").WillReturnResult(sqlmock.NewResult(0, 1))
				expectation := mock.ExpectExec("-- name: RetainProviderTaxExpectation").WithArgs("tenant", sqlmock.AnyArg(), sqlmock.AnyArg(), retainedUpgradeTaxArg{profile: retained, recorded: &expectationRecorded})
				if tc.expectationError != nil {
					expectation.WillReturnError(tc.expectationError)
				} else {
					expectation.WillReturnResult(sqlmock.NewResult(0, 1))
				}
			}
			backend := &stripeUpgradeTaxBackend{profile: retained, syncError: tc.syncError, expectationRecorded: &expectationRecorded}
			previous := api.GetBackend(api.APIBackend)
			api.SetBackend(api.APIBackend, backend)
			t.Cleanup(func() { api.SetBackend(api.APIBackend, previous) })
			service := &Service{db: db, logger: logging.NewLogger(), stripeClient: billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test", Logger: logging.NewLogger()})}
			_, err = service.providerTierUpgrade(context.Background(), purserdb.GetTierUpgradeRow{Provider: "stripe", TenantID: "tenant"}, billing.TierUpgradeRequest{ID: "upgrade", TenantID: "tenant", CustomerID: "customer", SubscriptionID: "sub"})
			if tc.wantUpgrade {
				if !errors.Is(err, errUpgradeTaxOrder) || backend.taxUpdates != 1 || backend.upgradeReads != 1 || retained.RateBPS != 0 {
					t.Fatalf("paid upgrade did not follow retained current-tax sync: err%v taxupdates%d upgrades%d profile%+v", err, backend.taxUpdates, backend.upgradeReads, retained)
				}
			} else {
				expected := tc.retainError
				if expected == nil {
					expected = tc.expectationError
				}
				if expected == nil {
					expected = tc.syncError
				}
				if !errors.Is(err, expected) || backend.upgradeReads != 0 || backend.taxUpdates != 0 {
					t.Fatalf("tax preflight failure reached paid upgrade: %v updates%d upgrades%d", err, backend.taxUpdates, backend.upgradeReads)
				}
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

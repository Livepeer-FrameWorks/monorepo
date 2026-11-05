package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/database/purserdb"
	billingmollie "frameworks/api_billing/internal/mollie"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

type upgradeMollieTransport func(*http.Request) (*http.Response, error)

func (fn upgradeMollieTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestPaidMollieUpgradeRetainsCurrentTaxOnExistingNetBeforePurchase(t *testing.T) {
	for _, tc := range []struct {
		name, country, oldRate, oldGross, newGross string
		legacy, retainFail, syncFail               bool
	}{
		{name: "21 to zero retains old net", country: "CA", oldRate: "2100", oldGross: "24.20", newGross: "20.00"},
		{name: "zero to 21 retains old net", country: "NL", oldRate: "0", oldGross: "20.00", newGross: "24.20"},
		{name: "legacy amount is existing net", country: "NL", oldGross: "20.00", newGross: "24.20", legacy: true},
		{name: "unretained proof forbids writes", country: "CA", oldRate: "2100", oldGross: "24.20", retainFail: true},
		{name: "failed tax sync forbids purchase", country: "CA", oldRate: "2100", oldGross: "24.20", syncFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL", "CRYPTO_TAX_POLICY_JSON": ""} {
				appconfigtest.Set(t, key, value)
			}
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			retained := &billingstripe.TaxProfile{}
			mock.ExpectQuery("-- name: GetCryptoDocumentBillingProfile").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow("buyer@example.test", "Buyer", "Buyer Ltd", []byte(fmt.Sprintf(`{"street":"Main1","city":"Town","postal_code":"1234AB","country":%q}`, tc.country)), nil, "business"))
			if tc.country == "NL" {
				mock.ExpectQuery("-- name: GetEffectiveVATRate").WillReturnRows(sqlmock.NewRows([]string{"rate_bps", "source", "source_checked_on", "effective_from"}).AddRow(2100, "Dutch VAT", "2026-01-01", "2026-01-01"))
			}
			failure := errors.New("tax preflight unavailable")
			store := mock.ExpectExec("-- name: StoreProviderTaxProfile").WithArgs("tenant", sqlmock.AnyArg(), retainedUpgradeTaxArg{profile: retained})
			if tc.retainFail {
				store.WillReturnError(failure)
			} else {
				store.WillReturnResult(sqlmock.NewResult(0, 1))
			}
			reads, writes := 0, 0
			synced := false
			transport := upgradeMollieTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/v2/customers/customer/subscriptions/sub" {
					return nil, fmt.Errorf("unexpected provider path %s", r.URL.Path)
				}
				if r.Method == http.MethodPatch {
					writes++
					if retained.ProfileSHA == "" || tc.retainFail {
						return nil, errors.New("provider write before retained proof")
					}
					if tc.syncFail {
						return nil, failure
					}
					var body struct {
						Amount    map[string]string `json:"amount"`
						Metadata  map[string]any    `json:"metadata"`
						StartDate string            `json:"startDate"`
					}
					if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil {
						return nil, decodeErr
					}
					if body.StartDate != "" || body.Amount["value"] != tc.newGross || body.Metadata["frameworks_net_minor"] != "2000" || body.Metadata["frameworks_tax_profile"] != retained.ProfileSHA || body.Metadata["frameworks_vat_bps"] != strconv.Itoa(retained.RateBPS) {
						return nil, fmt.Errorf("tax-only sync changed old net/period or retained stale rate: %+v", body)
					}
					synced = true
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"amount":{"currency":"EUR","value":%q}}`, tc.newGross)))}, nil
				}
				if r.Method != http.MethodGet {
					return nil, errors.New("unexpected provider mutation")
				}
				reads++
				if synced {
					return nil, errUpgradeTaxOrder
				}
				metadata := map[string]string{"tenant_id": "tenant"}
				if !tc.legacy {
					metadata["frameworks_net_minor"], metadata["frameworks_vat_bps"], metadata["frameworks_tax_profile"] = "2000", tc.oldRate, strings.Repeat("a", 64)
				}
				raw, encodeErr := json.Marshal(map[string]any{"id": "sub", "status": "active", "amount": map[string]string{"value": tc.oldGross, "currency": "EUR"}, "metadata": metadata})
				if encodeErr != nil {
					return nil, encodeErr
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})
			client, err := billingmollie.NewClient(billingmollie.Config{APIKey: "test_secret", Logger: logging.NewLogger(), HTTPClient: &http.Client{Transport: transport}})
			if err != nil {
				t.Fatal(err)
			}
			service := &Service{db: db, logger: logging.NewLogger(), mollieClient: client}
			_, err = service.providerTierUpgrade(context.Background(), purserdb.GetTierUpgradeRow{Provider: "mollie", TenantID: "tenant"}, billing.TierUpgradeRequest{ID: "upgrade", TenantID: "tenant", CustomerID: "customer", SubscriptionID: "sub", BaseCents: 5000})
			if tc.retainFail || tc.syncFail {
				if !errors.Is(err, failure) || synced || (tc.retainFail && writes != 0) {
					t.Fatalf("failed preflight reached purchase: err=%v reads=%d writes=%d", err, reads, writes)
				}
			} else if !errors.Is(err, errUpgradeTaxOrder) || !synced || writes != 1 || reads != 3 {
				t.Fatalf("paid upgrade bypassed current-tax sync: err=%v reads=%d writes=%d synced=%v", err, reads, writes, synced)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocateMollieUpgradeDoesNotAssessOrMutateTax(t *testing.T) {
	reads := 0
	client, err := billingmollie.NewClient(billingmollie.Config{APIKey: "test_secret", Logger: logging.NewLogger(), HTTPClient: &http.Client{Transport: upgradeMollieTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			return nil, errors.New("locate-only mutated provider")
		}
		reads++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"sub","metadata":{"tenant_id":"tenant"}}`))}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{mollieClient: client}
	_, err = s.providerTierUpgrade(context.Background(), purserdb.GetTierUpgradeRow{Provider: "mollie", TenantID: "tenant"}, billing.TierUpgradeRequest{ID: "upgrade", TenantID: "tenant", CustomerID: "customer", SubscriptionID: "sub", LocateOnly: true})
	if !errors.Is(err, billing.ErrTierUpgradeRejected) || reads != 1 {
		t.Fatalf("locate-only changed tax: %v reads=%d", err, reads)
	}
}

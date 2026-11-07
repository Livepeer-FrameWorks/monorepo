package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/DATA-DOG/go-sqlmock"
	api "github.com/stripe/stripe-go/v85"
)

type recurringFallbackBackend struct {
	stripeUpgradeTaxBackend
	metadata   map[string]string
	rate       string
	taxUpdates int
}

func (b *recurringFallbackBackend) Call(method, path, key string, params api.ParamsContainer, out api.LastResponseSetter) error {
	switch result := out.(type) {
	case *api.Subscription:
		if b.metadata == nil {
			b.metadata = map[string]string{"tenant_id": "tenant"}
		}
		if method == http.MethodPost {
			p := params.(*api.SubscriptionParams)
			if p.PauseCollection != nil || api.StringValue(p.ProrationBehavior) != "none" {
				return errors.New("fallback paused collection or prorated paid terms")
			}
			if len(p.DefaultTaxRates) > 0 {
				if len(p.DefaultTaxRates) != 1 || api.StringValue(p.DefaultTaxRates[0]) != "vat21" {
					return errors.New("fallback did not synchronize Dutch VAT")
				}
				b.rate = "vat21"
				b.taxUpdates++
			}
			for k, v := range p.Metadata {
				b.metadata[k] = v
			}
		}
		*result = api.Subscription{ID: "sub", Customer: &api.Customer{ID: "customer"}, Metadata: b.metadata, AutomaticTax: &api.SubscriptionAutomaticTax{Enabled: false}}
		if b.rate != "" {
			result.DefaultTaxRates = []*api.TaxRate{{ID: b.rate}}
		}
		return nil
	case *api.Customer:
		if method == http.MethodPost {
			p := params.(*api.CustomerParams)
			if p.Name != nil && strings.TrimSpace(api.StringValue(p.Name)) == "" || p.Email != nil && strings.TrimSpace(api.StringValue(p.Email)) == "" || api.StringValue(p.TaxExempt) != "none" {
				return errors.New("fallback erased known parties or kept exemption")
			}
			if p.Address != nil {
				for _, field := range []*string{p.Address.Line1, p.Address.City, p.Address.PostalCode, p.Address.Country} {
					if field != nil && strings.TrimSpace(api.StringValue(field)) == "" {
						return errors.New("fallback erased an address component")
					}
				}
			}
		}
		*result = api.Customer{ID: "customer", Name: "Known Buyer", Email: "buyer@example.test", Metadata: map[string]string{"tenant_id": "tenant"}}
		return nil
	case *api.TaxRate:
		p := params.(*api.TaxRateParams)
		if api.Float64Value(p.Percentage) != 21 || api.BoolValue(p.Inclusive) {
			return errors.New("fallback used another tax rate")
		}
		*result = api.TaxRate{ID: "vat21", Country: "NL", Percentage: 21}
		return nil
	}
	return errors.New("unexpected recurring fallback provider call " + path)
}

func TestIncompleteRecurringProfileUsesDutchVATWithoutZeroTaxClaim(t *testing.T) {
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("-- name: GetCryptoDocumentBillingProfile").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow("buyer@example.test", "Buyer", "Buyer B.V.", []byte(`{"street":" ","city":"Leiden","postal_code":"2332ED","country":"NL"}`), "unverified VAT claim", "business"))
	stored := &billingstripe.TaxProfile{}
	mock.ExpectQuery("-- name: GetEffectiveVATRate").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("-- name: StoreProviderTaxProfile").WithArgs("tenant", sqlmock.AnyArg(), retainedUpgradeTaxArg{profile: stored}).WillReturnResult(sqlmock.NewResult(0, 1))
	profile, err := AssessRecurringProviderTaxProfile(t.Context(), db, "tenant", "")
	if err != nil {
		t.Fatalf("incomplete recurring profile did not receive Dutch VAT fallback: %v", err)
	}
	if !profile.IncompleteProfile || profile.RateBPS != 2100 || profile.ReverseCharge || profile.VATNumber != "" || profile.Street != "" {
		t.Fatalf("fallback invented complete identity or a zero-tax claim: %+v", profile)
	}
	var proof CryptoTaxAssessment
	if err = json.Unmarshal(profile.Evidence, &proof); err != nil || !proof.ReviewRequired || len(proof.ReviewReasons) != 1 || proof.ReviewReasons[0] != incompleteRecurringProfileReview || !strings.Contains(string(proof.ClaimedProfile), "unverified VAT claim") {
		t.Fatalf("fallback lost its actionable review or original VAT claim: %s err=%v", profile.Evidence, err)
	}
	if stored.Fingerprint() != profile.Fingerprint() {
		t.Fatal("fallback terms were not retained before provider sync")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedStructuredRecurringAddressRetainsClaimWithoutInventingFields(t *testing.T) {
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden NL", "SUPPLIER_VAT_NUMBER": "NL853319145B01", "SUPPLIER_REGISTRATION_NUMBER": "59104694", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("-- name: GetCryptoDocumentBillingProfile").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "billing_address", "tax_id", "customer_type"}).AddRow("buyer@example.test", "Buyer", "", []byte(`["not","a billing object"]`), "unverified", "business"))
	mock.ExpectQuery("-- name: GetEffectiveVATRate").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("-- name: StoreProviderTaxProfile").WillReturnResult(sqlmock.NewResult(0, 1))
	profile, err := AssessRecurringProviderTaxProfile(t.Context(), db, "tenant", "")
	if err != nil || !profile.IncompleteProfile || profile.RateBPS != 2100 || profile.Country != "" || profile.Street != "" || profile.City != "" || profile.PostalCode != "" {
		t.Fatalf("malformed address invented identity or blocked recurring VAT: %+v %v", profile, err)
	}
	if !strings.Contains(string(profile.Evidence), `["not","a billing object"]`) {
		t.Fatalf("malformed structured claim was discarded: %s", profile.Evidence)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

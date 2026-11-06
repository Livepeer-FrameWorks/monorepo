//go:build schema_verify

package grpc

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
)

func TestBillingDetailsNormalizeProvidedFieldsWithoutReplacingOthers_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	tenant, tier := uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name) VALUES($1,'normalization','Normalization')`, tier); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,billing_model,status,presentment_currency,billing_customer_type,billing_name,billing_email,billing_company,billing_address,tax_id) VALUES($1,$2,'postpaid','active','EUR','business','Old Buyer','old@example.test','Old Company','{"street":"Old1","city":"Leiden","postal_code":"OldCode","country":"NL"}','NL853319145B01')`, tenant, tier); err != nil {
		t.Fatal(err)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger()}
	name, company, email, vat, kind := " Buyer ", " Company ", " buyer@example.test ", " nl 853319145b01 ", " business "
	req := &purserpb.UpdateBillingDetailsRequest{TenantId: tenant, Name: &name, Company: &company, Email: &email, VatNumber: &vat, CustomerType: &kind, Address: &purserpb.BillingAddress{Street: " Main1 ", City: " Leiden ", State: " ZH ", PostalCode: " 2332ED ", Country: " nl "}}
	details, err := server.UpdateBillingDetails(t.Context(), req)
	if err != nil {
		t.Fatalf("billing whitespace was rejected instead of normalized: %v", err)
	}
	if details.Name != "Buyer" || details.Company != "Company" || details.Email != "buyer@example.test" || details.VatNumber != "NL853319145B01" || details.CustomerType != "business" || details.Address.Street != "Main1" || details.Address.City != "Leiden" || details.Address.State != "ZH" || details.Address.PostalCode != "2332ED" || details.Address.Country != "NL" {
		t.Fatalf("saved billing details remain noncanonical: %+v", details)
	}
	if req.GetName() != " Buyer " || req.GetAddress().GetCountry() != " nl " {
		t.Fatal("normalization mutated the caller's request")
	}
	newName := " New Buyer "
	details, err = server.UpdateBillingDetails(t.Context(), &purserpb.UpdateBillingDetailsRequest{TenantId: tenant, Name: &newName})
	if err != nil {
		t.Fatal(err)
	}
	if details.Name != "New Buyer" || details.Company != "Company" || details.Email != "buyer@example.test" || details.VatNumber != "NL853319145B01" || details.Address.Street != "Main1" {
		t.Fatalf("partial normalization replaced omitted fields: %+v", details)
	}
	nationalVAT := " 853319145b01 "
	details, err = server.UpdateBillingDetails(t.Context(), &purserpb.UpdateBillingDetailsRequest{TenantId: tenant, VatNumber: &nationalVAT})
	if err != nil || details.GetVatNumber() != "NL853319145B01" {
		t.Fatalf("partial VAT update ignored stored country: %+v %v", details, err)
	}
	consumer, clearVAT := " consumer ", " \t "
	details, err = server.UpdateBillingDetails(t.Context(), &purserpb.UpdateBillingDetailsRequest{TenantId: tenant, CustomerType: &consumer, VatNumber: &clearVAT})
	if err != nil || details.GetCustomerType() != "consumer" || details.GetVatNumber() != "" || details.GetName() != "New Buyer" || details.GetAddress().GetStreet() != "Main1" {
		t.Fatalf("explicit trimmed clear lost partial-update semantics: %+v %v", details, err)
	}
}

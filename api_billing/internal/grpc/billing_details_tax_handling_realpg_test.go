//go:build schema_verify

package grpc

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
)

func TestCompleteBillingProfileSaveStartsAssessedTax_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	tenant, tier := uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name) VALUES($1,'tax-handling','Tax handling')`, tier); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,billing_model,status,presentment_currency,billing_email,billing_address,stripe_customer_id,tax_assessed_since) VALUES($1,$2,'postpaid','active','USD','buyer@example.ca','{"city":"Toronto","country":"CA"}','cus_existing',NULL)`, tenant, tier); err != nil {
		t.Fatal(err)
	}
	assessed := func() bool {
		t.Helper()
		var started bool
		if err := db.QueryRow(`SELECT tax_assessed_since IS NOT NULL FROM purser.tenant_subscriptions WHERE tenant_id=$1::uuid`, tenant).Scan(&started); err != nil {
			t.Fatal(err)
		}
		return started
	}
	server := &PurserServer{db: db, logger: logging.NewLogger()}
	email := "buyer@example.com"
	if _, err := server.UpdateBillingDetails(t.Context(), &purserpb.UpdateBillingDetailsRequest{TenantId: tenant, Email: &email}); err != nil {
		t.Fatal(err)
	}
	if assessed() {
		t.Fatal("an incomplete billing profile save started assessed tax handling")
	}
	name := "Buyer"
	if _, err := server.UpdateBillingDetails(t.Context(), &purserpb.UpdateBillingDetailsRequest{TenantId: tenant, Name: &name, Address: &purserpb.BillingAddress{Street: "1 Congress Ave", City: "Austin", PostalCode: "78701", Country: "US"}}); err != nil {
		t.Fatal(err)
	}
	if !assessed() {
		t.Fatal("a complete billing profile save left the relationship on its untaxed treatment")
	}
	var fresh bool
	other := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,billing_model,status) VALUES($1,$2,'prepaid','active')`, other, tier); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT tax_assessed_since IS NOT NULL FROM purser.tenant_subscriptions WHERE tenant_id=$1::uuid`, other).Scan(&fresh); err != nil || !fresh {
		t.Fatalf("a relationship created after the release keeps the untaxed treatment: assessed=%v err=%v", fresh, err)
	}
}

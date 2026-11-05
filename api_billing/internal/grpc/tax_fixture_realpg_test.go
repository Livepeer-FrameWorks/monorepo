//go:build schema_verify

package grpc

import (
	"database/sql"
	"testing"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
)

func configureRPCTaxSupplier(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{"SUPPLIER_NAME": "DDVTech B.V.", "SUPPLIER_ADDRESS": "Leiden, NL", "SUPPLIER_VAT_NUMBER": "NL123456789B01", "SUPPLIER_REGISTRATION_NUMBER": "12345678", "SUPPLIER_COUNTRY": "NL"} {
		appconfigtest.Set(t, key, value)
	}
}

// Pricing and phase fixtures without a specified buyer use an identified
// Canadian business; explicit billing countries remain part of each test.
func seedRPCTaxBuyer(t *testing.T, db *sql.DB, tenant string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions
 SET billing_name=COALESCE(NULLIF(billing_name,''),'Contract business'),billing_email=COALESCE(NULLIF(billing_email,''),'billing@example.test'),
 billing_customer_type=CASE WHEN billing_customer_type='unspecified' THEN 'business' ELSE billing_customer_type END,
 billing_address=jsonb_build_object('street','Main1','city','Toronto','postal_code','M5V1A1','country','CA')||COALESCE(billing_address,'{}'::jsonb)
 WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
}

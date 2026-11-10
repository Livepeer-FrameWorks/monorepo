package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

// Saving a profile that completes the billing identity starts assessed tax
// handling in the same transaction as the profile write.
func TestCompleteBillingProfileSaveStartsAssessedTaxHandling(t *testing.T) {
	s, mock := newReadServer(t, true)
	const tenantID = "90000000-0000-4000-8000-000000000010"
	mock.ExpectBegin()
	mock.ExpectQuery(`-- name: LockTenantPresentmentCurrency`).WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"presentment_currency", "presentment_locked"}).AddRow("USD", false))
	mock.ExpectQuery(`-- name: GetTenantBillingDetailsForUpdate`).WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "tax_id", "billing_address", "customer_type"}).
			AddRow("buyer@example.com", "", "", "", []byte(`{"city":"Toronto","country":"CA"}`), "unspecified"))
	mock.ExpectExec(`-- name: UpdateTenantBillingDetails`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`-- name: StartAssessedTaxHandling`).WithArgs(tenantID).WillReturnResult(sqlmock.NewResult(0, 1))
	expectDomainEvent(mock, "billing.details_updated", tenantID)
	mock.ExpectCommit()
	mock.ExpectQuery(`-- name: GetTenantBillingDetails`).WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "tax_id", "billing_address", "updated_at", "presentment_currency", "customer_type", "tax_review_required"}).
			AddRow("buyer@example.com", "Buyer", nil, nil, []byte(`{"street":"1 Congress Ave","city":"Austin","postal_code":"78701","country":"US"}`), time.Now(), "USD", "unspecified", false))
	name := "Buyer"
	if _, err := s.UpdateBillingDetails(context.Background(), &purserpb.UpdateBillingDetailsRequest{TenantId: tenantID, Name: &name, Address: &purserpb.BillingAddress{Street: "1 Congress Ave", City: "Austin", PostalCode: "78701", Country: "US"}}); err != nil {
		t.Fatalf("UpdateBillingDetails: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("complete profile save did not start assessed tax handling: %v", err)
	}
}

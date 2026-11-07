package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConsumerBillingProfileDoesNotRequireCompanyOrVAT(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`-- name: GetTenantBillingDetails`).WithArgs("tenant-1").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "tax_id", "billing_address", "updated_at", "presentment_currency", "customer_type", "tax_review_required"}).AddRow("person@example.test", "A Person", nil, nil, []byte(`{"street":"Street 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}`), time.Now(), "EUR", "consumer", false))
	got, err := (&PurserServer{db: db}).GetBillingDetails(context.Background(), &purserpb.GetBillingDetailsRequest{TenantId: "tenant-1"})
	if err != nil || !got.GetIsComplete() || got.GetCustomerType() != "consumer" || got.GetCompany() != "" || got.GetVatNumber() != "" {
		t.Fatalf("profile=%+v error=%v", got, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBillingCustomerTypeMustBeExplicitSupportedValue(t *testing.T) {
	for _, value := range []string{"", "unspecified", "exempt", "BUSINESS"} {
		_, err := (&PurserServer{}).UpdateBillingDetails(context.Background(), &purserpb.UpdateBillingDetailsRequest{TenantId: "tenant-1", CustomerType: &value})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("type=%q error=%v", value, err)
		}
	}
}

func TestChangingToConsumerCannotRetainBusinessVATClaim(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(`-- name: LockTenantPresentmentCurrency`).WithArgs("tenant-1").WillReturnRows(sqlmock.NewRows([]string{"presentment_currency", "presentment_locked"}).AddRow("EUR", false))
	mock.ExpectQuery(`-- name: GetTenantBillingDetailsForUpdate`).WithArgs("tenant-1").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "tax_id", "billing_address", "customer_type"}).AddRow("person@example.test", "A Person", "Company", "DE123456789", []byte(`{}`), "business"))
	mock.ExpectRollback()
	value := "consumer"
	_, err = (&PurserServer{db: db}).UpdateBillingDetails(context.Background(), &purserpb.UpdateBillingDetailsRequest{TenantId: "tenant-1", CustomerType: &value})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("error=%v", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

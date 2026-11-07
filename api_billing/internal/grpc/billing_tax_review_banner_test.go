package grpc

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func TestGetBillingDetailsShowsTaxReviewWithoutChangingCompleteness(t *testing.T) {
	for _, review := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleared", true: "open"}[review], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectQuery("-- name: GetTenantBillingDetails").WithArgs("tenant").
				WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "tax_id", "billing_address", "updated_at", "presentment_currency", "customer_type", "tax_review_required"}).
					AddRow("buyer@example.test", "Buyer", nil, "invalid", []byte(`{"street":"Main 1","city":"Vienna","postal_code":"1010","country":"AT"}`), time.Now(), "EUR", "business", review))
			details, err := (&PurserServer{db: db}).GetBillingDetails(t.Context(), &purserpb.GetBillingDetailsRequest{TenantId: "tenant"})
			if err != nil || !details.GetIsComplete() || details.GetTaxReviewRequired() != review {
				t.Fatalf("complete profile lost recorded tax review: details=%+v want_review=%v err=%v", details, review, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

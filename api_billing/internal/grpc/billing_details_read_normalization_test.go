package grpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func TestGetBillingDetailsNormalizesLegacyProfileBeforeCompleteness(t *testing.T) {
	for _, test := range []struct {
		name     string
		field    string
		value    string
		complete bool
	}{
		{name: "padded complete profile", complete: true},
		{name: "blank legal name", field: "name", value: " \t "},
		{name: "blank billing email", field: "email", value: " \t "},
		{name: "blank street", field: "street", value: " \t "},
		{name: "blank city", field: "city", value: " \t "},
		{name: "blank postal code", field: "postal_code", value: " \t "},
		{name: "blank country", field: "country", value: " \t "},
		{name: "invalid country", field: "country", value: " XX "},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			fields := map[string]string{"name": " Buyer ", "email": " buyer@example.test ", "street": " Main 1 ", "city": " Amsterdam ", "state": " NH ", "postal_code": " 1000 AA ", "country": " nl "}
			if test.field != "" {
				fields[test.field] = test.value
			}
			address, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			mock.ExpectQuery("-- name: GetTenantBillingDetails").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{"billing_email", "billing_name", "billing_company", "tax_id", "billing_address", "updated_at", "presentment_currency", "customer_type", "tax_review_required"}).AddRow(fields["email"], fields["name"], " Company ", " NL123456789B01 ", address, time.Now(), "EUR", "business", false))
			server := &PurserServer{db: db}
			details, err := server.GetBillingDetails(context.Background(), &purserpb.GetBillingDetailsRequest{TenantId: "tenant"})
			if err != nil {
				t.Fatal(err)
			}
			if details.GetIsComplete() != test.complete {
				t.Errorf("is_complete=%v, want %v for saved %s=%q", details.GetIsComplete(), test.complete, test.field, test.value)
			}
			if test.complete && (details.Name != "Buyer" || details.Email != "buyer@example.test" || details.Company != "Company" || details.VatNumber != "NL123456789B01" || details.Address.Street != "Main 1" || details.Address.City != "Amsterdam" || details.Address.State != "NH" || details.Address.PostalCode != "1000 AA" || details.Address.Country != "NL") {
				t.Errorf("returned profile was not normalized: %+v", details)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"
)

func TestCreateSubscription_PersistsUUIDAndBillingModel(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	server := &PurserServer{db: mockDB, logger: logging.NewLogger()}
	tenantID := "tenant-1"
	tierID := "11111111-1111-1111-1111-111111111111"

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM purser\.billing_tiers`).
		WithArgs(tierID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(`INSERT INTO purser\.tenant_subscriptions`).
		WithArgs(
			sqlmock.AnyArg(), tenantID, tierID, "billing@example.com", "prepaid",
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), "card", sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	resp, err := server.CreateSubscription(context.Background(), &pb.CreateSubscriptionRequest{
		TenantId:      tenantID,
		TierId:        tierID,
		BillingEmail:  "billing@example.com",
		PaymentMethod: "card",
		BillingModel:  "prepaid",
		CustomFeatures: &pb.BillingFeatures{
			Recording: true,
		},
	})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if _, err := uuid.Parse(resp.Id); err != nil {
		t.Fatalf("subscription id = %q, want UUID: %v", resp.Id, err)
	}
	if resp.BillingModel != "prepaid" {
		t.Errorf("BillingModel = %q, want prepaid", resp.BillingModel)
	}
	if resp.CustomFeatures == nil || !resp.CustomFeatures.Recording {
		t.Errorf("CustomFeatures not preserved on response: %+v", resp.CustomFeatures)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestValidatePricingOverrideRule(t *testing.T) {
	valid := &pb.PricingRule{
		Meter:            "delivered_minutes",
		Model:            "tiered_graduated",
		Currency:         "EUR",
		IncludedQuantity: "1000",
		UnitPrice:        "0.00055",
		ConfigJson:       "{}",
	}
	if err := validatePricingOverrideRule(valid); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}
	if err := validatePricingOverrideRule(&pb.PricingRule{Meter: "delivered_minutes", UnitPrice: "0.00042"}); err != nil {
		t.Fatalf("partial override rejected: %v", err)
	}

	cases := []*pb.PricingRule{
		{Meter: "egress_gb", Model: "tiered_graduated", Currency: "EUR", IncludedQuantity: "0", UnitPrice: "1", ConfigJson: "{}"},
		{Meter: "delivered_minutes", Model: "mystery", Currency: "EUR", IncludedQuantity: "0", UnitPrice: "1", ConfigJson: "{}"},
		{Meter: "delivered_minutes", Model: "tiered_graduated", Currency: "EURO", IncludedQuantity: "0", UnitPrice: "1", ConfigJson: "{}"},
		{Meter: "delivered_minutes", Model: "tiered_graduated", Currency: "EUR", IncludedQuantity: "not-decimal", UnitPrice: "1", ConfigJson: "{}"},
		{Meter: "delivered_minutes", Model: "tiered_graduated", Currency: "EUR", IncludedQuantity: "0", UnitPrice: "1", ConfigJson: "{bad"},
	}
	for _, tc := range cases {
		if err := validatePricingOverrideRule(tc); err == nil {
			t.Fatalf("invalid rule accepted: %+v", tc)
		}
	}
}

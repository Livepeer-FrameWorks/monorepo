package grpc

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func TestCreateSubscription_PersistsUUIDAndBillingModel(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	server := &PurserServer{db: mockDB, logger: logging.NewLogger()}
	tenantID := "81000000-0000-4000-8000-000000000001"
	tierID := "11111111-1111-1111-1111-111111111111"

	mock.ExpectQuery(`SELECT EXISTS \(\s+SELECT 1 FROM purser\.billing_tiers`).
		WithArgs(tierID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO purser\.tenant_subscriptions`).
		WithArgs(
			sqlmock.AnyArg(), tenantID, tierID, "billing@example.com", "prepaid",
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), "card", sqlmock.AnyArg(), "USD",
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	created := expectDomainEvent(mock, "billing.subscription_created", tenantID)
	mock.ExpectQuery(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs(sameEventID{created}, "subscription_created", tenantID, "", "subscription", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("22222222-2222-2222-2222-222222222222"))
	mock.ExpectCommit()

	resp, err := server.CreateSubscription(context.Background(), &purserpb.CreateSubscriptionRequest{
		TenantId:      tenantID,
		TierId:        tierID,
		BillingEmail:  "billing@example.com",
		PaymentMethod: "card",
		BillingModel:  "prepaid",
		CustomFeatures: &purserpb.BillingFeatures{
			ProcessingCustomizable: true,
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
	if resp.PresentmentCurrency != "USD" {
		t.Errorf("PresentmentCurrency = %q, want the inserted USD", resp.PresentmentCurrency)
	}
	if resp.CustomFeatures == nil || !resp.CustomFeatures.ProcessingCustomizable {
		t.Errorf("CustomFeatures not preserved on response: %+v", resp.CustomFeatures)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// capturedBytes records the driver value sqlmock receives for one argument.
type capturedBytes struct{ value []byte }

func (c *capturedBytes) Match(v driver.Value) bool {
	b, ok := v.([]byte)
	if !ok {
		return false
	}
	c.value = append([]byte(nil), b...)
	return true
}

func TestCreateSubscriptionStoresFeaturesThatReadBack(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	server := &PurserServer{db: mockDB, logger: logging.NewLogger()}
	tenantID := "81000000-0000-4000-8000-000000000001"
	tierID := "11111111-1111-1111-1111-111111111111"
	stored := &capturedBytes{}

	mock.ExpectQuery(`SELECT EXISTS \(\s+SELECT 1 FROM purser\.billing_tiers`).
		WithArgs(tierID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO purser\.tenant_subscriptions`).
		WithArgs(
			sqlmock.AnyArg(), tenantID, tierID, "billing@example.com", "postpaid",
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), "card", stored, "USD",
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	created := expectDomainEvent(mock, "billing.subscription_created", tenantID)
	mock.ExpectQuery(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs(sameEventID{created}, "subscription_created", tenantID, "", "subscription", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("22222222-2222-2222-2222-222222222222"))
	mock.ExpectCommit()

	if _, err := server.CreateSubscription(context.Background(), &purserpb.CreateSubscriptionRequest{
		TenantId:      tenantID,
		TierId:        tierID,
		BillingEmail:  "billing@example.com",
		PaymentMethod: "card",
		CustomFeatures: &purserpb.BillingFeatures{
			SupportLevel:           "dedicated",
			Sla:                    true,
			ProcessingCustomizable: true,
		},
	}); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}

	got := scanBillingFeatures(stored.value)
	if got == nil || got.GetSupportLevel() != "dedicated" || !got.GetSla() || !got.GetProcessingCustomizable() {
		t.Fatalf("stored custom_features %s read back as %+v", stored.value, got)
	}
	var keys map[string]any
	if err := json.Unmarshal(stored.value, &keys); err != nil {
		t.Fatalf("stored custom_features is not JSON: %v", err)
	}
	for key := range keys {
		switch key {
		case "support_level", "sla", "processing_customizable":
		default:
			t.Errorf("stored custom_features carries unsupported key %q: %s", key, stored.value)
		}
	}
}

func TestValidatePricingOverrideRule(t *testing.T) {
	valid := &purserpb.PricingRule{
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
	if err := validatePricingOverrideRule(&purserpb.PricingRule{Meter: "delivered_minutes", UnitPrice: "0.00042"}); err != nil {
		t.Fatalf("partial override rejected: %v", err)
	}
	if err := validatePricingOverrideRule(&purserpb.PricingRule{Meter: "egress_gb", Model: "tiered_graduated", Currency: "EUR", IncludedQuantity: "0", UnitPrice: "1", ConfigJson: "{}"}); err != nil {
		t.Fatalf("priceable egress rule rejected: %v", err)
	}

	cases := []*purserpb.PricingRule{
		{Meter: "Bad-Meter", Model: "tiered_graduated", Currency: "EUR", IncludedQuantity: "0", UnitPrice: "1", ConfigJson: "{}"},
		{Meter: "delivered_minutes", Model: "mystery", Currency: "EUR", IncludedQuantity: "0", UnitPrice: "1", ConfigJson: "{}"},
		{Meter: "delivered_minutes", Model: "tiered_graduated", Currency: "EURO", IncludedQuantity: "0", UnitPrice: "1", ConfigJson: "{}"},
		{Meter: "delivered_minutes", Model: "tiered_graduated", Currency: "EUR", IncludedQuantity: "not-decimal", UnitPrice: "1", ConfigJson: "{}"},
		{Meter: "delivered_minutes", Model: "tiered_graduated", Currency: "EUR", IncludedQuantity: "0", UnitPrice: "-1", ConfigJson: "{}"},
		{Meter: "transcode_rendition_seconds", Model: "dimensioned", Currency: "EUR", IncludedQuantity: "0", UnitPrice: "1", ConfigJson: `{"rates":"invalid"}`},
		{Meter: "delivered_minutes", Model: "tiered_graduated", Currency: "EUR", IncludedQuantity: "0", UnitPrice: "1", ConfigJson: "{bad"},
	}
	for _, tc := range cases {
		if err := validatePricingOverrideRule(tc); err == nil {
			t.Fatalf("invalid rule accepted: %+v", tc)
		}
	}
}

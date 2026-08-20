package resources

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func decodeResource[T any](t *testing.T, resultText string) T {
	t.Helper()
	var value T
	if err := json.Unmarshal([]byte(resultText), &value); err != nil {
		t.Fatalf("decode resource: %v", err)
	}
	return value
}

func TestBillingBalanceExposesSettledReservedAndAvailable(t *testing.T) {
	purser := &clientstest.FakePurser{
		GetTenantBillingStatusFn: func(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
			return &purserpb.GetTenantBillingStatusResponse{BillingModel: "prepaid"}, nil
		},
		GetBillingDetailsFn: func(context.Context, string) (*purserpb.BillingDetails, error) {
			return &purserpb.BillingDetails{IsComplete: true}, nil
		},
		GetPrepaidBalanceFn: func(context.Context, string, string) (*purserpb.PrepaidBalance, error) {
			return &purserpb.PrepaidBalance{
				BalanceCents: 1000, ReservedBalanceCents: 350, AvailableBalanceCents: 650,
				Currency: "EUR", LowBalanceThresholdCents: 500,
			}, nil
		},
	}
	periscopeClient := &clientstest.FakePeriscope{
		GetLiveUsageSummaryFn: func(context.Context, string, *periscope.TimeRangeOpts) (*periscopepb.GetLiveUsageSummaryResponse, error) {
			return nil, errors.New("analytics unavailable")
		},
	}
	clients := clientstest.Clients(clientstest.WithPurser(purser), clientstest.WithPeriscope(periscopeClient))
	result, err := handleBillingBalance(clientstest.AuthedCtx("tenant-1"), clients, clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	info := decodeResource[BalanceInfo](t, result.Contents[0].Text)
	if info.BalanceCents != 1000 || info.ReservedBalanceCents != 350 || info.AvailableBalanceCents != 650 {
		t.Fatalf("balance split = %+v", info)
	}
}

func TestBillingPricingPreservesMeterCatalogAndDimensionedRule(t *testing.T) {
	purser := &clientstest.FakePurser{
		GetBillingTiersFn: func(context.Context, bool, *commonpb.CursorPaginationRequest) (*purserpb.GetBillingTiersResponse, error) {
			return &purserpb.GetBillingTiersResponse{Tiers: []*purserpb.BillingTier{{
				Id: "tier-1", TierName: "payg", DisplayName: "Pay As You Go", Currency: "EUR", IsActive: true,
				PricingRules: []*purserpb.PricingRule{{
					Meter: "transcode_rendition_seconds", Model: "dimensioned", Currency: "EUR",
					IncludedQuantity: "0", UnitPrice: "0", ConfigJson: `{"rates":[{"selectors":{"output_codec":"av1"},"unit_price":"0.02"}]}`,
				}},
			}}}, nil
		},
		ListMeterDefinitionsFn: func(context.Context) (*purserpb.ListMeterDefinitionsResponse, error) {
			return &purserpb.ListMeterDefinitionsResponse{Meters: []*purserpb.MeterDefinition{
				{Meter: "api_requests", Unit: "request", Aggregation: "sum", DisplayName: "API requests", AllowedDimensions: []string{"service"}},
				{Meter: "transcode_rendition_seconds", Unit: "second", Aggregation: "sum", DisplayName: "Transcode renditions", AllowedDimensions: []string{"output_codec"}, DefaultPriceable: true},
			}}, nil
		},
		GetSubscriptionFn: func(context.Context, string) (*purserpb.GetSubscriptionResponse, error) {
			return &purserpb.GetSubscriptionResponse{Subscription: &purserpb.TenantSubscription{TierId: "tier-1"}}, nil
		},
	}
	result, err := handleBillingPricing(clientstest.AuthedCtx("tenant-1"), clientstest.Clients(clientstest.WithPurser(purser)))
	if err != nil {
		t.Fatal(err)
	}
	info := decodeResource[PricingInfo](t, result.Contents[0].Text)
	if info.Resources["api_requests"].Configured {
		t.Fatal("unpriced API meter must remain discoverable without pretending it has a tier rule")
	}
	transcode := info.Resources["transcode_rendition_seconds"]
	if !transcode.Configured || transcode.Unit != "second" || transcode.Model != "dimensioned" || transcode.Config == nil {
		t.Fatalf("dimensioned pricing = %+v", transcode)
	}
}

func TestUsageAnalyticsUsesPurserItemizedUsage(t *testing.T) {
	periodStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	dimensions, err := structpb.NewStruct(map[string]any{"service": "bridge", "operation_type": "query"})
	if err != nil {
		t.Fatal(err)
	}
	purser := &clientstest.FakePurser{
		GetSubscriptionFn: func(context.Context, string) (*purserpb.GetSubscriptionResponse, error) {
			return &purserpb.GetSubscriptionResponse{Subscription: &purserpb.TenantSubscription{
				BillingPeriodStart: timestamppb.New(periodStart), BillingPeriodEnd: timestamppb.New(periodEnd),
			}}, nil
		},
		GetTenantUsageFn: func(_ context.Context, _, startDate, endDate string) (*purserpb.TenantUsageResponse, error) {
			if startDate != "2026-08-01" || endDate == "" {
				t.Fatalf("usage period = %s..%s", startDate, endDate)
			}
			return &purserpb.TenantUsageResponse{
				BillingPeriod: "2026-08-01 to 2026-08-31", Currency: "EUR", UsageAmount: "0", BaseAmount: "0",
				Usage: map[string]float64{"api_requests": 42}, Costs: map[string]float64{"api_requests": 0},
				LineItems: []*purserpb.LineItem{{
					LineKey: "meter:api_requests:_source:202608", Meter: "api_requests", Description: "API requests",
					Quantity: "42", BillableQuantity: "42", UnitPrice: "0", Total: "0", Currency: "EUR",
					Unit: "request", Dimensions: dimensions, PricingSource: "tier",
				}},
			}, nil
		},
	}
	result, err := handleUsageAnalytics(clientstest.AuthedCtx("tenant-1"), clientstest.Clients(clientstest.WithPurser(purser)), clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	usage := decodeResource[UsageAnalytics](t, result.Contents[0].Text)
	if len(usage.LineItems) != 1 || usage.LineItems[0].Unit != "request" || usage.LineItems[0].Dimensions["service"] != "bridge" {
		t.Fatalf("itemized usage = %+v", usage)
	}
}

func TestInvoiceResourcePreservesPermanentLineSnapshot(t *testing.T) {
	dimensions, err := structpb.NewStruct(map[string]any{"output_codec": "av1"})
	if err != nil {
		t.Fatal(err)
	}
	purser := &clientstest.FakePurser{
		GetInvoiceFn: func(context.Context, string) (*purserpb.GetInvoiceResponse, error) {
			return &purserpb.GetInvoiceResponse{Invoice: &purserpb.Invoice{
				Id: "inv-1", Status: "paid", Currency: "EUR",
				LineItems: []*purserpb.LineItem{{
					LineKey: "meter:transcode_rendition_seconds:cluster-1:202608", Meter: "transcode_rendition_seconds",
					Quantity: "60", BillableQuantity: "60", UnitPrice: "0.02", Total: "1.20", Currency: "EUR",
					Unit: "second", Dimensions: dimensions, ClusterId: "cluster-1", PricingSource: "cluster_metered",
				}},
			}}, nil
		},
	}
	result, err := handleBillingInvoice(clientstest.AuthedCtx("tenant-1"), "billing://invoices/inv-1", clientstest.Clients(clientstest.WithPurser(purser)), clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	invoice := decodeResource[InvoiceInfo](t, result.Contents[0].Text)
	if invoice.Status != "paid" || len(invoice.LineItems) != 1 || invoice.LineItems[0].Dimensions["output_codec"] != "av1" {
		t.Fatalf("invoice = %+v", invoice)
	}
}

func TestBillingPaymentsUsesPaginatedTenantPaymentAPI(t *testing.T) {
	now := timestamppb.Now()
	purser := &clientstest.FakePurser{
		GetBillingStatusFn: func(context.Context, string) (*purserpb.BillingStatusResponse, error) {
			return &purserpb.BillingStatusResponse{OutstandingAmount: 12.5, Currency: "EUR", PaymentMethods: []string{"card"}}, nil
		},
		ListPaymentsFn: func(_ context.Context, req *purserpb.ListPaymentsRequest) (*purserpb.ListPaymentsResponse, error) {
			if req.GetTenantId() != "tenant-1" || req.GetPagination().GetFirst() != 50 {
				t.Fatalf("request = %+v", req)
			}
			return &purserpb.ListPaymentsResponse{
				Payments: []*purserpb.Payment{{
					Id: "pay-1", InvoiceId: "inv-1", Method: "card", Amount: 12.5,
					Currency: "EUR", Status: "failed", CreatedAt: now, UpdatedAt: now,
				}},
				Pagination: &commonpb.CursorPaginationResponse{HasNextPage: true, TotalCount: 51},
			}, nil
		},
	}
	result, err := handleBillingPayments(clientstest.AuthedCtx("tenant-1"), clientstest.Clients(clientstest.WithPurser(purser)), clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	payments := decodeResource[PaymentsResponse](t, result.Contents[0].Text)
	if len(payments.Payments) != 1 || payments.Payments[0].ID != "pay-1" || !payments.HasMore {
		t.Fatalf("payments = %+v", payments)
	}
}

func TestBillingPaymentDetailUsesTenantScopedGet(t *testing.T) {
	purser := &clientstest.FakePurser{
		GetPaymentFn: func(context.Context, string) (*purserpb.Payment, error) {
			return &purserpb.Payment{Id: "pay-1", InvoiceId: "inv-1", Method: "card", Currency: "EUR", Status: "confirmed"}, nil
		},
	}
	result, err := handleBillingPayment(clientstest.AuthedCtx("tenant-1"), "billing://payments/pay-1", clientstest.Clients(clientstest.WithPurser(purser)), clientstest.DiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	payment := decodeResource[PaymentInfo](t, result.Contents[0].Text)
	if payment.ID != "pay-1" || payment.InvoiceID != "inv-1" || payment.Status != "confirmed" {
		t.Fatalf("payment = %+v", payment)
	}
}

func TestBillingDocumentsListAndDownloadAreTenantScoped(t *testing.T) {
	now := timestamppb.Now()
	purser := &clientstest.FakePurser{
		ListBillingDocumentsFn: func(_ context.Context, tenantID string) (*purserpb.ListBillingDocumentsResponse, error) {
			if tenantID != "tenant-1" {
				t.Fatalf("tenant = %q", tenantID)
			}
			return &purserpb.ListBillingDocumentsResponse{Documents: []*purserpb.BillingDocument{{
				Id: "doc-1", Kind: "credit_note", DocumentNumber: "CN-1", AmountCents: 2500,
				Currency: "EUR", Status: "issued", IssuedAt: now, RetentionUntil: now,
			}}}, nil
		},
		GetBillingDocumentFn: func(_ context.Context, tenantID, kind, documentID string) (*purserpb.GetBillingDocumentResponse, error) {
			if tenantID != "tenant-1" || kind != "credit_note" || documentID != "doc-1" {
				t.Fatalf("download request = %q %q %q", tenantID, kind, documentID)
			}
			return &purserpb.GetBillingDocumentResponse{
				ContentType: "text/html; charset=utf-8", Content: []byte("<html>credit note</html>"), Sha256: "digest",
			}, nil
		},
	}
	clients := clientstest.Clients(clientstest.WithPurser(purser))
	list, err := handleBillingDocuments(clientstest.AuthedCtx("tenant-1"), clients)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeResource[struct {
		Documents []BillingDocumentInfo `json:"documents"`
	}](t, list.Contents[0].Text)
	if len(envelope.Documents) != 1 || envelope.Documents[0].DownloadURI != "billing://documents/credit_note/doc-1" {
		t.Fatalf("documents = %+v", envelope.Documents)
	}
	download, err := handleBillingDocument(clientstest.AuthedCtx("tenant-1"), envelope.Documents[0].DownloadURI, clients)
	if err != nil {
		t.Fatal(err)
	}
	if download.Contents[0].Text != "<html>credit note</html>" || download.Contents[0].Meta["sha256"] != "digest" {
		t.Fatalf("download = %+v", download.Contents[0])
	}
}

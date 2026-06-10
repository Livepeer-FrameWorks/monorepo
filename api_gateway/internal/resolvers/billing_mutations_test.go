package resolvers

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ============================================================================
// USAGE READ RESOLVERS (Purser-backed; the pure bucketing helpers are covered
// in billing_aggregates_test.go — these pin the resolver wiring + guards).
// ============================================================================

// DoGetUsageRecords forwards the tenant and a default 30d window to Purser with
// First=500, and returns the records verbatim.
func TestDoGetUsageRecords(t *testing.T) {
	var gotTenant string
	var gotPage *commonpb.CursorPaginationRequest
	r := purserResolver(&clientstest.FakePurser{
		GetUsageRecordsFn: func(_ context.Context, tenantID, _, _ string, tr *commonpb.TimeRange, p *commonpb.CursorPaginationRequest) (*purserpb.UsageRecordsResponse, error) {
			gotTenant, gotPage = tenantID, p
			if tr == nil || tr.Start == nil || tr.End == nil {
				t.Error("default time range not built")
			}
			return &purserpb.UsageRecordsResponse{UsageRecords: []*purserpb.UsageRecord{{Id: "u1"}}}, nil
		},
	})
	recs, err := r.DoGetUsageRecords(clientstest.AuthedCtx("t1"), nil)
	if err != nil || len(recs) != 1 || recs[0].Id != "u1" {
		t.Fatalf("DoGetUsageRecords = (%+v, %v)", recs, err)
	}
	if gotTenant != "t1" {
		t.Errorf("tenant not forwarded: %q", gotTenant)
	}
	if gotPage == nil || gotPage.First != 500 {
		t.Errorf("usage records must request First=500, got %+v", gotPage)
	}

	guard := &clientstest.FakePurser{}
	if _, err := purserResolver(guard).DoGetUsageRecords(context.Background(), nil); err == nil {
		t.Fatal("missing tenant should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("backend consulted without tenant: %d calls", guard.Calls)
	}

	failing := purserResolver(&clientstest.FakePurser{
		GetUsageRecordsFn: func(context.Context, string, string, string, *commonpb.TimeRange, *commonpb.CursorPaginationRequest) (*purserpb.UsageRecordsResponse, error) {
			return nil, errors.New("purser down")
		},
	})
	if _, err := failing.DoGetUsageRecords(clientstest.AuthedCtx("t1"), nil); err == nil {
		t.Fatal("backend error should propagate")
	}
}

// DoGetUsageAggregates forwards granularity + usage-type filters and the
// explicit time range to Purser, returning the aggregates verbatim.
func TestDoGetUsageAggregates(t *testing.T) {
	var gotGranularity string
	var gotTypes []string
	r := purserResolver(&clientstest.FakePurser{
		GetUsageAggregatesFn: func(_ context.Context, tenantID string, tr *commonpb.TimeRange, granularity string, usageTypes []string) (*purserpb.GetUsageAggregatesResponse, error) {
			if tenantID != "t1" {
				t.Errorf("tenant not forwarded: %q", tenantID)
			}
			gotGranularity, gotTypes = granularity, usageTypes
			if tr == nil {
				t.Error("time range not built")
			}
			return &purserpb.GetUsageAggregatesResponse{Aggregates: []*purserpb.UsageAggregate{{UsageType: "egress_gb"}}}, nil
		},
	})
	tr := &model.TimeRangeInput{Start: time.Now().Add(-time.Hour), End: time.Now()}
	aggs, err := r.DoGetUsageAggregates(clientstest.AuthedCtx("t1"), tr, "daily", []string{"egress_gb"})
	if err != nil || len(aggs) != 1 || aggs[0].UsageType != "egress_gb" {
		t.Fatalf("DoGetUsageAggregates = (%+v, %v)", aggs, err)
	}
	if gotGranularity != "daily" || len(gotTypes) != 1 || gotTypes[0] != "egress_gb" {
		t.Errorf("granularity/types not forwarded: %q %v", gotGranularity, gotTypes)
	}

	guard := &clientstest.FakePurser{}
	if _, err := purserResolver(guard).DoGetUsageAggregates(context.Background(), nil, "daily", nil); err == nil {
		t.Fatal("missing tenant should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("backend consulted without tenant: %d calls", guard.Calls)
	}
}

// DoGetTenantUsage converts an explicit time range to YYYY-MM-DD date strings and
// fans the proto usage/cost maps out into model array entries.
func TestDoGetTenantUsage(t *testing.T) {
	var gotStart, gotEnd string
	r := purserResolver(&clientstest.FakePurser{
		GetTenantUsageFn: func(_ context.Context, _, startDate, endDate string) (*purserpb.TenantUsageResponse, error) {
			gotStart, gotEnd = startDate, endDate
			return &purserpb.TenantUsageResponse{
				BillingPeriod: "2026-04",
				Usage:         map[string]float64{"egress_gb": 12},
				Costs:         map[string]float64{"egress_gb": 1.2},
				TotalCost:     1.2,
				Currency:      "EUR",
				BaseAmount:    "10.00",
				UsageAmount:   "1.20",
			}, nil
		},
	})
	tr := &model.TimeRangeInput{
		Start: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC),
	}
	got, err := r.DoGetTenantUsage(clientstest.AuthedCtx("t1"), tr)
	if err != nil || got == nil {
		t.Fatalf("DoGetTenantUsage = (%+v, %v)", got, err)
	}
	if gotStart != "2026-04-01" || gotEnd != "2026-04-30" {
		t.Errorf("date range not formatted: %q..%q", gotStart, gotEnd)
	}
	if got.BillingPeriod != "2026-04" || got.Currency != "EUR" || got.TotalCost != 1.2 {
		t.Errorf("scalar fields not mapped: %+v", got)
	}
	if len(got.Usage) != 1 || got.Usage[0].ResourceType != "egress_gb" || got.Usage[0].Amount != 12 {
		t.Errorf("usage map not fanned into entries: %+v", got.Usage)
	}
	if len(got.Costs) != 1 || got.Costs[0].Cost != 1.2 {
		t.Errorf("cost map not fanned into entries: %+v", got.Costs)
	}

	guard := &clientstest.FakePurser{}
	if _, err := purserResolver(guard).DoGetTenantUsage(context.Background(), nil); err == nil {
		t.Fatal("missing tenant should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("backend consulted without tenant: %d calls", guard.Calls)
	}
}

// DoGetUsageRecordsConnection forwards the window + pagination and wraps records
// in keyset-cursor edges, surfacing the backend HasNextPage + TotalCount.
func TestDoGetUsageRecordsConnection(t *testing.T) {
	ps := timestamppb.New(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	r := purserResolver(&clientstest.FakePurser{
		GetUsageRecordsFn: func(_ context.Context, tenantID, _, _ string, _ *commonpb.TimeRange, _ *commonpb.CursorPaginationRequest) (*purserpb.UsageRecordsResponse, error) {
			if tenantID != "t1" {
				t.Errorf("tenant not forwarded: %q", tenantID)
			}
			return &purserpb.UsageRecordsResponse{
				UsageRecords: []*purserpb.UsageRecord{{Id: "u1", PeriodStart: ps, CreatedAt: ps}},
				Pagination:   &commonpb.CursorPaginationResponse{HasNextPage: true, TotalCount: 7},
			}, nil
		},
	})
	first := 10
	conn, err := r.DoGetUsageRecordsConnection(clientstest.AuthedCtx("t1"), nil, &first, nil, nil, nil)
	if err != nil || conn == nil {
		t.Fatalf("DoGetUsageRecordsConnection = (%+v, %v)", conn, err)
	}
	if len(conn.Edges) != 1 || conn.Edges[0].Node.Id != "u1" || conn.Edges[0].Cursor == "" {
		t.Errorf("edge/cursor not built: %+v", conn.Edges)
	}
	if !conn.PageInfo.HasNextPage || conn.TotalCount != 7 {
		t.Errorf("pagination not surfaced: %+v / total=%d", conn.PageInfo, conn.TotalCount)
	}

	guard := &clientstest.FakePurser{}
	if _, err := purserResolver(guard).DoGetUsageRecordsConnection(context.Background(), nil, nil, nil, nil, nil); err == nil {
		t.Fatal("missing tenant should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("backend consulted without tenant: %d calls", guard.Calls)
	}
}

// ============================================================================
// DoGetLiveUsageSummary (Periscope-backed)
// ============================================================================

// periscopeResolverFor wires a FakePeriscope into a Resolver for the one
// Periscope-backed billing resolver.
func periscopeResolverFor(p *clientstest.FakePeriscope) *Resolver {
	return &Resolver{
		Clients: clientstest.Clients(clientstest.WithPeriscope(p)),
		Logger:  clientstest.DiscardLogger(),
	}
}

// DoGetLiveUsageSummary defaults period_start to the start of the current month
// and clamps period_end to now (never the future), returning the response's
// summary.
func TestDoGetLiveUsageSummary(t *testing.T) {
	var gotStart, gotEnd time.Time
	p := &clientstest.FakePeriscope{
		GetLiveUsageSummaryFn: func(_ context.Context, tenantID string, tr *periscope.TimeRangeOpts) (*periscopepb.GetLiveUsageSummaryResponse, error) {
			if tenantID != "t1" {
				t.Errorf("tenant not forwarded: %q", tenantID)
			}
			gotStart, gotEnd = tr.StartTime, tr.EndTime
			return &periscopepb.GetLiveUsageSummaryResponse{
				Summary: &periscopepb.LiveUsageSummary{TenantId: tenantID, EgressGb: 12.5},
			}, nil
		},
	}
	got, err := periscopeResolverFor(p).DoGetLiveUsageSummary(clientstest.AuthedCtx("t1"), nil, nil)
	if err != nil || got == nil || got.EgressGb != 12.5 {
		t.Fatalf("DoGetLiveUsageSummary = (%+v, %v)", got, err)
	}
	now := time.Now().UTC()
	wantStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	if !gotStart.Equal(wantStart) {
		t.Errorf("default period_start should be start of month, got %s", gotStart)
	}
	if gotEnd.After(now.Add(time.Minute)) {
		t.Errorf("period_end should be clamped to ~now, got %s", gotEnd)
	}

	// Explicit future period_end is clamped back to now.
	future := now.Add(48 * time.Hour)
	start := now.Add(-24 * time.Hour)
	var clampedEnd time.Time
	pClamp := &clientstest.FakePeriscope{
		GetLiveUsageSummaryFn: func(_ context.Context, _ string, tr *periscope.TimeRangeOpts) (*periscopepb.GetLiveUsageSummaryResponse, error) {
			clampedEnd = tr.EndTime
			return &periscopepb.GetLiveUsageSummaryResponse{Summary: &periscopepb.LiveUsageSummary{}}, nil
		},
	}
	if _, err := periscopeResolverFor(pClamp).DoGetLiveUsageSummary(clientstest.AuthedCtx("t1"), &start, &future); err != nil {
		t.Fatal(err)
	}
	if clampedEnd.After(now.Add(time.Minute)) {
		t.Errorf("future period_end not clamped: %s", clampedEnd)
	}

	// Missing tenant → error, no backend call.
	guard := &clientstest.FakePeriscope{}
	if _, err := periscopeResolverFor(guard).DoGetLiveUsageSummary(context.Background(), nil, nil); err == nil {
		t.Fatal("missing tenant should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("backend consulted without tenant: %d calls", guard.Calls)
	}

	failing := periscopeResolverFor(&clientstest.FakePeriscope{
		GetLiveUsageSummaryFn: func(context.Context, string, *periscope.TimeRangeOpts) (*periscopepb.GetLiveUsageSummaryResponse, error) {
			return nil, errors.New("periscope down")
		},
	})
	if _, err := failing.DoGetLiveUsageSummary(clientstest.AuthedCtx("t1"), nil, nil); err == nil {
		t.Fatal("backend error should propagate")
	}
}

// ============================================================================
// DoCreatePayment
// ============================================================================

// DoCreatePayment maps the GraphQL PaymentMethod enum to the Purser method string
// and forwards the invoice + return URL, returning the payment response.
func TestDoCreatePayment(t *testing.T) {
	var gotReq *purserpb.PaymentRequest
	r := purserResolver(&clientstest.FakePurser{
		CreatePaymentFn: func(_ context.Context, req *purserpb.PaymentRequest) (*purserpb.PaymentResponse, error) {
			gotReq = req
			return &purserpb.PaymentResponse{Id: "pay-1", Status: "pending", Method: req.Method, Amount: 5, Currency: "EUR"}, nil
		},
	})
	ret := "https://app/return"
	got, err := r.DoCreatePayment(clientstest.AuthedCtx("t1"), model.CreatePaymentInput{
		InvoiceID: "inv-1", Method: model.PaymentMethodCard, ReturnURL: &ret,
	})
	if err != nil || got == nil || got.Id != "pay-1" {
		t.Fatalf("DoCreatePayment = (%+v, %v)", got, err)
	}
	if gotReq.InvoiceId != "inv-1" || gotReq.Method != "card" || gotReq.ReturnUrl != "https://app/return" {
		t.Errorf("request not mapped from input: %+v", gotReq)
	}

	// Unsupported method is rejected before any tenant/backend work.
	badGuard := &clientstest.FakePurser{}
	if _, err := purserResolver(badGuard).DoCreatePayment(clientstest.AuthedCtx("t1"), model.CreatePaymentInput{
		InvoiceID: "inv-1", Method: model.PaymentMethod("WIRE"),
	}); err == nil {
		t.Fatal("unsupported payment method should error")
	}
	if badGuard.Calls != 0 {
		t.Fatalf("backend consulted for invalid method: %d calls", badGuard.Calls)
	}

	// Valid method but no tenant → tenant guard error, no backend call.
	guard := &clientstest.FakePurser{}
	if _, err := purserResolver(guard).DoCreatePayment(context.Background(), model.CreatePaymentInput{
		InvoiceID: "inv-1", Method: model.PaymentMethodCard,
	}); err == nil {
		t.Fatal("missing tenant should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("backend consulted without tenant: %d calls", guard.Calls)
	}

	failing := purserResolver(&clientstest.FakePurser{
		CreatePaymentFn: func(context.Context, *purserpb.PaymentRequest) (*purserpb.PaymentResponse, error) {
			return nil, errors.New("purser down")
		},
	})
	if _, err := failing.DoCreatePayment(clientstest.AuthedCtx("t1"), model.CreatePaymentInput{
		InvoiceID: "inv-1", Method: model.PaymentMethodCryptoUsdc,
	}); err == nil {
		t.Fatal("backend error should propagate")
	}
}

// ============================================================================
// DoSubmitX402Payment (x402 settlement seam)
// ============================================================================

// DoSubmitX402Payment short-circuits to a ValidationError for empty payloads and
// for a missing Purser client, never reaching settlement.
func TestDoSubmitX402Payment_Validation(t *testing.T) {
	r := purserResolver(&clientstest.FakePurser{})
	res, err := r.DoSubmitX402Payment(clientstest.AuthedCtx("t1"), "   ", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("empty payment should be a ValidationError, got %T", res)
	}

	// No Purser wired → ValidationError ("x402 settlement unavailable").
	noPurser := &Resolver{Clients: clientstest.Clients(), Logger: clientstest.DiscardLogger()}
	res, err = noPurser.DoSubmitX402Payment(clientstest.AuthedCtx("t1"), "anything", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("missing Purser should be a ValidationError, got %T", res)
	}

	// Unparseable X-PAYMENT header → settlement returns ErrInvalidPayment, which
	// the default arm maps to a ValidationError (not a Go error).
	res, err = r.DoSubmitX402Payment(clientstest.AuthedCtx("t1"), "not-base64-json", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("invalid header should be a ValidationError, got %T", res)
	}
}

// ============================================================================
// STRIPE
// ============================================================================

// DoCreateStripeCheckout forwards tier/period/URLs to Purser.CreateStripeCheckoutSession
// and returns a StripeCheckoutSession success union member; missing auth and
// backend errors map to AuthError / ValidationError (never a Go error).
func TestDoCreateStripeCheckout(t *testing.T) {
	var gotTier, gotPeriod, gotSuccess, gotCancel string
	r := purserResolver(&clientstest.FakePurser{
		CreateStripeCheckoutSessionFn: func(_ context.Context, _, tierID, billingPeriod, successURL, cancelURL string) (*purserpb.CreateStripeCheckoutResponse, error) {
			gotTier, gotPeriod, gotSuccess, gotCancel = tierID, billingPeriod, successURL, cancelURL
			return &purserpb.CreateStripeCheckoutResponse{SessionId: "cs_1", CheckoutUrl: "https://stripe/cs_1"}, nil
		},
	})
	res, err := r.DoCreateStripeCheckout(clientstest.AuthedCtx("t1"), "tier-pro", "monthly", "https://ok", "https://no")
	if err != nil {
		t.Fatal(err)
	}
	sess, ok := res.(*model.StripeCheckoutSession)
	if !ok || sess.SessionID != "cs_1" || sess.CheckoutURL != "https://stripe/cs_1" {
		t.Fatalf("expected StripeCheckoutSession, got %T %+v", res, res)
	}
	if gotTier != "tier-pro" || gotPeriod != "monthly" || gotSuccess != "https://ok" || gotCancel != "https://no" {
		t.Errorf("args not forwarded: %q %q %q %q", gotTier, gotPeriod, gotSuccess, gotCancel)
	}

	// Missing auth → AuthError union member, no backend call.
	authGuard := &clientstest.FakePurser{}
	res, _ = purserResolver(authGuard).DoCreateStripeCheckout(context.Background(), "t", "monthly", "", "")
	if _, ok := res.(*model.AuthError); !ok {
		t.Fatalf("missing auth should be AuthError, got %T", res)
	}
	if authGuard.Calls != 0 {
		t.Fatalf("backend consulted without auth: %d calls", authGuard.Calls)
	}

	// Backend error → ValidationError union member (not a Go error).
	failing := purserResolver(&clientstest.FakePurser{
		CreateStripeCheckoutSessionFn: func(context.Context, string, string, string, string, string) (*purserpb.CreateStripeCheckoutResponse, error) {
			return nil, errors.New("stripe boom")
		},
	})
	res, err = failing.DoCreateStripeCheckout(clientstest.AuthedCtx("t1"), "t", "monthly", "", "")
	if err != nil {
		t.Fatalf("backend error should be a union member, not a Go error: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("backend error should be ValidationError, got %T", res)
	}
}

// DoCreateStripeBillingPortal forwards the return URL and returns a portal session.
func TestDoCreateStripeBillingPortal(t *testing.T) {
	var gotReturn string
	r := purserResolver(&clientstest.FakePurser{
		CreateStripeBillingPortalFn: func(_ context.Context, _, returnURL string) (*purserpb.CreateBillingPortalResponse, error) {
			gotReturn = returnURL
			return &purserpb.CreateBillingPortalResponse{PortalUrl: "https://stripe/portal"}, nil
		},
	})
	res, err := r.DoCreateStripeBillingPortal(clientstest.AuthedCtx("t1"), "https://app/back")
	if err != nil {
		t.Fatal(err)
	}
	portal, ok := res.(*model.StripeBillingPortalSession)
	if !ok || portal.PortalURL != "https://stripe/portal" {
		t.Fatalf("expected StripeBillingPortalSession, got %T %+v", res, res)
	}
	if gotReturn != "https://app/back" {
		t.Errorf("return URL not forwarded: %q", gotReturn)
	}

	authGuard := &clientstest.FakePurser{}
	res, _ = purserResolver(authGuard).DoCreateStripeBillingPortal(context.Background(), "")
	if _, ok := res.(*model.AuthError); !ok {
		t.Fatalf("missing auth should be AuthError, got %T", res)
	}
	if authGuard.Calls != 0 {
		t.Fatalf("backend consulted without auth: %d calls", authGuard.Calls)
	}

	failing := purserResolver(&clientstest.FakePurser{
		CreateStripeBillingPortalFn: func(context.Context, string, string) (*purserpb.CreateBillingPortalResponse, error) {
			return nil, errors.New("boom")
		},
	})
	res, err = failing.DoCreateStripeBillingPortal(clientstest.AuthedCtx("t1"), "")
	if err != nil {
		t.Fatalf("backend error should be a union member: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("backend error should be ValidationError, got %T", res)
	}
}

// ============================================================================
// MOLLIE
// ============================================================================

// DoCreateMollieFirstPayment forwards tier/method/redirect and maps the response
// fields (payment_id, mollie_customer_id, payment_url) into the union member.
func TestDoCreateMollieFirstPayment(t *testing.T) {
	var gotTier, gotMethod, gotRedirect string
	r := purserResolver(&clientstest.FakePurser{
		CreateMollieFirstPaymentFn: func(_ context.Context, _, tierID, method, redirectURL string) (*purserpb.CreateMollieFirstPaymentResponse, error) {
			gotTier, gotMethod, gotRedirect = tierID, method, redirectURL
			return &purserpb.CreateMollieFirstPaymentResponse{PaymentId: "tr_1", MollieCustomerId: "cst_1", PaymentUrl: "https://mollie/tr_1"}, nil
		},
	})
	res, err := r.DoCreateMollieFirstPayment(clientstest.AuthedCtx("t1"), "tier-pro", "ideal", "https://app/return")
	if err != nil {
		t.Fatal(err)
	}
	fp, ok := res.(*model.MollieFirstPayment)
	if !ok || fp.PaymentID != "tr_1" || fp.CustomerID != "cst_1" || fp.PaymentURL != "https://mollie/tr_1" {
		t.Fatalf("expected MollieFirstPayment, got %T %+v", res, res)
	}
	if gotTier != "tier-pro" || gotMethod != "ideal" || gotRedirect != "https://app/return" {
		t.Errorf("args not forwarded: %q %q %q", gotTier, gotMethod, gotRedirect)
	}

	authGuard := &clientstest.FakePurser{}
	res, _ = purserResolver(authGuard).DoCreateMollieFirstPayment(context.Background(), "t", "ideal", "")
	if _, ok := res.(*model.AuthError); !ok {
		t.Fatalf("missing auth should be AuthError, got %T", res)
	}
	if authGuard.Calls != 0 {
		t.Fatalf("backend consulted without auth: %d calls", authGuard.Calls)
	}

	failing := purserResolver(&clientstest.FakePurser{
		CreateMollieFirstPaymentFn: func(context.Context, string, string, string, string) (*purserpb.CreateMollieFirstPaymentResponse, error) {
			return nil, errors.New("boom")
		},
	})
	res, err = failing.DoCreateMollieFirstPayment(clientstest.AuthedCtx("t1"), "t", "ideal", "")
	if err != nil {
		t.Fatalf("backend error should be a union member: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("backend error should be ValidationError, got %T", res)
	}
}

// DoCreateMollieSubscription forwards tier/mandate/description and maps the
// response; an empty next_payment_date leaves the pointer nil.
func TestDoCreateMollieSubscription(t *testing.T) {
	var gotTier, gotMandate, gotDesc string
	r := purserResolver(&clientstest.FakePurser{
		CreateMollieSubscriptionFn: func(_ context.Context, _, tierID, mandateID, description string) (*purserpb.CreateMollieSubscriptionResponse, error) {
			gotTier, gotMandate, gotDesc = tierID, mandateID, description
			return &purserpb.CreateMollieSubscriptionResponse{SubscriptionId: "sub_1", Status: "active", NextPaymentDate: "2026-07-01"}, nil
		},
	})
	desc := "Pro plan"
	res, err := r.DoCreateMollieSubscription(clientstest.AuthedCtx("t1"), "tier-pro", "mdt_1", &desc)
	if err != nil {
		t.Fatal(err)
	}
	sub, ok := res.(*model.MollieSubscription)
	if !ok || sub.SubscriptionID != "sub_1" || sub.Status != "active" {
		t.Fatalf("expected MollieSubscription, got %T %+v", res, res)
	}
	if sub.NextPaymentDate == nil || *sub.NextPaymentDate != "2026-07-01" {
		t.Errorf("next payment date not mapped: %v", sub.NextPaymentDate)
	}
	if gotTier != "tier-pro" || gotMandate != "mdt_1" || gotDesc != "Pro plan" {
		t.Errorf("args not forwarded: %q %q %q", gotTier, gotMandate, gotDesc)
	}

	// Empty next_payment_date → nil pointer.
	rEmpty := purserResolver(&clientstest.FakePurser{
		CreateMollieSubscriptionFn: func(context.Context, string, string, string, string) (*purserpb.CreateMollieSubscriptionResponse, error) {
			return &purserpb.CreateMollieSubscriptionResponse{SubscriptionId: "sub_2", Status: "pending"}, nil
		},
	})
	res, _ = rEmpty.DoCreateMollieSubscription(clientstest.AuthedCtx("t1"), "t", "m", nil)
	if sub := res.(*model.MollieSubscription); sub.NextPaymentDate != nil {
		t.Errorf("empty next payment date should be nil, got %v", *sub.NextPaymentDate)
	}

	authGuard := &clientstest.FakePurser{}
	res, _ = purserResolver(authGuard).DoCreateMollieSubscription(context.Background(), "t", "m", nil)
	if _, ok := res.(*model.AuthError); !ok {
		t.Fatalf("missing auth should be AuthError, got %T", res)
	}
	if authGuard.Calls != 0 {
		t.Fatalf("backend consulted without auth: %d calls", authGuard.Calls)
	}

	failing := purserResolver(&clientstest.FakePurser{
		CreateMollieSubscriptionFn: func(context.Context, string, string, string, string) (*purserpb.CreateMollieSubscriptionResponse, error) {
			return nil, errors.New("boom")
		},
	})
	res, err = failing.DoCreateMollieSubscription(clientstest.AuthedCtx("t1"), "t", "m", nil)
	if err != nil {
		t.Fatalf("backend error should be a union member: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("backend error should be ValidationError, got %T", res)
	}
}

// DoListMollieMandates returns the backend mandate list, or an empty slice when
// the response carries none.
func TestDoListMollieMandates(t *testing.T) {
	r := purserResolver(&clientstest.FakePurser{
		ListMollieMandatesFn: func(_ context.Context, tenantID string) (*purserpb.ListMollieMandatesResponse, error) {
			if tenantID != "t1" {
				t.Errorf("tenant not forwarded: %q", tenantID)
			}
			return &purserpb.ListMollieMandatesResponse{Mandates: []*purserpb.MollieMandate{{MollieMandateId: "mdt_1"}}}, nil
		},
	})
	mandates, err := r.DoListMollieMandates(clientstest.AuthedCtx("t1"))
	if err != nil || len(mandates) != 1 || mandates[0].MollieMandateId != "mdt_1" {
		t.Fatalf("DoListMollieMandates = (%+v, %v)", mandates, err)
	}

	// Nil mandate list → empty (non-nil) slice, not nil.
	rEmpty := purserResolver(&clientstest.FakePurser{
		ListMollieMandatesFn: func(context.Context, string) (*purserpb.ListMollieMandatesResponse, error) {
			return &purserpb.ListMollieMandatesResponse{}, nil
		},
	})
	mandates, err = rEmpty.DoListMollieMandates(clientstest.AuthedCtx("t1"))
	if err != nil || mandates == nil || len(mandates) != 0 {
		t.Fatalf("empty mandates should be a non-nil empty slice, got (%+v, %v)", mandates, err)
	}

	guard := &clientstest.FakePurser{}
	if _, err := purserResolver(guard).DoListMollieMandates(context.Background()); err == nil {
		t.Fatal("missing tenant should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("backend consulted without tenant: %d calls", guard.Calls)
	}
}

// ============================================================================
// CARD TOP-UP
// ============================================================================

// DoCreateCardTopup validates the amount bounds, maps the provider enum, defaults
// the currency, and passes optional billing-detail pointers straight through.
func TestDoCreateCardTopup(t *testing.T) {
	var gotReq *purserpb.CreateCardTopupRequest
	r := purserResolver(&clientstest.FakePurser{
		CreateCardTopupFn: func(_ context.Context, req *purserpb.CreateCardTopupRequest) (*purserpb.CreateCardTopupResponse, error) {
			gotReq = req
			return &purserpb.CreateCardTopupResponse{TopupId: "tp_1", CheckoutUrl: "https://co/tp_1", ExpiresAt: timestamppb.Now()}, nil
		},
	})
	email := "pay@ex.com"
	got, err := r.DoCreateCardTopup(clientstest.AuthedCtx("t1"), model.CreateCardTopupInput{
		AmountCents:  2500,
		Provider:     model.CardPaymentProviderStripe,
		SuccessURL:   "https://ok",
		CancelURL:    "https://no",
		BillingEmail: &email,
	})
	if err != nil || got == nil || got.TopupID != "tp_1" || got.CheckoutURL != "https://co/tp_1" {
		t.Fatalf("DoCreateCardTopup = (%+v, %v)", got, err)
	}
	if gotReq.TenantId != "t1" || gotReq.AmountCents != 2500 || gotReq.Provider != "stripe" {
		t.Errorf("request not mapped: %+v", gotReq)
	}
	if gotReq.Currency == "" {
		t.Error("currency should default, not be empty")
	}
	if gotReq.BillingEmail == nil || *gotReq.BillingEmail != "pay@ex.com" {
		t.Errorf("billing email pointer not passed through: %v", gotReq.BillingEmail)
	}

	// Out-of-range amount → error before backend.
	amtGuard := &clientstest.FakePurser{}
	if _, err := purserResolver(amtGuard).DoCreateCardTopup(clientstest.AuthedCtx("t1"), model.CreateCardTopupInput{
		AmountCents: 0, Provider: model.CardPaymentProviderStripe,
	}); err == nil {
		t.Fatal("zero amount should error")
	}
	if amtGuard.Calls != 0 {
		t.Fatalf("backend consulted for invalid amount: %d calls", amtGuard.Calls)
	}

	// Unsupported provider → error.
	provGuard := &clientstest.FakePurser{}
	if _, err := purserResolver(provGuard).DoCreateCardTopup(clientstest.AuthedCtx("t1"), model.CreateCardTopupInput{
		AmountCents: 2500, Provider: model.CardPaymentProvider("PAYPAL"),
	}); err == nil {
		t.Fatal("unsupported provider should error")
	}
	if provGuard.Calls != 0 {
		t.Fatalf("backend consulted for invalid provider: %d calls", provGuard.Calls)
	}

	guard := &clientstest.FakePurser{}
	if _, err := purserResolver(guard).DoCreateCardTopup(context.Background(), model.CreateCardTopupInput{
		AmountCents: 2500, Provider: model.CardPaymentProviderStripe,
	}); err == nil {
		t.Fatal("missing tenant should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("backend consulted without tenant: %d calls", guard.Calls)
	}
}

// ============================================================================
// CRYPTO TOP-UP
// ============================================================================

// DoCreateCryptoTopup validates amount + asset, defaults currency, forwards the
// proto enum, and maps the deposit/quote fields onto the result.
func TestDoCreateCryptoTopup(t *testing.T) {
	var gotReq *purserpb.CreateCryptoTopupRequest
	r := purserResolver(&clientstest.FakePurser{
		CreateCryptoTopupFn: func(_ context.Context, req *purserpb.CreateCryptoTopupRequest) (*purserpb.CreateCryptoTopupResponse, error) {
			gotReq = req
			return &purserpb.CreateCryptoTopupResponse{
				TopupId:             "ct_1",
				DepositAddress:      "0xabc",
				Asset:               req.Asset,
				AssetSymbol:         "ETH",
				ExpectedAmountCents: req.ExpectedAmountCents,
				ExpiresAt:           timestamppb.Now(),
				QuotedAt:            timestamppb.Now(),
				Network:             "arbitrum",
			}, nil
		},
	})
	got, err := r.DoCreateCryptoTopup(clientstest.AuthedCtx("t1"), model.CreateCryptoTopupInput{
		AmountCents: 5000, Asset: purserpb.CryptoAsset_CRYPTO_ASSET_ETH,
	})
	if err != nil || got == nil || got.TopupID != "ct_1" || got.DepositAddress != "0xabc" {
		t.Fatalf("DoCreateCryptoTopup = (%+v, %v)", got, err)
	}
	if got.Asset != purserpb.CryptoAsset_CRYPTO_ASSET_ETH || got.AssetSymbol != "ETH" || got.ExpectedAmountCents != 5000 {
		t.Errorf("result not mapped: %+v", got)
	}
	if gotReq.TenantId != "t1" || gotReq.Asset != purserpb.CryptoAsset_CRYPTO_ASSET_ETH || gotReq.ExpectedAmountCents != 5000 {
		t.Errorf("request not mapped: %+v", gotReq)
	}
	if gotReq.Currency == "" {
		t.Error("currency should default")
	}

	// Unspecified asset → error before backend.
	assetGuard := &clientstest.FakePurser{}
	if _, err := purserResolver(assetGuard).DoCreateCryptoTopup(clientstest.AuthedCtx("t1"), model.CreateCryptoTopupInput{
		AmountCents: 5000, Asset: purserpb.CryptoAsset_CRYPTO_ASSET_UNSPECIFIED,
	}); err == nil {
		t.Fatal("unspecified asset should error")
	}
	if assetGuard.Calls != 0 {
		t.Fatalf("backend consulted for invalid asset: %d calls", assetGuard.Calls)
	}

	// Out-of-range amount → error.
	amtGuard := &clientstest.FakePurser{}
	if _, err := purserResolver(amtGuard).DoCreateCryptoTopup(clientstest.AuthedCtx("t1"), model.CreateCryptoTopupInput{
		AmountCents: 0, Asset: purserpb.CryptoAsset_CRYPTO_ASSET_ETH,
	}); err == nil {
		t.Fatal("zero amount should error")
	}
	if amtGuard.Calls != 0 {
		t.Fatalf("backend consulted for invalid amount: %d calls", amtGuard.Calls)
	}

	guard := &clientstest.FakePurser{}
	if _, err := purserResolver(guard).DoCreateCryptoTopup(context.Background(), model.CreateCryptoTopupInput{
		AmountCents: 5000, Asset: purserpb.CryptoAsset_CRYPTO_ASSET_ETH,
	}); err == nil {
		t.Fatal("missing tenant should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("backend consulted without tenant: %d calls", guard.Calls)
	}
}

// DoGetCryptoTopupStatus maps base fields and conditionally populates the
// optional pointers (tx hash, received amounts, credited amount, timestamps).
func TestDoGetCryptoTopupStatus(t *testing.T) {
	detected := timestamppb.New(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	r := purserResolver(&clientstest.FakePurser{
		GetCryptoTopupFn: func(_ context.Context, topupID string) (*purserpb.CryptoTopup, error) {
			return &purserpb.CryptoTopup{
				Id:                  topupID,
				DepositAddress:      "0xabc",
				Asset:               purserpb.CryptoAsset_CRYPTO_ASSET_ETH,
				Status:              "confirming",
				Confirmations:       3,
				ExpiresAt:           timestamppb.Now(),
				TxHash:              "0xdeadbeef",
				CreditedAmountCents: 4999,
				DetectedAt:          detected,
			}, nil
		},
	})
	got, err := r.DoGetCryptoTopupStatus(clientstest.AuthedCtx("t1"), "ct_1")
	if err != nil || got == nil || got.ID != "ct_1" || got.Status != "confirming" || got.Confirmations != 3 {
		t.Fatalf("DoGetCryptoTopupStatus = (%+v, %v)", got, err)
	}
	if got.TxHash == nil || *got.TxHash != "0xdeadbeef" {
		t.Errorf("tx hash pointer not populated: %v", got.TxHash)
	}
	if got.CreditedAmountCents == nil || *got.CreditedAmountCents != 4999 {
		t.Errorf("credited cents pointer not populated: %v", got.CreditedAmountCents)
	}
	if got.DetectedAt == nil {
		t.Error("detected-at pointer not populated")
	}

	// Bare response → optional pointers stay nil.
	rBare := purserResolver(&clientstest.FakePurser{
		GetCryptoTopupFn: func(_ context.Context, topupID string) (*purserpb.CryptoTopup, error) {
			return &purserpb.CryptoTopup{Id: topupID, Status: "pending", ExpiresAt: timestamppb.Now()}, nil
		},
	})
	got, err = rBare.DoGetCryptoTopupStatus(clientstest.AuthedCtx("t1"), "ct_2")
	if err != nil {
		t.Fatal(err)
	}
	if got.TxHash != nil || got.CreditedAmountCents != nil || got.DetectedAt != nil {
		t.Errorf("absent fields should leave pointers nil: %+v", got)
	}

	failing := purserResolver(&clientstest.FakePurser{
		GetCryptoTopupFn: func(context.Context, string) (*purserpb.CryptoTopup, error) {
			return nil, errors.New("boom")
		},
	})
	if _, err := failing.DoGetCryptoTopupStatus(clientstest.AuthedCtx("t1"), "ct_3"); err == nil {
		t.Fatal("backend error should propagate")
	}
}

// ============================================================================
// PROMOTION / TIER CHANGE
// ============================================================================

// DoPromoteToPaid maps a success response into a PromoteToPaidPayload; a backend
// error becomes a ValidationError union member, and a missing tenant likewise.
func TestDoPromoteToPaid(t *testing.T) {
	var gotTier string
	r := purserResolver(&clientstest.FakePurser{
		PromoteToPaidFn: func(_ context.Context, _, tierID string) (*purserpb.PromoteToPaidResponse, error) {
			gotTier = tierID
			return &purserpb.PromoteToPaidResponse{
				Success: true, Message: "ok", NewBillingModel: "postpaid",
				CreditBalanceCents: 1500, SubscriptionId: "sub_9",
			}, nil
		},
	})
	res, err := r.DoPromoteToPaid(clientstest.AuthedCtx("t1"), "tier-pro")
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := res.(*model.PromoteToPaidPayload)
	if !ok || !payload.Success || payload.NewBillingModel != "postpaid" || payload.CreditBalanceCents != 1500 || payload.SubscriptionID != "sub_9" {
		t.Fatalf("expected PromoteToPaidPayload, got %T %+v", res, res)
	}
	if gotTier != "tier-pro" {
		t.Errorf("tier not forwarded: %q", gotTier)
	}

	// Missing tenant → ValidationError union member, no backend call.
	guard := &clientstest.FakePurser{}
	res, _ = purserResolver(guard).DoPromoteToPaid(context.Background(), "tier-pro")
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("missing tenant should be ValidationError, got %T", res)
	}
	if guard.Calls != 0 {
		t.Fatalf("backend consulted without tenant: %d calls", guard.Calls)
	}

	// Backend error → ValidationError (PROMOTION_FAILED).
	failing := purserResolver(&clientstest.FakePurser{
		PromoteToPaidFn: func(context.Context, string, string) (*purserpb.PromoteToPaidResponse, error) {
			return nil, errors.New("ineligible")
		},
	})
	res, err = failing.DoPromoteToPaid(clientstest.AuthedCtx("t1"), "tier-pro")
	if err != nil {
		t.Fatalf("backend error should be a union member: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("backend error should be ValidationError, got %T", res)
	}
}

// DoChangeBillingTier returns a ChangeBillingTierPayload and, when the response
// carries an applied/pending tier id, hydrates the tier via GetBillingTier.
func TestDoChangeBillingTier(t *testing.T) {
	effective := timestamppb.New(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	var gotTierLookup string
	r := purserResolver(&clientstest.FakePurser{
		ChangeBillingTierFn: func(_ context.Context, _, tierID string) (*purserpb.ChangeBillingTierResponse, error) {
			return &purserpb.ChangeBillingTierResponse{
				Success: true, Message: "applied", EffectiveAt: effective, AppliedTierId: tierID,
			}, nil
		},
		GetBillingTierFn: func(_ context.Context, tierID string) (*purserpb.BillingTier, error) {
			gotTierLookup = tierID
			return &purserpb.BillingTier{Id: tierID, DisplayName: "Pro"}, nil
		},
	})
	res, err := r.DoChangeBillingTier(clientstest.AuthedCtx("t1"), "tier-pro")
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := res.(*model.ChangeBillingTierPayload)
	if !ok || !payload.Success || payload.Message != "applied" {
		t.Fatalf("expected ChangeBillingTierPayload, got %T %+v", res, res)
	}
	if payload.EffectiveAt == nil {
		t.Error("effective-at not mapped")
	}
	if payload.AppliedTier == nil || payload.AppliedTier.Id != "tier-pro" {
		t.Errorf("applied tier not hydrated: %+v", payload.AppliedTier)
	}
	if gotTierLookup != "tier-pro" {
		t.Errorf("applied tier id not looked up: %q", gotTierLookup)
	}

	// Empty tier id → ValidationError before backend.
	emptyGuard := &clientstest.FakePurser{}
	res, _ = purserResolver(emptyGuard).DoChangeBillingTier(clientstest.AuthedCtx("t1"), "   ")
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("blank tier should be ValidationError, got %T", res)
	}
	if emptyGuard.Calls != 0 {
		t.Fatalf("backend consulted for blank tier: %d calls", emptyGuard.Calls)
	}

	// Missing tenant → ValidationError, no backend call.
	guard := &clientstest.FakePurser{}
	res, _ = purserResolver(guard).DoChangeBillingTier(context.Background(), "tier-pro")
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("missing tenant should be ValidationError, got %T", res)
	}
	if guard.Calls != 0 {
		t.Fatalf("backend consulted without tenant: %d calls", guard.Calls)
	}

	// Backend error → ValidationError (TIER_CHANGE_FAILED).
	failing := purserResolver(&clientstest.FakePurser{
		ChangeBillingTierFn: func(context.Context, string, string) (*purserpb.ChangeBillingTierResponse, error) {
			return nil, errors.New("boom")
		},
	})
	res, err = failing.DoChangeBillingTier(clientstest.AuthedCtx("t1"), "tier-pro")
	if err != nil {
		t.Fatalf("backend error should be a union member: %v", err)
	}
	if _, ok := res.(*model.ValidationError); !ok {
		t.Fatalf("backend error should be ValidationError, got %T", res)
	}

	// Tier hydration failure surfaces as a real Go error (load applied tier).
	hydrateFail := purserResolver(&clientstest.FakePurser{
		ChangeBillingTierFn: func(_ context.Context, _, tierID string) (*purserpb.ChangeBillingTierResponse, error) {
			return &purserpb.ChangeBillingTierResponse{Success: true, AppliedTierId: tierID}, nil
		},
		GetBillingTierFn: func(context.Context, string) (*purserpb.BillingTier, error) {
			return nil, errors.New("tier lookup boom")
		},
	})
	if _, err := hydrateFail.DoChangeBillingTier(clientstest.AuthedCtx("t1"), "tier-pro"); err == nil {
		t.Fatal("applied tier hydration failure should be a Go error")
	}
}

// ============================================================================
// SUBSCRIPTION CUSTOM TERMS
// ============================================================================

// DoUpdateSubscriptionCustomTerms maps custom features, pricing overrides, and
// entitlement overrides into the Purser request, and treats empty override
// slices as explicit clear flags.
func TestDoUpdateSubscriptionCustomTerms(t *testing.T) {
	var gotReq *purserpb.UpdateSubscriptionRequest
	r := purserResolver(&clientstest.FakePurser{
		UpdateSubscriptionFn: func(_ context.Context, req *purserpb.UpdateSubscriptionRequest) (*purserpb.TenantSubscription, error) {
			gotReq = req
			return &purserpb.TenantSubscription{Id: "sub_1", Status: "active"}, nil
		},
	})
	rec := true
	support := "priority"
	cfg := `{"k":"v"}`
	got, err := r.DoUpdateSubscriptionCustomTerms(clientstest.AuthedCtx("admin"), "tenant-9", model.UpdateSubscriptionCustomTermsInput{
		CustomFeatures: &model.BillingFeaturesInput{Recording: &rec, SupportLevel: &support},
		PricingOverrides: []*model.PricingRuleInput{
			{Meter: "egress_gb", Model: "per_unit", Currency: "EUR", IncludedQuantity: "0", UnitPrice: "0.01", ConfigJSON: &cfg},
		},
		EntitlementOverrides: []*model.EntitlementEntryInput{{Key: "max_streams", Value: "100"}},
	})
	if err != nil || got == nil || got.Id != "sub_1" {
		t.Fatalf("DoUpdateSubscriptionCustomTerms = (%+v, %v)", got, err)
	}
	if gotReq.TenantId != "tenant-9" {
		t.Errorf("tenant not forwarded: %q", gotReq.TenantId)
	}
	if gotReq.CustomFeatures == nil || !gotReq.CustomFeatures.Recording || gotReq.CustomFeatures.SupportLevel != "priority" {
		t.Errorf("custom features not mapped: %+v", gotReq.CustomFeatures)
	}
	if len(gotReq.PricingOverrides) != 1 || gotReq.PricingOverrides[0].Meter != "egress_gb" || gotReq.PricingOverrides[0].ConfigJson != `{"k":"v"}` {
		t.Errorf("pricing overrides not mapped: %+v", gotReq.PricingOverrides)
	}
	if gotReq.ClearPricingOverrides {
		t.Error("non-empty pricing overrides should not set clear flag")
	}
	if v, ok := gotReq.EntitlementOverrides["max_streams"]; !ok || v != "100" {
		t.Errorf("entitlement overrides not mapped: %+v", gotReq.EntitlementOverrides)
	}

	// Empty (non-nil) override slices → explicit clear flags.
	var clearReq *purserpb.UpdateSubscriptionRequest
	rClear := purserResolver(&clientstest.FakePurser{
		UpdateSubscriptionFn: func(_ context.Context, req *purserpb.UpdateSubscriptionRequest) (*purserpb.TenantSubscription, error) {
			clearReq = req
			return &purserpb.TenantSubscription{Id: "sub_2"}, nil
		},
	})
	if _, err := rClear.DoUpdateSubscriptionCustomTerms(clientstest.AuthedCtx("admin"), "tenant-9", model.UpdateSubscriptionCustomTermsInput{
		PricingOverrides:     []*model.PricingRuleInput{},
		EntitlementOverrides: []*model.EntitlementEntryInput{},
	}); err != nil {
		t.Fatal(err)
	}
	if !clearReq.ClearPricingOverrides || !clearReq.ClearEntitlementOverrides {
		t.Errorf("empty override slices should set clear flags: %+v", clearReq)
	}

	failing := purserResolver(&clientstest.FakePurser{
		UpdateSubscriptionFn: func(context.Context, *purserpb.UpdateSubscriptionRequest) (*purserpb.TenantSubscription, error) {
			return nil, errors.New("boom")
		},
	})
	if _, err := failing.DoUpdateSubscriptionCustomTerms(clientstest.AuthedCtx("admin"), "tenant-9", model.UpdateSubscriptionCustomTermsInput{}); err == nil {
		t.Fatal("backend error should propagate")
	}
}

// ============================================================================
// BALANCE TRANSACTIONS
// ============================================================================

// DoGetBalanceTransactionsConnection maps proto transactions into connection
// edges (incl. optional description/reference pointers) and surfaces the
// backend cursor page info.
func TestDoGetBalanceTransactionsConnection(t *testing.T) {
	refID := "ref-1"
	r := purserResolver(&clientstest.FakePurser{
		ListBalanceTransactionsFn: func(_ context.Context, tenantID string, txType *string, _ *commonpb.TimeRange, _ *commonpb.CursorPaginationRequest) (*purserpb.ListBalanceTransactionsResponse, error) {
			if tenantID != "t1" {
				t.Errorf("tenant not forwarded: %q", tenantID)
			}
			if txType == nil || *txType != "topup" {
				t.Errorf("transaction-type filter not forwarded: %v", txType)
			}
			return &purserpb.ListBalanceTransactionsResponse{
				Transactions: []*purserpb.BalanceTransaction{
					{Id: "tx-1", TenantId: "t1", AmountCents: 5000, BalanceAfterCents: 5000, TransactionType: "topup", Description: "Top-up", ReferenceId: &refID, CreatedAt: timestamppb.Now()},
				},
				Pagination: &commonpb.CursorPaginationResponse{HasNextPage: true, TotalCount: 4, StartCursor: ptrStr("tx-1"), EndCursor: ptrStr("tx-1")},
			}, nil
		},
	})
	txType := "topup"
	conn, err := r.DoGetBalanceTransactionsConnection(clientstest.AuthedCtx("t1"), nil, &txType, nil)
	if err != nil || conn == nil || len(conn.Edges) != 1 {
		t.Fatalf("DoGetBalanceTransactionsConnection = (%+v, %v)", conn, err)
	}
	node := conn.Edges[0].Node
	if node.ID != "tx-1" || node.AmountCents != 5000 || node.TransactionType != "topup" {
		t.Errorf("transaction not mapped: %+v", node)
	}
	if node.Description == nil || *node.Description != "Top-up" {
		t.Errorf("description pointer not populated: %v", node.Description)
	}
	if node.ReferenceID == nil || *node.ReferenceID != "ref-1" {
		t.Errorf("reference id pointer not populated: %v", node.ReferenceID)
	}
	if !conn.PageInfo.HasNextPage || conn.TotalCount != 4 || conn.PageInfo.StartCursor == nil {
		t.Errorf("page info not surfaced: %+v / total=%d", conn.PageInfo, conn.TotalCount)
	}

	guard := &clientstest.FakePurser{}
	if _, err := purserResolver(guard).DoGetBalanceTransactionsConnection(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("missing tenant should error")
	}
	if guard.Calls != 0 {
		t.Fatalf("backend consulted without tenant: %d calls", guard.Calls)
	}
}

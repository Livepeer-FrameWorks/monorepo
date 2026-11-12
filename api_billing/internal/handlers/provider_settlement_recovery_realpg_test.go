//go:build schema_verify

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/fx"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestProviderSettlementRecoveryPreservesCauseAndOwner_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	fixture, client := installStripeFixture(t)
	svc := &Service{db: db, logger: logging.NewLogger(), stripeClient: client}
	tenant, payment := uuid.NewString(), "pi_"+uuid.NewString()
	newProviderSettlementQueries(db).insertPending(t, tenant, "stripe", payment, 1000, "EUR")
	fixture.route(http.MethodGet, "/v1/balance_transactions/txn_cad", func(recordedProviderRequest) (int, any) {
		return http.StatusOK, map[string]any{"id": "txn_cad", "amount": 1200, "fee": 60, "net": 1140, "currency": "cad", "exchange_rate": 1.2, "created": time.Now().Unix()}
	})
	body, err := json.Marshal(map[string]any{"id": "evt_cad", "type": "charge.updated", "data": map[string]any{"object": map[string]any{"id": "ch_not_the_payment_intent", "payment_intent": payment, "balance_transaction": "txn_cad"}}})
	if err != nil {
		t.Fatal(err)
	}
	untrustedTenant := uuid.NewString()
	if ok, _, _ := svc.enqueueProviderWebhook(t.Context(), "stripe", "evt_cad", map[string]string{"tenant_id": untrustedTenant}, body); !ok {
		t.Fatal("enqueue")
	}
	unbound, err := purserdb.New(db).ListCheckoutRecovery(t.Context(), purserdb.ListCheckoutRecoveryParams{TenantID: untrustedTenant, RowLimit: 10})
	if err != nil || len(unbound) != 0 {
		t.Fatalf("unverified webhook header assigned recovery ownership: %+v %v", unbound, err)
	}
	store := &providerWebhookInboxStore{db: db}
	claims, err := store.ClaimBatch(t.Context(), 1, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim: %v %v", claims, err)
	}
	_, dispatchErr := (&providerWebhookDispatcher{service: svc}).Dispatch(t.Context(), claims[0].Payload)
	if !errors.Is(dispatchErr, fx.ErrUnsupportedCurrency) {
		t.Errorf("dispatcher erased settlement cause: %v", dispatchErr)
	}
	if err := store.RecordFailureToken(t.Context(), claims[0].ID, 0, nil, dispatchErr, 0, claims[0].LeaseToken); err != nil {
		t.Fatal(err)
	}
	var status, cause string
	if err := db.QueryRowContext(t.Context(), `SELECT status,last_error FROM purser.provider_webhook_inbox WHERE id=$1`, claims[0].ID).Scan(&status, &cause); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cause, "unsupported_currency") || !strings.Contains(cause, payment) || strings.Contains(cause, "ch_not_the_payment_intent") {
		t.Errorf("recovery cause has no safe payment correlation: %s", cause)
	}
	if status != "blocked" {
		t.Errorf("permanent settlement repeats instead of staying visible: status=%s", status)
	}
	again, err := store.ClaimBatch(t.Context(), 1, time.Minute)
	if err != nil || len(again) != 0 {
		t.Errorf("blocked settlement reclaimed: %v %v", again, err)
	}
	items, err := purserdb.New(db).ListCheckoutRecovery(t.Context(), purserdb.ListCheckoutRecoveryParams{TenantID: tenant, RowLimit: 10})
	if err != nil || len(items) != 2 || items[0].TenantID != tenant || items[1].TenantID != tenant {
		t.Errorf("recorded payment owner absent from recovery: %+v %v", items, err)
	}
	count, err := purserdb.New(db).ProviderWebhookInboxRepeatedFailures(context.Background())
	if err != nil || count != 2 {
		t.Errorf("blocked settlement disappeared from alert inventory: %d %v", count, err)
	}
}

func TestPolledBlockedSettlementRemainsVisibleWithoutWebhook_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	svc := &Service{db: db, logger: logging.NewLogger()}
	tenant, payment := uuid.NewString(), "pi_polled_native"
	newProviderSettlementQueries(db).insertPending(t, tenant, "stripe", payment, 1000, "EUR")
	rate := decimal.RequireFromString("1.2")
	_, err := svc.settleStripePayment(t.Context(), payment, &billingstripe.BalanceTransaction{ID: "txn_polled", Currency: "CAD", Amount: 1200, Fee: 60, Net: 1140, ExchangeRate: &rate, Created: time.Now()})
	if !errors.Is(err, fx.ErrUnsupportedCurrency) {
		t.Fatalf("expected retained blocked native evidence: %v", err)
	}
	items, err := purserdb.New(db).ListCheckoutRecovery(t.Context(), purserdb.ListCheckoutRecoveryParams{TenantID: tenant, RowLimit: 10})
	if err != nil || len(items) != 1 || items[0].ReservationStatus != "provider_settlement" || items[0].TenantID != tenant {
		t.Fatalf("polled settlement absent from recovery without a callback: %+v %v", items, err)
	}
	count, err := purserdb.New(db).ProviderWebhookInboxRepeatedFailures(t.Context())
	if err != nil || count != 1 {
		t.Fatalf("polled blocked settlement disappeared from alerts: %d %v", count, err)
	}
}

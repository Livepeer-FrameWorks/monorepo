//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_billing/internal/fx"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestProviderNativeSettlementValuation_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	ctx := context.Background()
	fixture, stripeClient := installStripeFixture(t)
	service := &Service{db: db, logger: logging.NewLogger(), stripeClient: stripeClient}
	queries := newProviderSettlementQueries(db)
	date := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	if err := fx.Upsert(ctx, db, []fx.Rate{
		{Currency: fx.USD, ReferenceDate: date, UnitsPerEUR: decimal.RequireFromString("1.2"), Source: fx.SourceECB},
		{Currency: fx.GBP, ReferenceDate: date, UnitsPerEUR: decimal.RequireFromString("0.8"), Source: fx.SourceECB},
	}, date); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, charge, settled string
		amount, fee, grossEUR int64
		feeEUR                int64
		providerRate          any
	}{
		{name: "USD to USD", charge: "USD", settled: "usd", amount: 1200, fee: 60, grossEUR: 1000, feeEUR: 50},
		{name: "EUR to USD", charge: "EUR", settled: "usd", amount: 1200, fee: 60, grossEUR: 1000, feeEUR: 50, providerRate: 1.2},
		{name: "provider precision exceeds storage", charge: "EUR", settled: "usd", amount: 1200, fee: 60, grossEUR: 1000, feeEUR: 50, providerRate: 1.2345678901234567},
		{name: "GBP to GBP", charge: "GBP", settled: "gbp", amount: 800, fee: 40, grossEUR: 1000, feeEUR: 50},
	} {
		t.Run(test.name, func(t *testing.T) {
			tenant := uuid.NewString()
			payment := "pi_" + uuid.NewString()
			transaction := "txn_" + uuid.NewString()
			charge := test.amount
			if !strings.EqualFold(test.charge, test.settled) {
				charge = 1000
			}
			queries.insertPending(t, tenant, "stripe", payment, charge, test.charge)
			fixture.route(http.MethodGet, "/v1/balance_transactions/"+transaction, func(recordedProviderRequest) (int, any) {
				return http.StatusOK, map[string]any{
					"id": transaction, "object": "balance_transaction", "amount": test.amount,
					"fee": test.fee, "net": test.amount - test.fee, "currency": test.settled,
					"exchange_rate": test.providerRate, "created": date.Unix(),
				}
			})
			body, err := json.Marshal(map[string]any{"id": "ch_fixture", "payment_intent": payment, "balance_transaction": transaction})
			if err != nil {
				t.Fatal(err)
			}
			payload := StripeWebhookPayload{ID: "evt_fixture", Type: "charge.updated"}
			payload.Data.Object = body
			if err := service.handleStripeChargeUpdatedContext(context.Background(), payload); err != nil {
				t.Fatalf("native provider settlement rejected: %v", err)
			}
			var status, currency, source string
			var native, fee, net, grossEUR, feeEUR, netEUR int64
			if err := db.QueryRowContext(ctx, `SELECT status, settlement_currency, settled_amount_cents, fee_cents, net_cents,
				settled_eur_cents, fee_eur_cents, net_eur_cents, valuation_source
				FROM purser.provider_settlements WHERE tenant_id=$1 AND provider='stripe' AND provider_payment_id=$2`, tenant, payment).
				Scan(&status, &currency, &native, &fee, &net, &grossEUR, &feeEUR, &netEUR, &source); err != nil {
				t.Fatal(err)
			}
			if status != "settled" || currency != strings.ToUpper(test.settled) || native != test.amount || fee != test.fee || net != test.amount-test.fee {
				t.Fatalf("native evidence changed: status=%s currency=%s amounts=%d/%d/%d", status, currency, native, fee, net)
			}
			if grossEUR != test.grossEUR || feeEUR != test.feeEUR || netEUR != grossEUR-feeEUR || source != fx.SourceECB {
				t.Fatalf("EUR valuation = %d/%d/%d source=%s", grossEUR, feeEUR, netEUR, source)
			}
			if err := service.handleStripeChargeUpdatedContext(context.Background(), payload); err != nil {
				t.Fatalf("idempotent provider replay: %v", err)
			}
			var count int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.provider_settlements WHERE tenant_id=$1 AND provider='stripe' AND provider_payment_id=$2 AND status='settled'`, tenant, payment).Scan(&count); err != nil || count != 1 {
				t.Fatalf("replay settlement count=%d error=%v", count, err)
			}
		})
	}
}

func TestProviderNativeEvidenceSurvivesMissingValuation_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	ctx := context.Background()
	service := &Service{db: db, logger: logging.NewLogger()}
	tenant, payment := uuid.NewString(), "pi_"+uuid.NewString()
	newProviderSettlementQueries(db).insertPending(t, tenant, "stripe", payment, 1200, "USD")
	date := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	transaction := &billingstripe.BalanceTransaction{ID: "txn_missing_fx", Currency: "USD", Amount: 1200, Fee: 60, Net: 1140, Created: date}
	if _, err := service.settleStripePayment(ctx, payment, transaction); !errors.Is(err, fx.ErrUnavailable) {
		t.Fatalf("missing reference rate outcome = %v", err)
	}
	var status, transactionID string
	var native int64
	var valued bool
	if err := db.QueryRowContext(ctx, `SELECT status, provider_balance_transaction_id, settled_amount_cents,
		settled_eur_cents IS NOT NULL FROM purser.provider_settlements WHERE tenant_id=$1 AND provider_payment_id=$2`, tenant, payment).
		Scan(&status, &transactionID, &native, &valued); err != nil {
		t.Fatal(err)
	}
	if status != "recorded" || transactionID != transaction.ID || native != 1200 || valued {
		t.Fatalf("native evidence was lost or falsely valued: status=%s transaction=%s native=%d valued=%t", status, transactionID, native, valued)
	}
	if err := fx.Upsert(ctx, db, []fx.Rate{{Currency: fx.USD, ReferenceDate: date, UnitsPerEUR: decimal.RequireFromString("1.2"), Source: fx.SourceECB}}, date); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE purser.provider_settlements SET updated_at=NOW()-INTERVAL '11 minutes' WHERE tenant_id=$1 AND provider_payment_id=$2`, tenant, payment); err != nil {
		t.Fatal(err)
	}
	if err := service.retryRecordedProviderSettlements(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	var eur int64
	if err := db.QueryRowContext(ctx, `SELECT status, settled_eur_cents FROM purser.provider_settlements WHERE tenant_id=$1 AND provider_payment_id=$2`, tenant, payment).Scan(&status, &eur); err != nil {
		t.Fatal(err)
	}
	if status != "settled" || eur != 1000 {
		t.Fatalf("stored evidence did not resume without a provider client: status=%s EUR=%d", status, eur)
	}
}

func TestProviderNativeValuationConcurrentReplay_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	ctx := context.Background()
	service := &Service{db: db, logger: logging.NewLogger()}
	tenant, payment := uuid.NewString(), "pi_"+uuid.NewString()
	newProviderSettlementQueries(db).insertPending(t, tenant, "stripe", payment, 1200, "USD")
	date := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	if err := fx.Upsert(ctx, db, []fx.Rate{{Currency: fx.USD, ReferenceDate: date, UnitsPerEUR: decimal.RequireFromString("1.2"), Source: fx.SourceECB}}, date); err != nil {
		t.Fatal(err)
	}
	transaction := &billingstripe.BalanceTransaction{ID: "txn_concurrent", Currency: "USD", Amount: 1200, Fee: 60, Net: 1140, Created: date}
	var group sync.WaitGroup
	var applied atomic.Int32
	errorsCh := make(chan error, 2)
	for range 2 {
		group.Go(func() {
			changed, err := service.settleStripePayment(ctx, payment, transaction)
			if err != nil {
				errorsCh <- err
			}
			if changed {
				applied.Add(1)
			}
		})
	}
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Error(err)
	}
	if applied.Load() != 1 {
		t.Fatalf("valuation applied %d times", applied.Load())
	}
	if _, err := service.settleProviderPayment(ctx, uuid.NewString(), "stripe", payment, transaction.ID, "USD", 1200, 60, 1140, sql.NullString{}, date); !errors.Is(err, errProviderSettlementEvidenceConflict) {
		t.Fatalf("wrong tenant could reuse payment identity: %v", err)
	}
}

func TestProviderNativeSettlementConstraints_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	tenant, payment := uuid.NewString(), "pi_identity"
	newProviderSettlementQueries(db).insertPending(t, tenant, "stripe", payment, 1000, "EUR")
	_, err := db.ExecContext(t.Context(), `UPDATE purser.provider_settlements SET status='settled', provider_balance_transaction_id='txn_identity',
		settled_amount_cents=1000,fee_cents=50,net_cents=950,settlement_currency='EUR',settled_at=NOW()
		WHERE tenant_id=$1 AND provider_payment_id=$2`, tenant, payment)
	if err != nil {
		t.Fatalf("mixed-version EUR writer lost compatibility: %v", err)
	}
	var source string
	var eur, fee, net int64
	if err := db.QueryRowContext(t.Context(), `SELECT valuation_source,settled_eur_cents,fee_eur_cents,net_eur_cents FROM purser.provider_settlements WHERE tenant_id=$1 AND provider_payment_id=$2`, tenant, payment).Scan(&source, &eur, &fee, &net); err != nil {
		t.Fatal(err)
	}
	if source != fx.SourceIdentity || eur != 1000 || fee != 50 || net != 950 {
		t.Fatalf("identity valuation=%s %d/%d/%d", source, eur, fee, net)
	}
	for _, assignment := range []string{"net_cents=949", "net_eur_cents=949", "valuation_reference_date=CURRENT_DATE+1", "fee_eur_cents=49,net_eur_cents=951"} {
		if _, err := db.ExecContext(t.Context(), `UPDATE purser.provider_settlements SET `+assignment+` WHERE tenant_id=$1 AND provider_payment_id=$2`, tenant, payment); err == nil {
			t.Errorf("constraint accepted inconsistent evidence: %s", assignment)
		}
	}
}

func TestProviderNativeSettlementRoundingUsesUTCReferenceDate_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	svc := &Service{db: db, logger: logging.NewLogger()}
	tenant, payment := uuid.NewString(), "pi_rounding"
	newProviderSettlementQueries(db).insertPending(t, tenant, "stripe", payment, 2, "USD")
	date := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	if err := fx.Upsert(t.Context(), db, []fx.Rate{{Currency: fx.USD, ReferenceDate: date, UnitsPerEUR: decimal.RequireFromString("1.5"), Source: fx.SourceECB}}, date); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 12, 0, 30, 0, 1234, time.FixedZone("UTC+2", 2*60*60))
	if _, err := svc.settleProviderPayment(t.Context(), tenant, "stripe", payment, "txn_rounding", "USD", 2, 1, 1, sql.NullString{}, at); err != nil {
		t.Fatal(err)
	}
	var gross, fee, net int64
	var reference time.Time
	if err := db.QueryRowContext(t.Context(), `SELECT settled_eur_cents,fee_eur_cents,net_eur_cents,valuation_reference_date FROM purser.provider_settlements WHERE tenant_id=$1 AND provider_payment_id=$2`, tenant, payment).Scan(&gross, &fee, &net, &reference); err != nil {
		t.Fatal(err)
	}
	if gross != 1 || fee != 1 || net != 0 || reference.Format(time.DateOnly) != "2026-11-11" {
		t.Fatalf("rounding/date=%d/%d/%d %s", gross, fee, net, reference)
	}
}

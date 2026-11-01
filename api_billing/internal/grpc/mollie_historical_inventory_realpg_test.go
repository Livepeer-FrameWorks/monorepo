//go:build schema_verify

package grpc

import (
	"database/sql"
	"strings"
	"testing"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
)

func assertMollieHistoricalPaymentInventory(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	validation, migrationErr := dbsql.Content.ReadFile("migrations/purser/v0.3.14/postdeploy/007_validate_historical_funding_inventory_indexes.sql")
	if migrationErr != nil {
		t.Fatal(migrationErr)
	}
	if _, migrationErr = db.ExecContext(t.Context(), string(validation)); migrationErr != nil {
		t.Fatalf("inventory index postdeploy check: %v", migrationErr)
	}
	tenant, tier, _ := seedFallbackLifecycle(t, db)
	otherTenant := uuid.NewString()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed := func(owner string) string {
		t.Helper()
		id := uuid.NewString()
		exec(`INSERT INTO purser.payment_provider_intents (
			id, tenant_id, provider, purpose, local_reference_type, local_reference_id,
			status, currency, amount_cents, idempotency_key, provider_payment_id, succeeded_at, created_at
		) VALUES ($1::uuid, $2, 'mollie', 'mollie_first_payment', 'billing_tiers', $3,
			'succeeded', 'EUR', 2000, $1::text, $1::text,
			NOW()-INTERVAL '2 months', NOW()-INTERVAL '2 months')`, id, owner, tier)
		return id
	}
	q := purserdb.New(db)
	if _, err := q.ClearSubscriptionFreeFallback(t.Context(), tenant); err != nil {
		t.Fatal(err)
	}
	unbound := seed(tenant)
	foreign := seed(otherTenant)
	for _, change := range []string{
		"activated_at=NOW()", "provider_mandate_id='mandate'", "amount_cents=0",
		"status='expired'", "provider_payment_id=NULL", "recovery_started_at=NOW()",
	} {
		id := seed(tenant)
		exec("UPDATE purser.payment_provider_intents SET "+change+" WHERE tenant_id=$1 AND id=$2", tenant, id)
	}
	server := &PurserServer{db: db, billing: handlers.NewService(db, logging.NewLogger(), nil, nil, nil, nil, nil)}
	ctx := operatorAssignCtx(uuid.NewString())
	list := func(owner string, limit, offset int32) *purserpb.AdminListCheckoutRecoveryResponse {
		t.Helper()
		out, err := server.AdminListCheckoutRecovery(ctx, &purserpb.AdminListCheckoutRecoveryRequest{TenantId: owner, Limit: limit, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := list(tenant, 10, 0)
	if len(out.GetItems()) != 1 {
		t.Fatalf("unbound paid receipt absent from tenant recovery inventory: %+v", out)
	}
	item := out.Items[0]
	if item.GetReservationId() != unbound || item.GetTenantId() != tenant || item.GetProvider() != "mollie" ||
		item.GetStatus() != "delivery_review" || item.GetReservationStatus() != "succeeded" ||
		!item.GetAmountKnown() || item.GetAmountCents() != 2000 || item.GetCurrency() != "EUR" ||
		item.GetNextAttemptAt() != nil || !strings.Contains(item.GetLastError(), "verify delivered service") {
		t.Fatalf("historical payment lacks actionable, non-automatic diagnostics: %+v", item)
	}
	first, second := list("", 1, 0), list("", 1, 1)
	if !first.GetHasMore() || second.GetHasMore() || len(first.Items) != 1 || len(second.Items) != 1 ||
		first.Items[0].GetReservationId() == second.Items[0].GetReservationId() {
		t.Fatalf("historical global pagination lost or duplicated receipts: first=%v second=%v", first, second)
	}
	if got := list(otherTenant, 10, 0); len(got.Items) != 1 || got.Items[0].GetReservationId() != foreign {
		t.Fatalf("historical payment crossed tenant boundary: %v", got)
	}
	preview, err := server.AdminRecoverCheckout(ctx, &purserpb.AdminRecoverCheckoutRequest{TenantId: tenant, ReservationId: unbound, Reason: "Check payment across rollout"})
	if err != nil || preview.GetOutcome() != "preview" || preview.GetAmountCents() != 2000 {
		t.Fatalf("listed payment cannot use normal recovery preview: out=%v err=%v", preview, err)
	}
	var writes int
	if err = db.QueryRowContext(t.Context(), `SELECT
        (SELECT count(*) FROM purser.provider_webhook_inbox WHERE headers->>'tenant_id'=$1) +
        (SELECT count(*) FROM purser.checkout_recovery_audit WHERE tenant_id=$1::uuid)`, tenant).Scan(&writes); err != nil || writes != 0 {
		t.Fatalf("inventory or preview queued work/audit: writes=%d err=%v", writes, err)
	}
	if err = q.EnqueueCheckoutReservationExpiry(t.Context(), purserdb.EnqueueCheckoutReservationExpiryParams{TenantID: tenant, IntentID: unbound}); err != nil {
		t.Fatal(err)
	}
	if got := list(tenant, 10, 0); len(got.Items) != 1 || got.Items[0].GetReservationId() != unbound || got.Items[0].GetStatus() != "pending" {
		t.Fatalf("queued recovery duplicated delivery review: %v", got)
	}
	exec(`UPDATE purser.provider_webhook_inbox SET status='processed'
		WHERE headers->>'tenant_id'=$1 AND event_key='checkout-expiry:'||$2`, tenant, unbound)
	if got := list(tenant, 10, 0); len(got.Items) != 1 || got.Items[0].GetStatus() != "delivery_review" {
		t.Fatalf("an old processed sweep hid the unresolved payment: %v", got)
	}
	var activated, recovering sql.NullTime
	if err = db.QueryRowContext(t.Context(), `SELECT activated_at, recovery_started_at
		FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, tenant, unbound).Scan(&activated, &recovering); err != nil || activated.Valid || recovering.Valid {
		t.Fatalf("inventory or preview mutated funding: activated=%v recovering=%v err=%v", activated, recovering, err)
	}
	if err = q.CompleteProviderCheckoutReservation(t.Context(), purserdb.CompleteProviderCheckoutReservationParams{TenantID: tenant, IntentID: unbound}); err != nil {
		t.Fatal(err)
	}
	if got := list(tenant, 10, 0); len(got.Items) != 0 {
		t.Fatalf("resolved funding remains in delivery review: %v", got)
	}
}

//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

func purserDomainPayload(t *testing.T, db *sql.DB, eventID string, msg proto.Message) {
	t.Helper()
	var payload []byte
	if err := db.QueryRow(`SELECT payload FROM purser.domain_event_outbox WHERE event_id = $1::uuid`, eventID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(payload, msg); err != nil {
		t.Fatal(err)
	}
}

// The Mollie first payment and the Mollie subscription activation each commit
// their billing event with the state that records them, attributed to the
// API token caller; a failed event insert leaves the state unwritten, and a
// retried request that Mollie answers with the same object records nothing.
func TestMollieBillingEventsCommitWithTheirState_RealPG(t *testing.T) { //nolint:funlen // One database follows both producers.
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	const secret = "purser-mollie-event-test-secret"
	hasher, err := events.NewTokenHasher(secret)
	if err != nil {
		t.Fatal(err)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger(), tokenHasher: hasher}
	tierID := uuid.NewString()
	if _, err := db.Exec(`
		INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, metering_enabled)
		VALUES ($1, $2, 'Mollie events', 20.00, 'EUR', false)`, tierID, "mollie-events-"+tierID); err != nil {
		t.Fatal(err)
	}
	userID := uuid.NewString()
	callerCtx := context.WithValue(ctx, ctxkeys.KeyAuthType, "api_token")
	callerCtx = context.WithValue(callerCtx, ctxkeys.KeyUserID, userID)
	callerCtx = context.WithValue(callerCtx, ctxkeys.KeyAPITokenID, "token-record-7")
	wantHash := strconv.FormatUint(events.HashIdentifier([]byte(secret), "token-record-7"), 10)

	newIntent := func(t *testing.T, tenantID string) string {
		t.Helper()
		id, err := purserdb.New(db).UpsertMollieFirstPaymentIntent(ctx, purserdb.UpsertMollieFirstPaymentIntentParams{
			TenantID: tenantID, TierID: tierID, Currency: "EUR", AmountCents: 2000,
			IdempotencyKey: "mollie-first-payment:" + tenantID + ":" + tierID,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	intentPayment := func(t *testing.T, intentID string) string {
		t.Helper()
		var payment sql.NullString
		if err := db.QueryRow(`SELECT provider_payment_id FROM purser.payment_provider_intents WHERE id = $1::uuid`, intentID).Scan(&payment); err != nil {
			t.Fatal(err)
		}
		return payment.String
	}
	amount := &publicv1.Money{AmountMinor: 2000, Currency: "EUR"}

	t.Run("first payment: a failed event insert leaves the intent without the payment", func(t *testing.T) {
		tenantID := uuid.NewString()
		intentID := newIntent(t, tenantID)
		allow := rejectPurserDomainEvents(t, db)
		err := server.recordMollieFirstPaymentOpen(callerCtx, tenantID, intentID, "tr_rollback", amount)
		allow()
		if err == nil || !strings.Contains(err.Error(), "domain outbox unavailable") {
			t.Fatalf("recordMollieFirstPaymentOpen err = %v, want the outbox failure", err)
		}
		if got := intentPayment(t, intentID); got != "" {
			t.Fatalf("intent payment = %q after the rollback, want none", got)
		}
		if rows := purserDomainRows(t, db, tenantID); len(rows) != 0 {
			t.Fatalf("domain rows = %+v after the rollback, want none", rows)
		}
	})

	t.Run("first payment: the payment and billing.payment_created commit together, once", func(t *testing.T) {
		tenantID := uuid.NewString()
		intentID := newIntent(t, tenantID)
		if err := server.recordMollieFirstPaymentOpen(callerCtx, tenantID, intentID, "tr_first", amount); err != nil {
			t.Fatal(err)
		}
		if got := intentPayment(t, intentID); got != "tr_first" {
			t.Fatalf("intent payment = %q, want tr_first", got)
		}
		rows := purserDomainRows(t, db, tenantID)
		if len(rows) != 1 {
			t.Fatalf("domain rows = %+v, want one", rows)
		}
		row := rows[0]
		if row.eventType != "billing.payment_created" || row.aggregateType != "payments" || row.aggregateID != "tr_first" ||
			row.authType != "api_token" || row.userID != userID || row.tokenHash != wantHash {
			t.Fatalf("domain row = %+v, want payment tr_first by the api token", row)
		}
		var created internalv1.PaymentCreated
		purserDomainPayload(t, db, row.eventID, &created)
		if created.GetPaymentId() != "" || created.GetProvider() != "mollie" || created.GetProviderReferenceId() != "tr_first" ||
			!proto.Equal(created.GetAmount(), amount) {
			t.Fatalf("payload = %v", &created)
		}

		if err := server.recordMollieFirstPaymentOpen(callerCtx, tenantID, intentID, "tr_first", amount); err != nil {
			t.Fatal(err)
		}
		if rows := purserDomainRows(t, db, tenantID); len(rows) != 1 {
			t.Fatalf("domain rows after the replay = %+v, want still one", rows)
		}
	})

	newSubscription := func(t *testing.T, tenantID string) string {
		t.Helper()
		var id string
		if err := db.QueryRow(`
			INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, billing_email)
			VALUES ($1::uuid, $2::uuid, 'active', 'prepaid', 'billing@example.test') RETURNING id::text`, tenantID, tierID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	activation := func(tenantID, mollieSubscriptionID string) purserdb.ActivateMollieTenantSubscriptionParams {
		periodEnd := time.Now().UTC().AddDate(0, 1, 0).Truncate(24 * time.Hour)
		return purserdb.ActivateMollieTenantSubscriptionParams{
			SubscriptionID:  sql.NullString{String: mollieSubscriptionID, Valid: true},
			NextPaymentDate: periodEnd,
			PeriodStart:     sql.NullTime{Time: periodEnd.AddDate(0, -1, 0), Valid: true},
			PeriodEnd:       sql.NullTime{Time: periodEnd, Valid: true},
			TierID:          tierID, TenantID: tenantID,
		}
	}
	mollieSubscription := func(t *testing.T, tenantID string) string {
		t.Helper()
		var id sql.NullString
		if err := db.QueryRow(`SELECT mollie_subscription_id FROM purser.tenant_subscriptions WHERE tenant_id = $1::uuid`, tenantID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id.String
	}

	t.Run("subscription: a failed event insert leaves the subscription unactivated", func(t *testing.T) {
		tenantID := uuid.NewString()
		newSubscription(t, tenantID)
		allow := rejectPurserDomainEvents(t, db)
		_, err := server.activateMollieSubscription(callerCtx, activation(tenantID, "sub_rollback"))
		allow()
		if err == nil || !strings.Contains(err.Error(), "domain outbox unavailable") {
			t.Fatalf("activateMollieSubscription err = %v, want the outbox failure", err)
		}
		if got := mollieSubscription(t, tenantID); got != "" {
			t.Fatalf("mollie subscription = %q after the rollback, want none", got)
		}
		if rows := purserDomainRows(t, db, tenantID); len(rows) != 0 {
			t.Fatalf("domain rows = %+v after the rollback, want none", rows)
		}
	})

	t.Run("subscription: activation and billing.subscription_created commit together, once", func(t *testing.T) {
		tenantID := uuid.NewString()
		subscriptionID := newSubscription(t, tenantID)
		rows, err := server.activateMollieSubscription(callerCtx, activation(tenantID, "sub_first"))
		if err != nil || rows != 1 {
			t.Fatalf("activateMollieSubscription = %d, %v", rows, err)
		}
		if got := mollieSubscription(t, tenantID); got != "sub_first" {
			t.Fatalf("mollie subscription = %q, want sub_first", got)
		}
		domain := purserDomainRows(t, db, tenantID)
		if len(domain) != 1 {
			t.Fatalf("domain rows = %+v, want one", domain)
		}
		row := domain[0]
		if row.eventType != "billing.subscription_created" || row.aggregateType != "subscriptions" || row.aggregateID != subscriptionID ||
			row.authType != "api_token" || row.userID != userID || row.tokenHash != wantHash {
			t.Fatalf("domain row = %+v, want subscription %s by the api token", row, subscriptionID)
		}
		var created internalv1.SubscriptionCreated
		purserDomainPayload(t, db, row.eventID, &created)
		if created.GetSubscriptionId() != subscriptionID || created.GetTierId() != tierID || created.GetStatus() != "active" {
			t.Fatalf("payload = %v", &created)
		}

		if rows, err := server.activateMollieSubscription(callerCtx, activation(tenantID, "sub_first")); err != nil || rows != 1 {
			t.Fatalf("replayed activation = %d, %v", rows, err)
		}
		if domain := purserDomainRows(t, db, tenantID); len(domain) != 1 {
			t.Fatalf("domain rows after the replay = %+v, want still one", domain)
		}
	})

	t.Run("subscription: a tenant without a subscription row records nothing", func(t *testing.T) {
		tenantID := uuid.NewString()
		if rows, err := server.activateMollieSubscription(callerCtx, activation(tenantID, "sub_orphan")); err != nil || rows != 0 {
			t.Fatalf("activateMollieSubscription = %d, %v, want 0 rows", rows, err)
		}
		if domain := purserDomainRows(t, db, tenantID); len(domain) != 0 {
			t.Fatalf("domain rows = %+v, want none", domain)
		}
	})
}

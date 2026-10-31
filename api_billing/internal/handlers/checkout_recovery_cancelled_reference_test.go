package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	stripeapi "github.com/stripe/stripe-go/v85"
)

type cancelledReferenceTransport struct {
	deadline                       time.Time
	previousStatus, incomingStatus string
}

func (f cancelledReferenceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var object any
	switch req.URL.Path {
	case "/v1/checkout/sessions/new-session":
		object = map[string]any{"id": "new-session", "status": "complete", "payment_status": "paid", "mode": "subscription", "subscription": "new-subscription", "created": time.Now().Unix(), "expires_at": f.deadline.Unix()}
	case "/v1/subscriptions/new-subscription":
		object = map[string]any{"id": "new-subscription", "status": f.incomingStatus}
	case "/v1/subscriptions/previous":
		object = map[string]any{"id": "previous", "status": f.previousStatus}
	default:
		return nil, fmt.Errorf("unexpected provider request in cancelled-reference fixture: %s", req.URL.Path)
	}
	body, _ := json.Marshal(object)
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
}

func TestRecoveryDistinguishesCancelledReferenceFromEstablishedSubscription(t *testing.T) {
	for _, tc := range []struct {
		name, previous, incoming string
		reconcile, wait          bool
	}{
		{"cancelled prior subscription", "canceled", "active", true, false},
		{"recoverable unpaid prior subscription", "unpaid", "active", false, false},
		{"paid receipt while subscription activation lags", "canceled", "incomplete", false, true},
		{"cancelled incoming subscription", "canceled", "canceled", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, m, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			deadline := time.Now().Add(time.Hour).Truncate(time.Second)
			previous := stripeapi.GetBackend(stripeapi.APIBackend)
			stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend, &stripeapi.BackendConfig{HTTPClient: &http.Client{Transport: cancelledReferenceTransport{deadline: deadline, previousStatus: tc.previous, incomingStatus: tc.incoming}}, MaxNetworkRetries: stripeapi.Int64(0)}))
			t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
			svc := &Service{db: db, logger: logging.NewLogger(), stripeClient: billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test", Logger: logging.NewLogger()})}
			m.ExpectBegin()
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			m.ExpectQuery("GetSelfServiceCheckoutTier").WithArgs("tenant", "receipt").WillReturnRows(sqlmock.NewRows([]string{"tier_id"}).AddRow("tier"))
			if tc.previous == "canceled" {
				m.ExpectQuery("GetTierForBillingChange").WithArgs("tier").WillReturnRows(sqlmock.NewRows([]string{"tier_level", "tier_name", "is_default_prepaid", "is_active", "is_enterprise"}).AddRow(2, "pro", false, true, false))
				m.ExpectQuery("SelfServiceAdvanceTierChange").WithArgs("tenant", "tier").WillReturnRows(sqlmock.NewRows([]string{"changing"}).AddRow(false))
				m.ExpectQuery("GetSelfServiceNativeSubscriptions").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{"stripe_id", "mollie_id", "paid_cancellation_term"}).AddRow("", "", false))
			}
			receipt := purserdb.GetProviderCheckoutReservationRow{ID: "receipt", TenantID: "tenant", Provider: "stripe", Purpose: "tenant_subscription_checkout", ProviderSessionID: sql.NullString{String: "new-session", Valid: true}, ProviderDeadline: sql.NullTime{Time: deadline, Valid: true}, ExpiresAt: sql.NullTime{Time: deadline, Valid: true}}
			current := purserdb.LockTenantSubscriptionForProviderChangeRow{Status: "cancelled", StripeSubscriptionID: sql.NullString{String: "previous", Valid: true}}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			var completedAt time.Time
			incomingID := ""
			reconcile, err := svc.inspectCheckoutForRecovery(ctx, tx, receipt, subscriptions.LockedSubscription{Current: current}, &completedAt, &incomingID)
			if (reconcile != nil) != tc.reconcile || errors.Is(err, ErrAwaitingProviderSettlement) != tc.wait || err != nil && !tc.wait {
				t.Fatalf("previous=%s incoming=%s: reconcile=%v err=%v", tc.previous, tc.incoming, reconcile != nil, err)
			}
			m.ExpectRollback()
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := m.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

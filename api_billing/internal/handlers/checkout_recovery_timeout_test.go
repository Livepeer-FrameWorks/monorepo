package handlers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	billingstripe "frameworks/api_billing/internal/stripe"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	stripeapi "github.com/stripe/stripe-go/v85"
)

type recoveryDeadlineTransport struct {
	t     *testing.T
	calls int
	paid  bool
}

func (r *recoveryDeadlineTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.calls++
	deadline, ok := req.Context().Deadline()
	if !ok || time.Until(deadline) > 30*time.Second {
		r.t.Fatal("checkout recovery provider request has no bounded deadline")
	}
	body := `{"id":"session","status":"open"}`
	if req.Method == http.MethodPost {
		body = `{"id":"session","status":"expired"}`
	}
	if r.paid {
		switch req.URL.Path {
		case "/v1/checkout/sessions/session":
			body = `{"id":"session","status":"complete","subscription":"subscription","expires_at":4102444800}`
		case "/v1/subscriptions/subscription":
			body = `{"id":"subscription","status":"active"}`
			if req.Method == http.MethodDelete {
				body = `{"id":"subscription","status":"canceled"}`
			}
		case "/v1/invoices":
			body = `{"object":"list","data":[],"has_more":false}`
		}
	}

	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestCheckoutRecoveryBoundsProviderCalls(t *testing.T) {
	testCheckoutRecoveryBoundsProviderCalls(t, false)
}

func TestCheckoutRecoveryBoundsSubscriptionCancellation(t *testing.T) {
	testCheckoutRecoveryBoundsProviderCalls(t, true)
}

func testCheckoutRecoveryBoundsProviderCalls(t *testing.T, paid bool) {
	db, m, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	transport := &recoveryDeadlineTransport{t: t, paid: paid}
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend, &stripeapi.BackendConfig{HTTPClient: &http.Client{Transport: transport}, MaxNetworkRetries: stripeapi.Int64(0)}))
	t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
	deadline := time.Now().Add(-time.Hour)
	svc := &Service{db: db, logger: logging.NewLogger(), stripeClient: billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test", Logger: logging.NewLogger()})}
	expectCheckoutRecoverySessionLock(m)
	m.ExpectBegin()
	expectProviderSubscriptionLock(m, "tenant")
	m.ExpectQuery("GetProviderCheckoutReservation").WillReturnRows(checkoutReservationTestRows("intent", "tenant", "stripe", "tenant_subscription_checkout", "customer", "session", nil, nil, "pending", deadline, 0))
	m.ExpectCommit()
	m.ExpectBegin()
	expectProviderSubscriptionLock(m, "tenant")
	m.ExpectQuery("GetProviderCheckoutReservation").WillReturnRows(checkoutReservationTestRows("intent", "tenant", "stripe", "tenant_subscription_checkout", "customer", "session", nil, nil, "pending", deadline, 0))
	m.ExpectExec("StartCheckoutCompensation").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectCommit()
	m.ExpectQuery("FinishCheckoutCompensation").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	expectCheckoutRecoverySessionUnlock(m)
	if err = svc.expireCheckoutReservation(context.Background(), checkoutReservationExpiry{TenantID: "tenant", IntentID: "intent"}); err != nil {
		t.Fatal(err)
	}
	expected := 3
	if paid {
		expected = 5
	}
	if transport.calls != expected {
		t.Fatalf("provider expiry calls=%d", transport.calls)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryClosesBoundSessionWithoutCustomerMapping(t *testing.T) {
	db, m, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	transport := &recoveryDeadlineTransport{t: t}
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend, &stripeapi.BackendConfig{HTTPClient: &http.Client{Transport: transport}, MaxNetworkRetries: stripeapi.Int64(0)}))
	t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
	deadline := time.Now().Add(-time.Hour)
	svc := &Service{db: db, logger: logging.NewLogger(), stripeClient: billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test", Logger: logging.NewLogger()})}
	expectCheckoutRecoverySessionLock(m)
	m.ExpectBegin()
	expectProviderSubscriptionLock(m, "tenant")
	m.ExpectQuery("GetProviderCheckoutReservation").WillReturnRows(checkoutReservationTestRows("intent", "tenant", "stripe", "tenant_subscription_checkout", nil, "session", nil, nil, "pending", deadline, 0))
	m.ExpectCommit()
	m.ExpectBegin()
	expectProviderSubscriptionLock(m, "tenant")
	m.ExpectQuery("GetProviderCheckoutReservation").WillReturnRows(checkoutReservationTestRows("intent", "tenant", "stripe", "tenant_subscription_checkout", nil, "session", nil, nil, "pending", deadline, 0))
	m.ExpectExec("StartCheckoutCompensation").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectCommit()
	m.ExpectQuery("FinishCheckoutCompensation").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	expectCheckoutRecoverySessionUnlock(m)
	if err = svc.expireCheckoutReservation(t.Context(), checkoutReservationExpiry{TenantID: "tenant", IntentID: "intent"}); err != nil {
		t.Fatal(err)
	}
	if transport.calls == 0 {
		t.Fatal("known provider session remained open because its customer mapping was missing")
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

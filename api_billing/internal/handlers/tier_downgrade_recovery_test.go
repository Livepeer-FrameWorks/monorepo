package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func expectTierDowngradeHold(mock sqlmock.Sqlmock, tenantID string, confirmed bool, invoiceID, invoiceStatus string) {
	mock.ExpectQuery(`-- name: GetTierDowngradeHold`).WithArgs(tenantID).WillReturnRows(sqlmock.NewRows(
		[]string{"tenant_id", "target_tier_id", "target_tier_name", "effective_at", "provider_confirmed", "invoice_id", "invoice_status"}).
		AddRow(tenantID, "tier-B", "developer", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), confirmed, invoiceID, invoiceStatus))
}

// Each hold the applier waits on is named with what releases it, in the order
// the applier checks them.
func TestTierDowngradeHoldNamesWhatHoldsIt(t *testing.T) {
	cases := []struct {
		name, invoiceID, invoiceStatus, status, detail string
		confirmed                                      bool
	}{
		{"provider", "invoice-1", "manual_review", DowngradeRecoveryAwaitingProvider, "tier_downgrade step", false},
		{"manual review", "invoice-1", "manual_review", DowngradeRecoveryInvoiceHeld, "admin billing set-cluster-pricing", true},
		{"draft", "invoice-1", "draft", DowngradeRecoveryInvoiceHeld, "metering is incomplete", true},
		{"no invoice", "", "", DowngradeRecoveryInvoiceHeld, "No invoice closes the period ending 2026-11-01T00:00:00Z", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer mockDB.Close()
			expectTierDowngradeHold(mock, "tenant-1", tc.confirmed, tc.invoiceID, tc.invoiceStatus)
			got, err := (&Service{db: mockDB}).TierDowngradeHold(context.Background(), "tenant-1")
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.status || !strings.Contains(got.Detail, tc.detail) || got.Hold.TargetTierName != "developer" {
				t.Fatalf("hold = %s %q (target %q), want %s containing %q", got.Status, got.Detail, got.Hold.TargetTierName, tc.status, tc.detail)
			}
		})
	}
}

func TestTierDowngradeHoldWithoutADowngradeIsNotFound(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	mock.ExpectQuery(`-- name: GetTierDowngradeHold`).WithArgs("tenant-1").WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}))
	if _, err = (&Service{db: mockDB}).TierDowngradeHold(context.Background(), "tenant-1"); !errors.Is(err, ErrNoTierDowngradeHold) {
		t.Fatalf("missing downgrade = %v, want ErrNoTierDowngradeHold", err)
	}
}

// Without the job manager the request must fail rather than report a hold it
// did not try to release.
func TestRecoverHeldTierDowngradeNeedsTheJobManager(t *testing.T) {
	if _, err := (&Service{}).RecoverHeldTierDowngrade(context.Background(), "tenant-1"); err == nil {
		t.Fatal("recovery without the job manager succeeded")
	}
}

// The job manager hands its one-tenant period close to the service the
// operator RPC calls.
func TestJobManagerWiresHeldDowngradeRecovery(t *testing.T) {
	svc := &Service{}
	wireAdvanceBilling(svc, &JobManager{})
	if svc.recoverHeldDowngrade == nil {
		t.Fatal("held downgrade recovery is not wired to the job manager")
	}
}

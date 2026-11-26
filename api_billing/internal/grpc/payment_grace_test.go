package grpc

import (
	"database/sql"
	"testing"
	"time"
)

func TestPaymentGracePreservesAdmissionWithinBothBounds(t *testing.T) {
	ends := sql.NullTime{Time: time.Now().Add(time.Hour), Valid: true}
	for _, tc := range []struct {
		name    string
		balance int64
		status  string
		ends    sql.NullTime
		want    bool
	}{
		{"zero", 0, "active", ends, true}, {"negative", -999, "active", ends, true},
		{"money bound", -1000, "active", ends, false}, {"overshoot", -5000, "active", ends, false},
		{"time bound", 0, "active", sql.NullTime{Time: time.Now().Add(-time.Second), Valid: true}, false},
		{"suspension", 0, "suspended", ends, false}, {"never funded", 0, "active", sql.NullTime{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := tenantAdmissionData{BillingModel: "prepaid", SubscriptionStatus: tc.status, BalanceCents: sql.NullInt64{Int64: tc.balance, Valid: true}, GraceEndsAt: tc.ends, GraceCreditCents: 1000}
			response := mapTenantAdmissionStatus(row)
			if response.GetPaymentGrace().GetActive() != tc.want || response.GetIsBalanceNegative() == tc.want {
				t.Fatalf("grace admission wrong: %+v", response)
			}
		})
	}
	row := tenantAdmissionData{BillingModel: "prepaid", SubscriptionStatus: "active", BalanceCents: sql.NullInt64{Int64: -900, Valid: true}, ReservedBalanceCents: 100, GraceEndsAt: ends, GraceCreditCents: 1000}
	if !mapTenantAdmissionStatus(row).IsBalanceNegative {
		t.Fatal("reservations exceeded grace ceiling")
	}
}

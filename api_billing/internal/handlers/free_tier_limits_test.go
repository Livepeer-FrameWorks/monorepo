package handlers

import (
	"github.com/DATA-DOG/go-sqlmock"
	"testing"
)

func TestFreeTargetLimitsPreserveExplicitOverridesAndRejectUnknownCaps(t *testing.T) {
	for _, raw := range []string{"30", "0", "null", "-1", "\"30\"", "2147483648"} {
		t.Run(raw, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectQuery("-- name: ListTierEntitlements").WithArgs("free").WillReturnRows(sqlmock.NewRows([]string{"key", "value"}).AddRow("storage_limit_gb", "10").AddRow("recording_retention_days", "30"))
			mock.ExpectQuery("-- name: ListSubscriptionEntitlementOverrides").WithArgs("sub").WillReturnRows(sqlmock.NewRows([]string{"key", "value"}).AddRow("recording_retention_days", raw))
			got, err := loadFreeTierLimits(t.Context(), db, "free", "sub")
			if raw == "30" || raw == "0" {
				if err != nil || got.StorageBytes != 10*(1<<30) {
					t.Fatalf("reviewed limit lost: %+v %v", got, err)
				}
			} else if err == nil {
				t.Fatalf("malformed cap unlocked Free: %s %+v", raw, got)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

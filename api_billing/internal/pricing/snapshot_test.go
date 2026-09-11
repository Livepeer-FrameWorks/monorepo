package pricing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

const snapshotTenant = "81000000-0000-4000-8000-000000000001"
const snapshotTier = "82000000-0000-4000-8000-000000000001"
const snapshotSubscription = "83000000-0000-4000-8000-000000000001"

var snapshotObservedAt = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func expectSnapshotClock(mock sqlmock.Sqlmock, at time.Time) {
	mock.ExpectQuery("clock_timestamp").WillReturnRows(sqlmock.NewRows([]string{"observed_at"}).AddRow(at))
}

func expectSnapshotTier(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("tenant_subscriptions").WithArgs(snapshotTenant).WillReturnRows(sqlmock.NewRows([]string{"tier_id", "tier_name", "base_price", "currency", "metering_enabled", "subscription_id"}).AddRow(snapshotTier, "pro", "10", "EUR", true, snapshotSubscription))
	mock.ExpectQuery("tier_pricing_rules").WithArgs(snapshotTier).WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}).AddRow("egress_gb", "all_usage", "EUR", "0", "0.05", "{}"))
	mock.ExpectQuery("subscription_pricing_overrides").WithArgs(snapshotSubscription).WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}))
	mock.ExpectQuery("tier_entitlements").WithArgs(snapshotTier).WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))
	mock.ExpectQuery("subscription_entitlement_overrides").WithArgs(snapshotSubscription).WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))
}

func expectSnapshotContext(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("ReadPlacementSubscriptionContext").WithArgs(snapshotTenant, snapshotSubscription).WillReturnRows(sqlmock.NewRows([]string{"period_start", "period_end", "pending_tier_id", "pending_effective_at"}).AddRow(nil, nil, nil, nil))
}

func expectSnapshotBoundaries(mock sqlmock.Sqlmock, at time.Time) {
	mock.ExpectQuery("ListPlacementPricingBoundaries").WithArgs(pq.Array([]string{"owned"}), at).WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "valid_until"}))
}

func snapshotOwnership(t *testing.T) *ClusterOwnershipSnapshot {
	t.Helper()
	owner := snapshotTenant
	qm := &fakeQM{clusters: map[string]*quartermasterpb.InfrastructureCluster{"owned": {ClusterId: "owned", OwnerTenantId: &owner}}}
	observation, err := CaptureClusterOwnership(context.Background(), qm, "owned")
	if err != nil {
		t.Fatal(err)
	}
	owner = "another owner"
	qm.clusters["owned"].IsPlatformOfficial = true
	return observation
}

func TestReadPlacementTariffsCommitsOnlyCompleteSnapshot(t *testing.T) {
	for _, failure := range []string{"", "begin", "clock", "tier", "history", "commit"} {
		t.Run(failure, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			owner := snapshotOwnership(t)
			injected := errors.New("injected read failure")
			begin := mock.ExpectBegin()
			if failure != "begin" && failure != "clock" {
				expectSnapshotClock(mock, snapshotObservedAt)
			}
			switch failure {
			case "begin":
				begin.WillReturnError(injected)
			case "clock":
				mock.ExpectQuery("clock_timestamp").WillReturnError(injected)
				mock.ExpectRollback()
			case "tier":
				mock.ExpectQuery("tenant_subscriptions").WithArgs(snapshotTenant).WillReturnError(sql.ErrNoRows)
				mock.ExpectRollback()
			default:
				expectSnapshotTier(mock)
				expectSnapshotContext(mock)
				if failure == "history" {
					mock.ExpectQuery("cluster_pricing_history").WithArgs("owned", sqlmock.AnyArg()).WillReturnError(injected)
					mock.ExpectRollback()
				} else {
					expectNoHistoryRow(mock, "owned")
					expectSnapshotBoundaries(mock, snapshotObservedAt)
					commit := mock.ExpectCommit()
					if failure == "commit" {
						commit.WillReturnError(injected)
					}
				}
			}
			got, err := ReadPlacementTariffs(context.Background(), db, snapshotTenant, []*ClusterOwnershipSnapshot{owner})
			if failure != "" {
				if err == nil || got != nil {
					t.Fatalf("failed transaction published snapshot: %+v %v", got, err)
				}
			} else {
				if err != nil || !got.AsOf.Equal(snapshotObservedAt) || got.Tier.TierID != snapshotTier || len(got.Clusters) != 1 || got.Clusters["owned"].Kind != KindTenantPrivate || got.Clusters["owned"].IsPlatformOfficial {
					t.Fatalf("wrong detached snapshot: %+v %v", got, err)
				}
				*got.Clusters["owned"].OwnerTenantID = uuid.Nil
				if owner.owner.OwnerTenantID.String() != snapshotTenant {
					t.Fatal("result mutated captured ownership")
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReadPlacementTariffsRejectsInputBeforeTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := snapshotOwnership(t)
	for _, owners := range [][]*ClusterOwnershipSnapshot{nil, {nil}, {{}}, {owner, owner}, make([]*ClusterOwnershipSnapshot, maxPlacementTariffClusters+1)} {
		if got, err := ReadPlacementTariffs(context.Background(), db, snapshotTenant, owners); err == nil || got != nil {
			t.Fatal("invalid owners accepted")
		}
	}
	for _, tenant := range []string{"", "not-a-uuid", uuid.Nil.String(), "{" + snapshotTenant + "}"} {
		if got, err := ReadPlacementTariffs(context.Background(), db, tenant, []*ClusterOwnershipSnapshot{owner}); err == nil || got != nil {
			t.Fatal("invalid tenant accepted")
		}
	}
	if _, err := ResolveClusterPricingTx(context.Background(), nil, ClusterPricingSnapshotInput{Ownership: owner, ConsumingTenantID: snapshotTenant, AsOf: time.Now()}); err == nil {
		t.Fatal("nil transaction accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := ReadPlacementTariffs(ctx, db, snapshotTenant, []*ClusterOwnershipSnapshot{owner}); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("canceled read published evidence: %+v %v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReadPlacementTariffsRetriesWholeSnapshot(t *testing.T) {
	for _, secondFails := range []bool{false, true} {
		t.Run(fmt.Sprint(secondFails), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			owner := snapshotOwnership(t)
			mock.ExpectBegin()
			expectSnapshotClock(mock, snapshotObservedAt.Add(-time.Minute))
			expectSnapshotTier(mock)
			expectSnapshotContext(mock)
			expectNoHistoryRow(mock, "owned")
			expectSnapshotBoundaries(mock, snapshotObservedAt.Add(-time.Minute))
			mock.ExpectCommit().WillReturnError(&pq.Error{Code: "40001", Message: "restart read"})
			mock.ExpectBegin()
			expectSnapshotClock(mock, snapshotObservedAt)
			var version uuid.UUID
			if secondFails {
				mock.ExpectQuery("tenant_subscriptions").WithArgs(snapshotTenant).WillReturnError(sql.ErrNoRows)
				mock.ExpectRollback()
			} else {
				expectSnapshotTier(mock)
				expectSnapshotContext(mock)
				version = expectHistoryRow(mock, "owned", "free_unmetered", "EUR", "0", "{}")
				expectSnapshotBoundaries(mock, snapshotObservedAt)
				mock.ExpectCommit()
			}
			got, err := ReadPlacementTariffs(context.Background(), db, snapshotTenant, []*ClusterOwnershipSnapshot{owner})
			if secondFails {
				if !errors.Is(err, sql.ErrNoRows) || got != nil {
					t.Fatalf("failed retry returned first attempt: %+v %v", got, err)
				}
			} else if err != nil || !got.AsOf.Equal(snapshotObservedAt) || got.Clusters["owned"].PriceVersionID != version {
				t.Fatalf("retry did not replace whole snapshot: %+v %v", got, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPlacementSnapshotRejectsMissingContextAndInvalidBoundaries(t *testing.T) {
	for _, failure := range []string{"context", "boundary read", "unknown cluster", "past boundary", "duplicate boundary"} {
		t.Run(failure, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			owner := snapshotOwnership(t)
			mock.ExpectBegin()
			expectSnapshotClock(mock, snapshotObservedAt)
			expectSnapshotTier(mock)
			if failure == "context" {
				mock.ExpectQuery("ReadPlacementSubscriptionContext").WithArgs(snapshotTenant, snapshotSubscription).WillReturnError(sql.ErrNoRows)
			} else {
				expectSnapshotContext(mock)
				expectNoHistoryRow(mock, "owned")
				query := mock.ExpectQuery("ListPlacementPricingBoundaries").WithArgs(pq.Array([]string{"owned"}), snapshotObservedAt)
				rows := sqlmock.NewRows([]string{"cluster_id", "valid_until"})
				switch failure {
				case "boundary read":
					query.WillReturnError(errors.New("boundary read failed"))
				case "unknown cluster":
					query.WillReturnRows(rows.AddRow("other", snapshotObservedAt.Add(time.Minute)))
				case "past boundary":
					query.WillReturnRows(rows.AddRow("owned", snapshotObservedAt))
				case "duplicate boundary":
					query.WillReturnRows(rows.AddRow("owned", snapshotObservedAt.Add(time.Minute)).AddRow("owned", snapshotObservedAt.Add(2*time.Minute)))
				}
			}
			mock.ExpectRollback()
			if got, err := ReadPlacementTariffs(context.Background(), db, snapshotTenant, []*ClusterOwnershipSnapshot{owner}); err == nil || got != nil {
				t.Fatalf("invalid snapshot escaped: %+v %v", got, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
